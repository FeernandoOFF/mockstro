// Package maestro owns Maestro commands, generated workspaces, hooks, logs and artifacts.
// The caller supplies per-flow setup and verification; device and mock ownership stay outside.
package maestro

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"gopkg.in/yaml.v3"
)

func Command(ex execx.Runner) (string, error) {
	if _, err := ex.LookPath("maestro"); err == nil {
		return "maestro", nil
	}
	dir := os.Getenv("MAESTRO_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "maestro", err
		}
		dir = filepath.Join(home, ".maestro")
	}
	path, err := ex.LookPath(filepath.Join(dir, "bin", "maestro"))
	if err != nil {
		return "maestro", err
	}
	return path, nil
}

type Workspace struct {
	Dir        string
	FlowsRoot  string
	SharedRoot string
	ConfigPath string
}

type Executor struct {
	Exec      execx.Runner
	Device    string
	Workspace Workspace
	Output    func(flow, line string)
	Log       func(string)
}

type Flow struct {
	Source    *fixtures.Flow
	Variables map[string]string
	LogPath   string
}

type Artifacts struct {
	Dir   string
	Shots []string
}

type Outcome struct {
	Exit      int
	Err       error
	Setup     bool
	Cancelled bool
	Unstarted bool
	Artifacts Artifacts
}

// Hooks run serially. Initialize prepares a result without activating fixtures;
// Start activates a flow that Maestro is actually about to execute.
// Complete returns whether the flow passed all caller verifications.
// Publish runs after Maestro has finished writing that flow's artifacts.
type Hooks struct {
	Initialize func(int)
	Start      func(int) error
	Complete   func(int, Outcome) bool
	Publish    func(int, Artifacts)
	Stopped    func() bool
}

func (x *Executor) output(flow, line string) {
	if x.Output != nil {
		x.Output(flow, line)
	}
}
func (x *Executor) log(s string) {
	if x.Log != nil {
		x.Log(s)
	}
}

func (w Workspace) Sanitize(f *fixtures.Flow) (string, error) {
	out := filepath.Join(w.FlowsRoot, "flows", filepath.FromSlash(f.Rel))
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", err
	}
	link := filepath.Join(w.FlowsRoot, "shared")
	if _, err := os.Lstat(link); os.IsNotExist(err) {
		if err := os.Symlink(w.SharedRoot, link); err != nil {
			return "", err
		}
	}
	data, err := fixtures.SanitizedFlow(f.Path)
	if err != nil {
		return "", fmt.Errorf("sanitize %s: %w", f.Rel, err)
	}
	return out, os.WriteFile(out, data, 0644)
}

// FlowPath keeps original relative paths for namespaced flows. Legacy reporting
// properties still need the generated copy used before metadata was namespaced.
func (w Workspace) FlowPath(f *fixtures.Flow) (string, error) {
	header, err := fixtures.ParseHeader(f.Path)
	if err != nil {
		return "", err
	}
	if !header.LegacyMetadata() {
		return f.Path, nil
	}
	return w.Sanitize(f)
}

func (w Workspace) BatchConfig() (string, error) {
	cfg := map[string]any{}
	if data, err := os.ReadFile(w.ConfigPath); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("%s: %w", w.ConfigPath, err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	for _, key := range []string{"flows", "includeTags", "excludeTags", "executionOrder", "testOutputDir"} {
		delete(cfg, key)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(w.Dir, "batch-config.yaml")
	return path, os.WriteFile(path, data, 0644)
}

// Run executes one flow, optionally recording to the supplied path.
func (x *Executor) Run(ctx context.Context, f Flow, recording string) Outcome {
	path, err := x.Workspace.FlowPath(f.Source)
	if err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	command, err := Command(x.Exec)
	if err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	if err := os.MkdirAll(filepath.Dir(f.LogPath), 0755); err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	log, err := os.Create(f.LogPath)
	if err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	defer log.Close()
	lw := execx.NewLines(log, func(line string) { x.output(f.Source.Rel, line) })
	defer lw.Flush()
	var args []string
	if x.Device != "" {
		args = append(args, "--device", x.Device)
	}
	mode := "test"
	if recording != "" {
		mode = "record"
	}
	args = append(args, mode)
	if recording != "" {
		args = append(args, "--local")
	}
	root := filepath.Join(x.Workspace.Dir, "results")
	if err := os.MkdirAll(root, 0755); err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	debug, err := os.MkdirTemp(root, strings.TrimSuffix(filepath.Base(f.LogPath), ".log")+"-")
	if err != nil {
		return Outcome{Exit: 1, Err: err, Setup: true}
	}
	args = append(args, "--config", x.Workspace.ConfigPath, "--debug-output", debug)
	if recording == "" {
		args = append(args, "--flatten-debug-output")
	}
	for _, key := range slices.Sorted(maps.Keys(f.Variables)) {
		args = append(args, "-e", key+"="+f.Variables[key])
	}
	args = append(args, path)
	if recording != "" {
		args = append(args, recording)
	}
	err = x.Exec.Run(ctx, execx.Cmd{Name: command, Args: args, Stdout: lw, Stderr: lw})
	out := outcome(ctx, err)
	out.Artifacts = findArtifacts(debug)
	return out
}

func outcome(ctx context.Context, err error) Outcome {
	exit := execx.ExitCode(err)
	cancelled := ctx.Err() != nil || exit == 130 || exit == 143
	if cancelled && exit <= 0 {
		exit = 130
	}
	if err != nil && exit < 0 {
		exit = 1
	}
	return Outcome{Exit: exit, Err: err, Cancelled: cancelled}
}
