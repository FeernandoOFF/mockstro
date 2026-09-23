package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/platform"
	"github.com/feernandooff/trun/internal/project"
	"github.com/feernandooff/trun/internal/wiremock"
)

// BuildMappings layers overrides (priority 1), the profile (3) and shared defaults (5).
func (e *Engine) BuildMappings(profile string, overrides []string) ([]any, error) {
	var out []any
	add := func(names []string, prio int) error {
		for _, n := range names {
			stub, err := e.Store.ResolvedStub(n)
			if err != nil {
				return fmt.Errorf("failed to read stub '%s': %w", n, err)
			}
			stub = stub.Clone()
			stub.Set("priority", json.Number(strconv.Itoa(prio)))
			out = append(out, stub)
		}
		return nil
	}
	if err := add(overrides, 1); err != nil {
		return nil, err
	}
	if profile != "" {
		pm, err := e.Store.ProfileMappings(profile)
		if err != nil {
			return nil, err
		}
		if err := add(pm, 3); err != nil {
			return nil, err
		}
	}
	shared, err := e.Store.ProfileMappings("shared")
	if err != nil {
		return nil, err
	}
	if err := add(shared, 5); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Run) override(fbrc map[string]string) platform.Override {
	return platform.Override{
		RunID: r.ID, Port: r.Port, FBRC: fbrc,
		ExpiresAt: time.Now().Add(project.OverrideTTLSecs * time.Second).UnixMilli(),
	}
}

// Activate loads a scenario into this run's WireMock and points the device at it.
func (r *Run) Activate(ctx context.Context, profile string, fbrc map[string]string, overrides []string) error {
	e := r.e
	mappings, err := e.BuildMappings(profile, overrides)
	if err != nil {
		return err
	}
	// Remote Config rides in the device override; only Map Remote setups serve it over HTTP.
	if r.Device != "" && r.overrideAuto {
		if err := e.Plat.WriteOverride(ctx, r.Device, r.override(fbrc)); err != nil {
			return err
		}
		r.overrideApplied = true
	}
	if !r.overrideAuto {
		mappings = append(mappings, wiremock.FBRCMappings(fbrc)...)
	}
	if !r.preflightDone && r.Device != "" {
		if r.overrideAuto {
			msg, err := e.Plat.PreflightOverride(ctx, r.Device, r.override(fbrc))
			if err != nil {
				return err
			}
			r.logf("%s", msg)
		} else {
			r.logf("%s", e.ManualProxyInstructions(r.Port, r.Device))
		}
		r.preflightDone = true
	}
	mappings = append(mappings, wiremock.FallbackMapping(e.P.FallbackURL), wiremock.NonceMapping(r.ID))
	return r.WM.Replace(mappings)
}

func (e *Engine) ManualProxyInstructions(port int, device string) string {
	s := "\nManual interception (server-only mock). Map Remote in Proxyman/Charles:\n"
	if e.Opts.MapRemote {
		s = fmt.Sprintf("\nManual interception (--map-remote on %s). Map Remote in Proxyman/Charles:\n", device)
	}
	for _, u := range []string{e.P.ProxyBaseURL, e.P.FBRCURL} {
		s += fmt.Sprintf("  Match:  %s/*\n  Map To: protocol=http, host=127.0.0.1, port=%d\n", u, port)
	}
	s += "  Leave Path and Query empty; disable Preserve Host Header.\n"
	s += "  The app is NOT modified: no override is written and no adb reverse is installed.\n"
	s += fmt.Sprintf("  FBRC overrides are served over HTTP, so they need the %s rule.\n", e.P.FBRCURL)
	return s
}

