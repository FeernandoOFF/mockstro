package lock

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

// fakePS answers `ps` for our own pid (live) and reports pid 999999 as gone.
func fakePS() *execx.Fake {
	self := strconv.Itoa(os.Getpid())
	return &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		args := strings.Join(c.Args, " ")
		switch {
		case c.Name == "ps" && strings.Contains(args, "lstart= -p "+self):
			return "Mon Sep 22 10:00:00 2026", nil
		case c.Name == "ps":
			return "", os.ErrNotExist
		}
		return "", os.ErrInvalid
	}}
}

func TestAcquireCompeteRelease(t *testing.T) {
	root := t.TempDir()
	ex := fakePS()
	a := &Manager{Root: root, Exec: ex, RunID: "first", Who: "agent"}
	b := &Manager{Root: root, Exec: ex, RunID: "second", Who: "agent"}
	if err := a.Acquire("device", "emulator-fake"); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire("device", "emulator-fake"); err == nil {
		t.Fatal("competing acquire succeeded")
	}
	// A competitor's release never removes someone else's lock.
	b.held = []string{DirFor(root, "device", "emulator-fake")}
	b.ReleaseAll()
	if !Held(root, "device", "emulator-fake") || !a.Owns("device", "emulator-fake") {
		t.Fatal("lock removed by non-owner")
	}
	a.ReleaseAll()
	if Held(root, "device", "emulator-fake") {
		t.Fatal("lock not released")
	}
}

func TestIncompleteLockIsBusyAndSweepRules(t *testing.T) {
	root := t.TempDir()
	ex := fakePS()
	dir := DirFor(root, "device", "emulator-fake")
	os.MkdirAll(dir, 0o755)
	m := &Manager{Root: root, Exec: ex, RunID: "x", Who: "agent"}
	if err := m.Acquire("device", "emulator-fake"); err == nil {
		t.Fatal("half-written lock treated as free")
	}
	ctx := context.Background()
	Sweep(ctx, ex, root, false)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("sweep removed an incomplete lock")
	}
	write := func(s string) { os.WriteFile(filepath.Join(dir, "owner"), []byte(s), 0o644) }
	write(`{"runId":"live","pid":` + strconv.Itoa(os.Getpid()) + `,"pidStartedAt":"Mon Sep 22 10:00:00 2026"}`)
	Sweep(ctx, ex, root, false)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("sweep removed a live lock")
	}
	write(`{"runId":"dead","pid":999999,"pidStartedAt":"x"}`)
	Sweep(ctx, ex, root, true)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("dry run removed a lock")
	}
	acts := Sweep(ctx, ex, root, false)
	if _, err := os.Stat(dir); err == nil || len(acts) != 1 || !acts[0].Released {
		t.Fatalf("dead lock not reclaimed: %+v", acts)
	}
	os.MkdirAll(dir+".reclaim", 0o755)
	if err := m.Acquire("device", "emulator-fake"); err == nil {
		t.Fatal("acquired while a reclaim was in progress")
	}
}

func TestLockRaceHasOneWinner(t *testing.T) {
	root := t.TempDir()
	ex := fakePS()
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := &Manager{Root: root, Exec: ex, RunID: "c" + strconv.Itoa(i), Who: "agent"}
			if m.Acquire("device", "emulator-race") == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("expected one winner, got %d", winners)
	}
}

func TestAcquireReclaimsLockOfDeadOwner(t *testing.T) {
	root := t.TempDir()
	ex := fakePS()
	dir := DirFor(root, "device", "emulator-fake")
	os.MkdirAll(dir, 0o755)
	write := func(s string) { os.WriteFile(filepath.Join(dir, "owner"), []byte(s), 0o644) }
	m := &Manager{Root: root, Exec: ex, RunID: "new", Who: "agent"}

	write(`{"runId":"live","pid":` + strconv.Itoa(os.Getpid()) + `,"pidStartedAt":"Mon Sep 22 10:00:00 2026"}`)
	if err := m.Acquire("device", "emulator-fake"); err == nil {
		t.Fatal("took a live run's lock")
	}
	write(`{"runId":"killed","pid":999999,"pidStartedAt":"x"}`)
	if err := m.Acquire("device", "emulator-fake"); err != nil {
		t.Fatalf("dead run's lock not reclaimed: %v", err)
	}
	if !m.Owns("device", "emulator-fake") {
		t.Fatal("reclaimed lock not owned by the new run")
	}
}
