// Package registry records active runs so `status` can show them from any project. Each run
// writes run.json into its own run dir and a copy into every registry root (the global one and,
// for test.sh compatibility, the repository's legacy one).
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/lock"
)

type APK struct {
	Path        string `json:"path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Source      string `json:"source,omitempty"`
	VersionName string `json:"versionName,omitempty"`
	VersionCode string `json:"versionCode,omitempty"`
	InstalledAt string `json:"installedAt,omitempty"`
}

type Run struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Owner         string `json:"owner"`
	OwnerLabel    string `json:"ownerLabel"`
	User          string `json:"user"`
	PID           int    `json:"pid"`
	PIDStartedAt  string `json:"pidStartedAt"`
	Device        string `json:"device"`
	Port          int    `json:"port"`
	Platform      string `json:"platform"`
	Command       string `json:"command"`
	Tags          string `json:"tags"`
	StartedAt     string `json:"startedAt"`
	RunDir        string `json:"runDir"`
	Worktree      string `json:"worktree,omitempty"`
	Runner        string `json:"runner,omitempty"`
	APK           *APK   `json:"apk,omitempty"`

	// Filled when read back for status.
	State string `json:"state,omitempty"`
	Stage string `json:"stage,omitempty"`
	Flow  string `json:"flow,omitempty"`
}

type Registry struct {
	Roots []string
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Save writes run.json and publishes it to every registry root.
func (r *Registry) Save(run *Run) error {
	local := *run
	local.Worktree = ""
	b, _ := json.Marshal(local)
	if err := writeAtomic(filepath.Join(run.RunDir, "run.json"), b); err != nil {
		return err
	}
	pub, _ := json.Marshal(run)
	for _, root := range r.Roots {
		_ = writeAtomic(filepath.Join(root, run.RunID+".json"), pub)
	}
	return nil
}

func (r *Registry) Remove(runID string) {
	for _, root := range r.Roots {
		os.Remove(filepath.Join(root, runID+".json"))
	}
}

// Stage records what a run is doing, read by status.
func Stage(runDir, stage, flow string) {
	if runDir == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(runDir, "stage"), []byte(stage+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(runDir, "current-flow"), []byte(flow+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(runDir, "heartbeat"), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func readTrim(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// Active is recomputed from live port locks every time.
func Active(ctx context.Context, ex execx.Runner, lockRoots, registryRoots []string) []Run {
	var runs []Run
	for _, o := range lock.PortLocks(ctx, ex, lockRoots) {
		var run *Run
		for _, root := range registryRoots {
			b, err := os.ReadFile(filepath.Join(root, o.RunID+".json"))
			if err != nil {
				continue
			}
			var rr Run
			if json.Unmarshal(b, &rr) == nil {
				run = &rr
				break
			}
		}
		if run == nil {
			run = &Run{RunID: o.RunID, PID: o.PID, Owner: o.Owner}
		}
		run.State = "active"
		if run.RunDir != "" {
			run.Stage = readTrim(filepath.Join(run.RunDir, "stage"))
			run.Flow = readTrim(filepath.Join(run.RunDir, "current-flow"))
		}
		if run.Stage == "" {
			run.Stage = "UNKNOWN"
		}
		runs = append(runs, *run)
	}
	return runs
}
