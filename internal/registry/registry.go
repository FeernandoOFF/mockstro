// Package registry records active runs so `status` can show them from any project. Each run
// writes run.json into its own run dir and a copy into the machine-wide registry root.
package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/lock"
)

type Artifact struct {
	Path        string `json:"path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Source      string `json:"source,omitempty"`
	VersionName string `json:"versionName,omitempty"`
	VersionCode string `json:"versionCode,omitempty"`
	InstalledAt string `json:"installedAt,omitempty"`
}

type Run struct {
	SchemaVersion int       `json:"schemaVersion"`
	RunID         string    `json:"runId"`
	Owner         string    `json:"owner"`
	OwnerLabel    string    `json:"ownerLabel"`
	User          string    `json:"user"`
	PID           int       `json:"pid"`
	PIDStartedAt  string    `json:"pidStartedAt"`
	Device        string    `json:"device"`
	Port          int       `json:"port"`
	Platform      string    `json:"platform"`
	Command       string    `json:"command"`
	Tags          string    `json:"tags"`
	StartedAt     string    `json:"startedAt"`
	RunDir        string    `json:"runDir"`
	Worktree      string    `json:"worktree,omitempty"`
	Runner        string    `json:"runner,omitempty"`
	Artifact      *Artifact `json:"artifact,omitempty"`

	// Filled when read back for status.
	State string `json:"state,omitempty"`
	Stage string `json:"stage,omitempty"`
	Flow  string `json:"flow,omitempty"`
}

type Registry struct {
	Root string
}

// Save writes run.json and publishes it to the registry root.
func (r *Registry) Save(run *Run) error {
	local := *run
	local.Worktree = ""
	b, _ := json.Marshal(local)
	if err := fsx.WriteAtomic(filepath.Join(run.RunDir, "run.json"), b); err != nil {
		return err
	}
	pub, _ := json.Marshal(run)
	_ = fsx.WriteAtomic(filepath.Join(r.Root, run.RunID+".json"), pub)
	return nil
}

func (r *Registry) Remove(runID string) {
	os.Remove(filepath.Join(r.Root, runID+".json"))
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
func Active(ctx context.Context, ex execx.Runner, lockRoot, registryRoot string) []Run {
	var runs []Run
	for _, o := range lock.PortLocks(ctx, ex, lockRoot) {
		var run *Run
		if b, err := os.ReadFile(filepath.Join(registryRoot, o.RunID+".json")); err == nil {
			var rr Run
			if json.Unmarshal(b, &rr) == nil {
				run = &rr
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
