package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/feernandooff/trun/internal/lock"
	"github.com/feernandooff/trun/internal/registry"
)

// DefaultKeepRuns is how many finished run dirs a project keeps (TRUN_KEEP_RUNS overrides).
const DefaultKeepRuns = 20

type RunDir struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	Active  bool
	Session bool
	Workers []string // a session's worker run dirs; they are listed, sized and deleted with it
}

func KeepRuns() int {
	if n, err := strconv.Atoi(os.Getenv("TRUN_KEEP_RUNS")); err == nil && n >= 0 {
		return n
	}
	return DefaultKeepRuns
}

// DirSize sums regular files without following symlinks (run dirs link to shared/ and __files/).
func DirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// RunDirs lists this project's run dirs, newest first. withSize walks each dir.
func (e *Engine) RunDirs(ctx context.Context, withSize bool) []RunDir {
	active := map[string]bool{}
	for _, r := range registry.Active(ctx, e.Exec, e.LockRoots(), e.RegistryRoots()) {
		if r.RunDir != "" {
			active[filepath.Clean(r.RunDir)] = true
		}
	}
	root := e.P.RunsRoot()
	entries, _ := os.ReadDir(root)
	var out []RunDir
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		p := filepath.Join(root, ent.Name())
		info, err := ent.Info()
		if err != nil {
			continue
		}
		rd := RunDir{Path: p, Name: ent.Name(), ModTime: info.ModTime(), Session: strings.HasPrefix(ent.Name(), "session-")}
		rd.Active = active[filepath.Clean(p)] || e.sessionActive(p, active) || e.ownerAlive(ctx, p, info.ModTime())
		if withSize {
			rd.Size = DirSize(p)
		}
		out = append(out, rd)
	}
	// Fold each session's worker dirs into the session row.
	owner := map[string]int{}
	for i := range out {
		if !out[i].Session {
			continue
		}
		links, _ := filepath.Glob(filepath.Join(out[i].Path, "worker-*"))
		for _, l := range links {
			if t, err := os.Readlink(l); err == nil && filepath.Dir(filepath.Clean(t)) == filepath.Clean(root) {
				owner[filepath.Clean(t)] = i
			}
		}
	}
	for i := range out {
		if si, ok := owner[filepath.Clean(out[i].Path)]; ok {
			sess := &out[si]
			sess.Workers = append(sess.Workers, out[i].Path)
			sess.Size += out[i].Size
			sess.Active = sess.Active || out[i].Active
		}
	}
	var folded []RunDir
	for _, rd := range out {
		if _, isWorker := owner[filepath.Clean(rd.Path)]; !isWorker {
			folded = append(folded, rd)
		}
	}
	sort.Slice(folded, func(i, j int) bool { return folded[i].ModTime.After(folded[j].ModTime) })
	return folded
}

// A parallel session is active while any of its workers is.
func (e *Engine) sessionActive(dir string, active map[string]bool) bool {
	links, _ := filepath.Glob(filepath.Join(dir, "worker-*"))
	for _, l := range links {
		if t, err := os.Readlink(l); err == nil && active[filepath.Clean(t)] {
			return true
		}
	}
	return false
}

// ownerAlive: a dir without a finished result still belongs to a run while the process that
// created it is alive (pid + start time from run.json; the pid in a session dir's name).
func (e *Engine) ownerAlive(ctx context.Context, dir string, mod time.Time) bool {
	if _, err := os.Stat(filepath.Join(dir, "result.tsv")); err == nil {
		return false
	}
	if b, err := os.ReadFile(filepath.Join(dir, "stage")); err == nil && strings.TrimSpace(string(b)) == "DONE" {
		return false
	}
	if b, err := os.ReadFile(filepath.Join(dir, "run.json")); err == nil {
		var o lock.Owner
		if json.Unmarshal(b, &o) == nil && o.PID > 0 {
			return lock.IsLive(ctx, e.Exec, &o)
		}
	}
	if parts := strings.Split(filepath.Base(dir), "-"); len(parts) >= 4 && parts[0] == "session" {
		if pid, err := strconv.Atoi(parts[2]); err == nil && pid > 0 {
			return syscall.Kill(pid, 0) == nil
		}
	}
	// Created a moment ago and not described yet.
	return time.Since(mod) < time.Minute
}

// DeleteRunDir removes one finished run dir; active runs are refused.
func (e *Engine) DeleteRunDir(ctx context.Context, path string) error {
	path = filepath.Clean(path)
	if filepath.Dir(path) != filepath.Clean(e.P.RunsRoot()) {
		return fmt.Errorf("%s is not a run dir", path)
	}
	for _, rd := range e.RunDirs(ctx, false) {
		if rd.Path != path {
			continue
		}
		if rd.Active {
			return fmt.Errorf("%s belongs to an active run", rd.Name)
		}
		removeGroup(rd)
		return nil
	}
	return os.RemoveAll(path)
}

// Prune keeps the newest `keep` finished run dirs and strips APK snapshots from the rest of
// the finished ones (older runs, including test.sh's, kept a full copy each).
func (e *Engine) Prune(ctx context.Context, keep int) (removed int, freed int64) {
	kept := 0
	for _, rd := range e.RunDirs(ctx, false) {
		if rd.Active {
			continue
		}
		if kept < keep {
			kept++
			freed += stripArtifacts(rd.Path)
			for _, w := range rd.Workers {
				freed += stripArtifacts(w)
			}
			continue
		}
		freed += removeGroup(rd)
		removed++
	}
	return
}

func stripArtifacts(dir string) int64 {
	art := filepath.Join(dir, "artifacts")
	size := DirSize(art)
	if size > 0 {
		os.RemoveAll(art)
	}
	return size
}
