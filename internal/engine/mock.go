package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// ResolveMockTarget interprets `mock [scenario|profile|flow [stub...]]`.
func (e *Engine) ResolveMockTarget(args []string) (*mockruntime.Target, error) {
	t := &mockruntime.Target{Kind: "profile"}
	var flowOverrides []string
	var defaults map[string]string
	if len(args) > 0 {
		first := args[0]
		switch {
		case strings.HasSuffix(first, ".yaml") || strings.HasSuffix(first, ".yml"):
			f, err := e.Store.Flow(first)
			if err != nil {
				return nil, err
			}
			if err := e.Store.ValidateFlow(f); err != nil {
				return nil, err
			}
			t.Kind, t.Name, t.Description, t.Profile = "flow", e.P.Rel(f.Path), f.Header.Description, f.Header.Profile
			defaults = f.Header.FBRC
			flowOverrides = f.Header.Overrides
		default:
			sc, err := e.Store.FindScenario(first)
			if err != nil {
				return nil, err
			}
			if sc != nil {
				if err := e.Store.ValidateScenario(sc); err != nil {
					return nil, err
				}
				t.Kind, t.Name, t.Description, t.Profile = "scenario", sc.Name, sc.Header.Description, sc.Header.Profile
				defaults = sc.Header.FBRC
				flowOverrides = sc.Header.Overrides
			} else {
				if e.Opts.ProfileArg != "" {
					return nil, fmt.Errorf("mock got both a positional profile ('%s') and --profile '%s'; pass one", first, e.Opts.ProfileArg)
				}
				if first == "shared" || !e.Store.ProfileExists(first) {
					return nil, e.unknownProfile(first)
				}
				t.Name, t.Profile = first, first
			}
		}
	}
	if e.Opts.ProfileArg != "" {
		if !e.Store.ProfileExists(e.Opts.ProfileArg) {
			return nil, e.unknownProfile(e.Opts.ProfileArg)
		}
		t.Profile = e.Opts.ProfileArg
	}
	var cli []string
	if len(args) > 1 {
		cli = args[1:]
	}
	for _, o := range cli {
		if !e.Store.StubExists(o) {
			return nil, fmt.Errorf("unknown stub: %s (see `mockstro list profiles`)", o)
		}
	}
	merged, err := e.Store.MergeOverridesByRoute(append(append([]string(nil), flowOverrides...), cli...))
	if err != nil {
		return nil, err
	}
	if err := e.Store.ValidateLayerDuplicates("manual mock overrides", merged); err != nil {
		return nil, err
	}
	t.Overrides = merged
	t.FBRC, err = remoteconfig.Validate(remoteconfig.Merge(defaults, e.Opts.FBRC))
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (e *Engine) unknownProfile(name string) error {
	profiles, _ := e.Store.ProfileNames()
	scs, _ := e.Store.Scenarios()
	var names []string
	for _, s := range scs {
		names = append(names, s.Name)
	}
	return fmt.Errorf("unknown profile or scenario: %s\nAvailable profiles: %s\nAvailable scenarios: %s", name, strings.Join(profiles, ", "), strings.Join(names, ", "))
}

// StartMock starts server-only mock on 18080, or attaches the explicitly selected device.
func (e *Engine) StartMock(ctx context.Context, t *mockruntime.Target) (*mockruntime.Session, error) {
	clean, err := remoteconfig.Validate(t.FBRC)
	if err != nil {
		return nil, err
	}
	target := *t
	target.FBRC = clean
	target.Overrides = append([]string(nil), t.Overrides...)
	t = &target
	run := e.newRun("mock", 0)
	if err := run.Setup(ctx, e.Opts.Attach); err != nil {
		run.Cleanup()
		return nil, err
	}
	if err := run.Activate(ctx, t.Profile, t.FBRC, t.Overrides); err != nil {
		run.Cleanup()
		return nil, err
	}
	run.stage("MOCK", t.Name)
	return mockruntime.New(newMockRuntime(run), t, run.Port, run.Device), nil
}

func (e *Engine) MockBanner(m *mockruntime.Session) string {
	var b strings.Builder
	port := m.Snapshot().Port
	fmt.Fprintf(&b, "WireMock API: http://127.0.0.1:%d/api/\n", port)
	if e.Opts.Attach {
		fmt.Fprintf(&b, "FBRC entries: %d (omitted keys use app defaults)\n", len(m.Target().FBRC))
	} else {
		fmt.Fprintf(&b, "Server-only: no device changes; FBRC served over HTTP via Map Remote (%d entries).\n", len(m.Target().FBRC))
		b.WriteString(e.ManualProxyInstructions(port, ""))
	}
	fmt.Fprintf(&b, "WireMock admin: http://127.0.0.1:%d/__admin/\n", port)
	t := m.Target()
	if t.Kind == "flow" {
		fmt.Fprintf(&b, "Flow: %s\n", t.Name)
	}
	if t.Kind == "scenario" {
		fmt.Fprintf(&b, "Scenario: %s — %s\n", t.Name, t.Description)
	}
	shared, _ := e.Store.ProfileMappings("shared")
	profile := m.Target().Profile
	if profile == "" {
		profile = "none"
	}
	fmt.Fprintf(&b, "Shared defaults: %d; profile: %s; overrides: %d\n", len(shared), profile, len(m.Target().Overrides))
	fb := e.P.FallbackURL
	if fb == "" {
		fb = "diagnostic HTTP 599"
	}
	fmt.Fprintf(&b, "Fallback: %s (manual serving does not assert requests)", fb)
	return b.String()
}

var recordingName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// RecordAPI proxies to target and persists matching paths. Recordings may contain PII.
func (e *Engine) RecordAPI(ctx context.Context, target, name, pathPattern string) error {
	if !regexp.MustCompile(`^https?://\S+$`).MatchString(target) {
		return fmt.Errorf("--target must be an http(s) URL")
	}
	if name == "" {
		name = time.Now().Format("20060102-150405")
	} else if !recordingName.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("--output must be a directory name without slashes or '..'")
	}
	dir := filepath.Join(e.P.RecordingsRoot(), name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("recording output already exists: %s", e.P.Rel(dir))
	}
	for _, d := range []string{"mappings", "__files"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return err
		}
	}
	run, err := e.serveRoot(ctx, "record-api", dir)
	if err != nil {
		return err
	}
	defer run.Cleanup()
	if err := run.WM.StartRecording(target, pathPattern); err != nil {
		return fmt.Errorf("failed to start native WireMock recording; see %s: %w", e.P.Rel(run.WM.Log), err)
	}
	run.recordingAPI = true
	e.Warnf("WARNING: API recordings may contain credentials, tokens, account data, and other PII.")
	e.Logf("Recording %s from http://127.0.0.1:%d to %s", pathPattern, run.Port, e.P.Rel(dir))
	e.Logf("Upstream: %s\nPress Ctrl-C to stop and persist the recording.", target)
	select {
	case <-ctx.Done():
	case <-run.WM.Done():
	}
	e.Logf("API recording stopped: %s", e.P.Rel(dir))
	return nil
}

