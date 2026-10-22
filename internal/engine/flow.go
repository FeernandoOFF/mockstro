package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/maestro"
	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
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
		RunID: r.ID, Port: r.Port, FBRC: fbrc, WebSocketPath: r.e.P.WebSocketPath,
		ExpiresAt: time.Now().Add(project.OverrideTTLSecs * time.Second).UnixMilli(),
	}
}

// Activate loads a scenario into this run's WireMock and points the device at it.
func (r *Run) Activate(ctx context.Context, profile string, fbrc map[string]string, overrides []string) error {
	_, err := r.activate(ctx, profile, fbrc, overrides)
	return err
}

// activate reports whether a switch invalidated the previous mappings, even if
// delivering the device override or a later readiness check fails.
func (r *Run) activate(ctx context.Context, profile string, fbrc map[string]string, overrides []string) (reset bool, err error) {
	clean, err := remoteconfig.Validate(fbrc)
	if err != nil {
		return reset, err
	}
	fbrc = clean
	e := r.e
	mappings, err := e.BuildMappings(profile, overrides)
	if err != nil {
		return reset, err
	}
	var httpMappings, messageMappings []any
	for _, mapping := range mappings {
		if stub, ok := mapping.(*ojson.Object); ok && fixtures.IsMessage(stub) {
			messageMappings = append(messageMappings, mapping)
		} else {
			httpMappings = append(httpMappings, mapping)
		}
	}
	websocket := len(messageMappings) > 0 || e.P.WebSocketPath != ""
	if websocket && !r.WM.MessageSupport {
		return reset, fmt.Errorf("WebSocket fixtures require wiremockVersion: %s", wiremock.WebSocketVersion)
	}
	if websocket || r.websocketActive {
		if r.Device != "" {
			if err := e.Plat.StopApp(ctx, r.Device); err != nil {
				return reset, fmt.Errorf("stop app before WebSocket scenario: %w", err)
			}
		}
		if r.websocketActive {
			reset = true
			r.WM.Stop()
			r.WM = nil
			if err := r.startWiremock(ctx); err != nil {
				return reset, err
			}
		}
	}
	mappings = httpMappings
	delivery := r.RemoteConfigDelivery()
	mappings = append(mappings, delivery.Mappings(fbrc)...)
	mappings = append(mappings, wiremock.FallbackMapping(e.P.FallbackURL), wiremock.NonceMapping(r.ID))
	replaced, err := r.WM.Replace(mappings, messageMappings)
	reset = reset || replaced
	if err != nil {
		return reset, err
	}
	r.websocketActive = websocket
	// Remote Config rides in the device override; only Map Remote setups serve it over HTTP.
	if delivery.UsesDeviceOverride() {
		if err := delivery.Deliver(ctx, fbrc); err != nil {
			return reset, err
		}
	}

	if !r.preflightDone && r.Device != "" {
		if r.overrideAuto {
			msg, err := e.Plat.PreflightOverride(ctx, r.Device, r.override(fbrc))
			if err != nil {
				return reset, err
			}
			r.logf("%s", msg)
		} else {
			r.logf("%s", e.ManualProxyInstructions(r.Port, r.Device))
		}
		r.preflightDone = true
	}
	return reset, nil
}