func (r *Run) sanitize(f *fixtures.Flow) (string, error) {
	out := filepath.Join(r.flowsRoot, "flows", filepath.FromSlash(f.Rel))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(r.flowsRoot, "shared")
	if _, err := os.Lstat(link); err != nil {
		if err := os.Symlink(r.e.P.SharedRoot(), link); err != nil {
			return "", err
		}
	}
	data, err := fixtures.SanitizedFlow(f.Path)
	if err != nil {
		return "", fmt.Errorf("sanitize %s: %w", f.Rel, err)
	}
	return out, os.WriteFile(out, data, 0o644)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func safeName(s string) string {
	s = unsafeName.ReplaceAllString(s, "-")
	if s == "" {
		return "flow"
	}
	return s
}

func (r *Run) recordingPath(f *fixtures.Flow) string {
	dir := r.e.Opts.RecordDir
	if dir == "" {
		dir = filepath.Join(r.Dir, "recordings")
	}
	name := FlowSlug(f.Feature, f.File) + ".mp4"
	return filepath.Join(dir, name)
}

func trimExt(s string) string { return s[:len(s)-len(filepath.Ext(s))] }

// RunFlow executes one flow end to end and always returns exactly one result.
func (r *Run) RunFlow(ctx context.Context, f *fixtures.Flow) *Result {
	e := r.e
	res := &Result{Feature: f.Feature, File: f.File, Path: f.Path, Worker: r.Worker, Device: r.Device}
	start := time.Now()
	finish := func(stage string, exit int) *Result {
		res.Stage, res.Exit, res.Duration = stage, exit, time.Since(start)
		r.e.emit(Event{Kind: EvFlowResult, Worker: r.Worker, RunID: r.ID, Flow: f.Rel, Result: res})
		return res
	}

	profile := e.Opts.ProfileArg
	if profile == "" {
		profile = f.Header.Profile
	}
	r.e.emit(Event{Kind: EvFlowStart, Worker: r.Worker, RunID: r.ID, Flow: f.Rel, Device: r.Device, Text: profile})
	r.stage("FLOW", f.Rel)
	if err := r.Activate(ctx, profile, e.FBRCFor(f.Header), f.Header.Overrides); err != nil {
		res.Message = err.Error()
		r.warnf("%v", err)
		return finish("SETUP", 1)
	}
	flowPath, err := r.sanitize(f)
	if err != nil {
		res.Message = err.Error()
		return finish("SETUP", 1)
	}

	logDir := filepath.Join(r.Dir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	slug := FlowSlug(f.Feature, f.File)
	res.LogPath = filepath.Join(logDir, slug+".log")
	// Maestro's own debug log would otherwise pile up in ~/.maestro/tests forever.
	debugDir := filepath.Join(r.Dir, "maestro-debug", slug)
	logFile, _ := os.Create(res.LogPath)
	lw := &lineWriter{tee: logFile, emit: func(line string) {
		e.emit(Event{Kind: EvOutput, Worker: r.Worker, RunID: r.ID, Flow: f.Rel, Text: line})
	}}

	var args []string
	if r.Device != "" {
		args = append(args, "--device", r.Device)
	}
	stage := "TEST"
	envArg := "WIREMOCK_PORT=" + strconv.Itoa(r.Port)
	if e.Opts.Record {
		stage = "RECORD"
		res.Recording = r.recordingPath(f)
		_ = os.MkdirAll(filepath.Dir(res.Recording), 0o755)
		target := res.Recording
		if e.Opts.Optimize {
			target = rawPath(res.Recording)
		}
		r.logf("Recording %s/%s [%s]", f.Feature, f.File, profile)
		args = append(args, "record", "--local", "--config", r.configPath, "--debug-output", debugDir, "-e", envArg, flowPath, target)
	} else {
		r.logf("Running %s/%s [%s]", f.Feature, f.File, profile)
		args = append(args, "test", "--config", r.configPath, "--debug-output", debugDir, "-e", envArg, flowPath)
	}
	before := r.resultDirs()
	runErr := e.Exec.Run(ctx, execx.Cmd{Name: "maestro", Args: args, Stdout: lw, Stderr: lw})
	res.Artifacts, res.Shots = r.newArtifacts(before)
	lw.Flush()
	if logFile != nil {
		logFile.Close()
	}
	exit := execx.ExitCode(runErr)
	if ctx.Err() != nil || exit == 130 || exit == 143 {
		if exit <= 0 {
			exit = 130
		}
		if res.Recording != "" {
			if raw := rawPath(res.Recording); e.Opts.Optimize && fileExists(raw) {
				res.Recording = raw
			} else if !fileExists(res.Recording) {
				res.Recording = ""
			}
		}
		return finish("CANCELLED", exit)
	}
	if runErr != nil && exit < 0 {
		res.Message = runErr.Error()
		exit = 1
	}

	if !r.acceptedOverride(ctx) {
		res.Message = fmt.Sprintf("App never accepted run %s on %s: no matching acceptance signal (stale build without the E2E seam?)", r.ID, r.Device)
		r.warnf("%s", res.Message)
		return finish("OVERRIDE", 1)
	}
	verifyOK := true
	if len(f.Header.Verifications) > 0 {
		v, err := r.Verify(f.Header.Verifications)
		res.Verifs = v
		if err != nil {
			r.warnf("%v", err)
			verifyOK = false
		}
		for _, x := range v {
			if !x.Pass {
				verifyOK = false
			}
		}
	}
	res.Unmatched = r.reportFallback()
	if !verifyOK {
		stage, exit = "VERIFY", 1
	}
	if exit == 0 {
		stage = "PASS"
	}
	return finish(stage, exit)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// acceptedOverride reads back what the app published, so a build without the seam fails.
func (r *Run) acceptedOverride(ctx context.Context) bool {
	if !r.overrideAuto || r.Device == "" {
		return true
	}
	return r.e.Plat.AcceptedRunID(ctx, r.Device) == r.ID
}

// Verify counts journal matches per named stub.
func (r *Run) Verify(vs []fixtures.Verification) ([]VerificationResult, error) {
	journal, err := r.WM.Journal(true)
	if err != nil {
		return nil, err
	}
	var out []VerificationResult
	r.logf("API verifications:")
	for _, v := range vs {
		count := 0
		for _, req := range journal {
			if req.StubName() == v.Stub {
				count++
			}
		}
		lo, hi := v.Bounds()
		vr := VerificationResult{Stub: v.Stub, Expectation: v.Expectation(), Got: count, Pass: count >= lo && count <= hi}
		if vr.Pass {
			r.logf("  PASS %s: expected %s, got %d", v.Stub, vr.Expectation, count)
		} else {
			r.warnf("  FAIL %s: expected %s, got %d", v.Stub, vr.Expectation, count)
		}
		out = append(out, vr)
	}
	return out, nil
}

// Browser noise from WebViews hitting the mock origin; not the app calling an API.
var browserNoise = map[string]bool{"/": true, "/favicon.ico": true, "/generate_204": true}

// reportFallback lists unstubbed requests. Reported, never a verdict.
func (r *Run) reportFallback() []string {
	journal, err := r.WM.Journal(true)
	if err != nil {
		r.warnf("%v", err)
		return nil
	}
	var unmatched []string
	ignored := 0
	for _, req := range journal {
		if req.StubName() != wiremock.Fallback {
			continue
		}
		if browserNoise[req.Path()] {
			ignored++
			continue
		}
		unmatched = append(unmatched, req.Request.Method+" "+req.Request.URL)
	}
	switch {
	case len(unmatched) > 0:
		r.warnf("Fallback requests: %d (unstubbed)", len(unmatched))
		for _, u := range unmatched {
			r.warnf("  %s", u)
		}
	case ignored > 0:
		r.logf("Fallback requests: 0 (ignored %d non-API browser requests)", ignored)
	default:
		r.logf("Fallback requests: 0")
	}
	return unmatched
}

// OptimizeRecordings re-encodes raw recordings with HandBrakeCLI.
func (e *Engine) OptimizeRecordings(ctx context.Context, results []*Result) bool {
	ok := true
	for _, res := range results {
		if res.Recording == "" {
			continue
		}
		raw := rawPath(res.Recording)
		if !fileExists(raw) {
			if res.Passed() {
				e.Warnf("Missing raw recording for %s/%s: %s", res.Feature, res.File, e.P.Rel(raw))
				res.Stage, res.Exit, res.Recording = "OPTIMIZE", 1, ""
				ok = false
			} else {
				res.Recording = ""
			}
			continue
		}
		e.Logf("Optimizing %s/%s", res.Feature, res.File)
		err := e.Exec.Run(ctx, execx.Cmd{Name: "HandBrakeCLI", Args: []string{"--output", res.Recording, "--input", raw}})
		if err == nil {
			os.Remove(raw)
			continue
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return false
		}
		res.Stage, res.Exit, res.Recording = "OPTIMIZE", execx.ExitCode(err), raw
		ok = false
	}
	return ok
}

// resultDirs lists Maestro's per-flow output dirs (results/<timestamp>/<flow name>).
func (r *Run) resultDirs() map[string]bool {
	out := map[string]bool{}
	matches, _ := filepath.Glob(filepath.Join(r.Dir, "results", "*", "*"))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			out[m] = true
		}
	}
	return out
}

// newArtifacts finds the output dir this flow produced and its screenshots, in step order.
func (r *Run) newArtifacts(before map[string]bool) (string, []string) {
	for d := range r.resultDirs() {
		if before[d] {
			continue
		}
		shots, _ := filepath.Glob(filepath.Join(d, "screenshots", "*.png"))
		sort.Strings(shots)
		return d, shots
	}
	return "", nil
}

// FlowSlug names a flow's log, debug dir and recording: <feature>-<file without .yaml>.
func FlowSlug(feature, file string) string { return safeName(feature) + "-" + safeName(trimExt(file)) }

// rawPath is where a recording is written before HandBrake optimises it.
func rawPath(recording string) string { return strings.TrimSuffix(recording, ".mp4") + ".raw.mp4" }
