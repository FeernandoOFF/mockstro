package engine

import (
	"maps"
	"sync"
	"testing"
)

func newTestSession(t *MockTarget) *MockSession {
	return &MockSession{Run: &Run{Port: 18080, Device: "emu-1"}, target: t, fbrc: maps.Clone(t.FBRC)}
}

func TestSnapshotCopiesTarget(t *testing.T) {
	target := &MockTarget{Kind: "scenario", Name: "elec-dpn", Profile: "elec-dpn", Overrides: []string{"a/b"}, FBRC: map[string]string{"K": "true"}}
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
	s := newTestSession(&MockTarget{Kind: "profile", Name: "one"})
	next := &MockTarget{Kind: "profile", Name: "two"}

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
			s.mu.Lock()
			s.target = next
			s.mu.Unlock()
		}
	}()
	wg.Wait()

	if got := s.Target().Name; got != "two" {
		t.Fatalf("target = %q, want two", got)
	}
}
