// Package lock implements the device/port locks: an atomic mkdir per resource plus an owner
// record, in one machine-wide root shared by every project.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/feernandooff/mockstro/internal/execx"
)

type Owner struct {
	RunID        string `json:"runId"`
	PID          int    `json:"pid"`
	PIDStartedAt string `json:"pidStartedAt"`
	Owner        string `json:"owner"`
	Kind         string `json:"kind"`
	Key          string `json:"key"`
}

// Manager acquires locks on behalf of one run.
type Manager struct {
	Root  string
	Exec  execx.Runner
	RunID string
	Who   string // human | agent | ci

	mu   sync.Mutex
	held []string
}

var unsafeKey = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func DirFor(root, kind, key string) string {
	return filepath.Join(root, kind+"-"+unsafeKey.ReplaceAllString(key, "_"))
}

// StartTime returns `ps -o lstart=` for pid; with the pid it identifies a process.
func StartTime(ctx context.Context, ex execx.Runner, pid int) string {
	out, err := ex.Output(ctx, execx.Cmd{Name: "ps", Args: []string{"-o", "lstart=", "-p", strconv.Itoa(pid)}})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var (
	selfStartOnce sync.Once
	selfStart     string
)

func (m *Manager) selfStartTime() string {
	selfStartOnce.Do(func() { selfStart = StartTime(context.Background(), m.Exec, os.Getpid()) })
	return selfStart
}

// Held reports whether root has a lock dir for the resource. Acquire or Sweep may
// reclaim a provably dead owner's directory; live and half-written locks stay busy.
func Held(root, kind, key string) bool {
	st, err := os.Stat(DirFor(root, kind, key))
	return err == nil && st.IsDir()
}

func ReadOwner(dir string) (*Owner, error) {
	b, err := os.ReadFile(filepath.Join(dir, "owner"))
	if err != nil {
		return nil, err
	}
	var o Owner
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (m *Manager) Acquire(kind, key string) error {
	dir, err := m.acquireOne(m.Root, kind, key)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.held = append(m.held, dir)
	m.mu.Unlock()
	return nil
}

var ErrBusy = errors.New("lock busy")

func (m *Manager) acquireOne(root, kind, key string) (string, error) {
	dir := DirFor(root, kind, key)
	reclaim := dir + ".reclaim"
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(reclaim, 0o755); err != nil {
		return "", ErrBusy
	}
	defer os.Remove(reclaim)
	owner := Owner{RunID: m.RunID, PID: os.Getpid(), PIDStartedAt: m.selfStartTime(), Owner: m.Who, Kind: kind, Key: key}
	data, _ := json.Marshal(owner)
	tmp := filepath.Join(root, fmt.Sprintf(".owner.%s.%d", m.RunID, os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		// A killed run never released its locks; reclaim them once their owner is provably gone
		// (the same check sweep uses), rather than treating the device as busy forever.
		if m.Exec == nil || !isDead(context.Background(), m.Exec, dir) || os.RemoveAll(dir) != nil || os.Mkdir(dir, 0o755) != nil {
			os.Remove(tmp)
			return "", ErrBusy
		}
	}
	if err := os.Rename(tmp, filepath.Join(dir, "owner")); err != nil {
		os.Remove(tmp)
		os.Remove(dir)
		return "", err
	}
	return dir, nil
}

// Owns reports whether this run holds the lock.
func (m *Manager) Owns(kind, key string) bool {
	if m.Root == "" {
		return false
	}
	o, err := ReadOwner(DirFor(m.Root, kind, key))
	return err == nil && o.RunID == m.RunID
}

func (m *Manager) releaseDir(dir string) {
	reclaim := dir + ".reclaim"
	if err := os.Mkdir(reclaim, 0o755); err != nil {
		return // guard busy: leave it for an explicit sweep
	}
	defer os.Remove(reclaim)
	if o, err := ReadOwner(dir); err == nil && o.RunID == m.RunID {
		os.RemoveAll(dir)
	}
}

func (m *Manager) ReleaseAll() {
	m.mu.Lock()
	held := m.held
	m.held = nil
	m.mu.Unlock()
	for _, d := range held {
		m.releaseDir(d)
	}
}

// Liveness.

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// IsLive: pid alive AND its start time matches, since pids are recycled.
func IsLive(ctx context.Context, ex execx.Runner, o *Owner) bool {
	if o == nil || o.PID <= 0 || o.PIDStartedAt == "" || !pidAlive(o.PID) {
		return false
	}
	return StartTime(ctx, ex, o.PID) == o.PIDStartedAt
}

// isDead: a complete owner record whose process is provably gone.
func isDead(ctx context.Context, ex execx.Runner, dir string) bool {
	o, err := ReadOwner(dir)
	if err != nil || o.RunID == "" || o.PID <= 0 || o.PIDStartedAt == "" {
		return false
	}
	if pidAlive(o.PID) {
		return false
	}
	out, err := ex.Output(ctx, execx.Cmd{Name: "ps", Args: []string{"-p", strconv.Itoa(o.PID), "-o", "pid="}})
	return err != nil || strings.TrimSpace(out) == ""
}

type SweepAction struct {
	Root, Name, RunID, Note string
	Released                bool
}

// Sweep reclaims locks whose owner is provably gone. Live, incomplete or contended locks stay.
func Sweep(ctx context.Context, ex execx.Runner, root string, dryRun bool) []SweepAction {
	var actions []SweepAction
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasSuffix(name, ".reclaim") || !(strings.HasPrefix(name, "port-") || strings.HasPrefix(name, "device-")) {
			continue
		}
		dir := filepath.Join(root, name)
		if _, err := os.Stat(filepath.Join(dir, "owner")); err != nil {
			actions = append(actions, SweepAction{Root: root, Name: name, Note: "incomplete lock left untouched; inspect and remove manually if no acquisition is running"})
			continue
		}
		if !isDead(ctx, ex, dir) {
			continue
		}
		o, _ := ReadOwner(dir)
		a := SweepAction{Root: root, Name: name, RunID: o.RunID}
		if dryRun {
			a.Note = "would release stale lock; no process/device cleanup"
			actions = append(actions, a)
			continue
		}
		reclaim := dir + ".reclaim"
		if err := os.Mkdir(reclaim, 0o755); err != nil {
			a.Note = "reclaim already in progress; left untouched"
			actions = append(actions, a)
			continue
		}
		if isDead(ctx, ex, dir) {
			os.RemoveAll(dir)
			a.Released = true
			a.Note = "released stale lock; processes and devices left untouched"
			actions = append(actions, a)
		}
		os.Remove(reclaim)
	}
	return actions
}

// PortLocks lists live port locks (one entry per run id).
func PortLocks(ctx context.Context, ex execx.Runner, root string) []*Owner {
	seen := map[string]bool{}
	var out []*Owner
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "port-") || strings.HasSuffix(e.Name(), ".reclaim") {
			continue
		}
		o, err := ReadOwner(filepath.Join(root, e.Name()))
		if err != nil || seen[o.RunID] || !IsLive(ctx, ex, o) {
			continue
		}
		seen[o.RunID] = true
		out = append(out, o)
	}
	return out
}