// Playback serves a native WireMock recording directory.
func (e *Engine) Playback(ctx context.Context, dir string) error {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.P.Root, dir)
	}
	for _, d := range []string{"", "mappings", "__files"} {
		if st, err := os.Stat(filepath.Join(dir, d)); err != nil || !st.IsDir() {
			return fmt.Errorf("playback directory is missing %s: %s", strings.TrimSuffix(d+"/", "/"), dir)
		}
	}
	run, err := e.serveRoot(ctx, "playback", dir)
	if err != nil {
		return err
	}
	defer run.Cleanup()
	e.Logf("Playing WireMock recording from %s", e.P.Rel(dir))
	e.Logf("API endpoint: http://127.0.0.1:%d/api/\nWireMock admin: http://127.0.0.1:%d/__admin/\nPress Ctrl-C to stop WireMock.", run.Port, run.Port)
	select {
	case <-ctx.Done():
	case <-run.WM.Done():
	}
	return nil
}

// serveRoot takes a port lock and starts WireMock directly on root (no stub loading).
func (e *Engine) serveRoot(ctx context.Context, command, root string) (*Run, error) {
	run := e.newRun(command, 0)
	e.Opts.PortPinned = true
	port, err := e.AcquirePort(ctx, run.locks, command, "")
	if err != nil {
		return nil, err
	}
	run.Port = port
	if err := run.initDir(); err != nil {
		run.Cleanup()
		return nil, err
	}
	jar, err := e.EnsureJar(ctx)
	if err != nil {
		run.Cleanup()
		return nil, err
	}
	var javaArgs []string
	if command == "record-api" {
		// The app can reach us through a system proxy's Map Remote rule. Sending
		// our upstream requests through that same proxy would loop back here.
		javaArgs = []string{"-Djava.net.useSystemProxies=false", "-Dhttp.proxyHost=", "-Dhttp.proxyPort=", "-Dhttps.proxyHost=", "-Dhttps.proxyPort="}
	}
	wm, err := wiremock.Start(ctx, e.Exec, jar, root, port, run.ID, filepath.Join(run.Dir, "wiremock.log"), javaArgs...)
	if err != nil {
		run.Cleanup()
		return nil, err
	}
	run.WM = wm
	return run, nil
}
