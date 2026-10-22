package mockruntime

import (
	"context"
	"sync"
	"testing"
)

func newTestSession(t *Target) *Session {
	return New(&testRuntime{}, t, 18080, "emu-1")
}

func TestSnapshotCopiesTarget(t *testing.T) {
	target := &Target{Kind: "scenario", Name: "elec-dpn", Profile: "elec-dpn", Overrides: []string{"a/b"}, FBRC: map[string]string{"K": "true"}}
	s := newTestSession(target)

	snap := s.Snapshot()
	if snap.Name != "elec-dpn" || snap.Port != 18080 || snap.Device != "emu-1" || snap.FBRC["K"] != "true" {
		t.Fatalf("snapshot = %+v", snap)
	}

	snap.Overrides[0] = "changed"
	snap.FBRC["K"] = "false"
	if target.Overrides[0] != "a/b" || target.FBRC["K"] != "true" {
		t.Fatalf("snapshot aliases the live target: %+v", target)
	}
}

func TestTargetIsSafeUnderConcurrentSwitch(t *testing.T) {
	s := newTestSession(&Target{Kind: "profile", Name: "one"})
	next := &Target{Kind: "profile", Name: "two"}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = s.Snapshot()
				_ = s.Target().Name
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			if err := s.Switch(context.Background(), next); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()

	if got := s.Target().Name; got != "two" {
		t.Fatalf("target = %q, want two", got)
	}
}

func TestTargetAndActivationInputsAreCopied(t *testing.T) {
	target := &Target{Name: "original", Overrides: []string{"one"}, FBRC: map[string]string{"K": "value"}}
	s := newTestSession(target)
	target.Name = "caller"
	target.Overrides[0] = "caller"
	target.FBRC["K"] = "caller"
	copy := s.Target()
	copy.Name = "reader"
	copy.Overrides[0] = "reader"
	copy.FBRC["K"] = "reader"
	if got := s.Snapshot(); got.Name != "original" || got.Overrides[0] != "one" || got.FBRC["K"] != "value" || got.Defaults["K"] != "value" {
		t.Fatalf("aliased input or target: %+v", got)
	}
	next := &Target{Name: "next", FBRC: map[string]string{"K": "next"}}
	if err := s.Switch(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	next.FBRC["K"] = "caller"
	if s.Snapshot().FBRC["K"] != "next" {
		t.Fatal("switch retained caller map")
	}
}