func (e *Engine) ManualProxyInstructions(port int, device string) string {
	s := "\nManual interception (server-only mock). Map Remote in Proxyman/Charles:\n"
	if e.Opts.MapRemote {
		s = fmt.Sprintf("\nManual interception (--map-remote on %s). Map Remote in Proxyman/Charles:\n", device)
	}
	api := e.P.ProxyBaseURL
	if api == "" {
		api = "<your API base URL>"
	}
	for _, u := range []string{api, e.P.FBRCURL} {
		s += fmt.Sprintf("  Match:  %s/*\n  Map To: protocol=http, host=127.0.0.1, port=%d\n", u, port)
	}
	s += "  Leave Path and Query empty; disable Preserve Host Header.\n"
	s += "  The app is NOT modified: no override is written and no adb reverse is installed.\n"
	s += fmt.Sprintf("  FBRC overrides are served over HTTP, so they need the %s rule.\n", e.P.FBRCURL)
	return s
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

// flowRun is one flow's in-progress result, shared by isolated and batched execution.
type flowRun struct {
	f       *fixtures.Flow
	res     *Result
	start   time.Time
	profile string
	held    bool // finishFlow leaves publishing to the caller (batches attach artifacts first)
}

func (r *Run) beginFlow(f *fixtures.Flow) *flowRun {
	e := r.e
	fr := &flowRun{f: f, start: time.Now(), profile: e.Opts.ProfileArg,
		res: &Result{Platform: e.Opts.Platform, Feature: f.Feature, File: f.File, Path: f.Path, Worker: r.Worker, Device: r.Device}}
	if fr.profile == "" {
		fr.profile = f.Header.Profile
	}
	logDir := filepath.Join(r.Dir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	fr.res.LogPath = filepath.Join(logDir, FlowSlug(f.Feature, f.File)+".log")
	e.emit(Event{Kind: EvFlowStart, Worker: r.Worker, RunID: r.ID, Flow: f.Rel, Device: r.Device, Text: fr.profile})
	r.stage("FLOW", f.Rel)
	return fr
}

func (r *Run) finishFlow(fr *flowRun, stage string, exit int) *Result {
	fr.res.Stage, fr.res.Exit, fr.res.Duration = stage, exit, time.Since(fr.start)
	if !fr.held {
		r.publishFlow(fr)
	}
	return fr.res
}

func (r *Run) publishFlow(fr *flowRun) {
	r.e.emit(Event{Kind: EvFlowResult, Worker: r.Worker, RunID: r.ID, Flow: fr.f.Rel, Result: fr.res})
}

// activateFlow loads the flow's scenario; on failure the flow is finished as SETUP.
func (r *Run) activateFlow(ctx context.Context, fr *flowRun) error {
	err := r.Activate(ctx, fr.profile, r.e.FBRCFor(fr.f.Header), fr.f.Header.Overrides)
	if err != nil {
		fr.res.Message = err.Error()
		r.warnf("%v", err)
	}
	return err
}

// RunFlow executes one flow in its own Maestro process and always returns exactly one result.
func (r *Run) RunFlow(ctx context.Context, f *fixtures.Flow) *Result {
	e := r.e
	fr := r.beginFlow(f)
	res := fr.res
	if r.activateFlow(ctx, fr) != nil {
		return r.finishFlow(fr, "SETUP", 1)
	}
	stage := "TEST"
	recording := ""
	if e.Opts.Record {
		stage = "RECORD"
		res.Recording = r.recordingPath(f)
		_ = os.MkdirAll(filepath.Dir(res.Recording), 0755)
		recording = res.Recording
		if e.Opts.Optimize {
			recording = rawPath(recording)
		}
		r.logf("Recording %s/%s [%s]", f.Feature, f.File, fr.profile)
	} else {
		r.logf("Running %s/%s [%s]", f.Feature, f.File, fr.profile)
	}
	out := r.maestro().Run(ctx, maestro.Flow{Source: f, Variables: e.flowVariables(f, r.Port), LogPath: res.LogPath}, recording)
	res.Artifacts, res.Shots = out.Artifacts.Dir, out.Artifacts.Shots
	exit := out.Exit
	if out.Setup {
		res.Message = out.Err.Error()
		return r.finishFlow(fr, "SETUP", exit)
	}
	runErr := out.Err
	if runErr != nil {
		res.Message = runErr.Error()
	}

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
		return r.finishFlow(fr, "CANCELLED", exit)
	}
	if runErr != nil && exit < 0 {
		res.Message = runErr.Error()
		exit = 1
	}
	return r.concludeFlow(ctx, fr, stage, exit)
}

// concludeFlow runs the post-flow checks (acceptance, API verifications, fallback report)
// against Maestro's outcome and finishes the flow.
func (r *Run) concludeFlow(ctx context.Context, fr *flowRun, stage string, exit int) *Result {
	res, f := fr.res, fr.f
	if !r.acceptedOverride(ctx) {
		// A failed Maestro run may never have launched the app (e.g. a flow parse error).
		if exit != 0 {
			res.Message = fmt.Sprintf("Maestro failed and the app never accepted run %s on %s (flow error, or a stale build without the E2E seam?)", r.ID, r.Device)
			r.warnf("%s", res.Message)
			return r.finishFlow(fr, stage, exit)
		}
		res.Message = fmt.Sprintf("App never accepted run %s on %s: no matching acceptance signal (stale build without the E2E seam?)", r.ID, r.Device)
		r.warnf("%s", res.Message)
		return r.finishFlow(fr, "OVERRIDE", 1)
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
	// VERIFY means the UI passed but the API counts did not; a UI failure keeps its own stage.
	if !verifyOK && exit == 0 {
		stage, exit = "VERIFY", 1
	}
	if exit == 0 {
		stage = "PASS"
	}
	return r.finishFlow(fr, stage, exit)
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
	var messages []wiremock.LoggedMessage
	if r.WM.MessageSupport {
		messages, err = r.WM.MessageJournal(true)
		if err != nil {
			return nil, err
		}
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
		for _, message := range messages {
			if message.EventType == "received" && message.StubName() == v.Stub {
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
	if r.WM.MessageSupport {
		messages, err := r.WM.MessageJournal(true)
		if err != nil {
			r.warnf("%v", err)
		} else {
			for _, m := range messages {
				if m.EventType == "received" && !m.WasMatched {
					unmatched = append(unmatched, "WS "+m.Channel.InitiatingRequest.URL+" "+m.Message.Body)
				}
			}
		}
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

// FlowSlug names a flow's log, debug dir and recording: <feature>-<file without .yaml>.
func FlowSlug(feature, file string) string { return safeName(feature) + "-" + safeName(trimExt(file)) }

// rawPath is where a recording is written before HandBrake optimises it.
func rawPath(recording string) string { return strings.TrimSuffix(recording, ".mp4") + ".raw.mp4" }

// flowVariables resolves the agreed input layers without exporting them to builds.
func (e *Engine) flowVariables(f *fixtures.Flow, port int) map[string]string {
	values := map[string]string{}
	maps.Copy(values, e.P.Variables)
	maps.Copy(values, e.P.TargetVariables)
	maps.Copy(values, f.Header.Variables)
	maps.Copy(values, e.P.LocalVariables)
	maps.Copy(values, e.Opts.Variables)
	values["APP_ID"] = e.P.AppID
	values["WIREMOCK_PORT"] = strconv.Itoa(port)
	return values
}
