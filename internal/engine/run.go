package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/lock"
	"github.com/feernandooff/mockstro/internal/registry"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// Run owns one device, one port and one WireMock for its lifetime. A single-device session is
// one Run; a parallel session is several, one per worker.
type Run struct {
	e       *Engine
	Worker  int
	ID      string
	Dir     string
	Device  string
	Port    int
	Command string

	locks           *lock.Manager
	reg             *registry.Registry
	info            *registry.Run
	websocketActive bool
	WM              *wiremock.Server

	overrideAuto    bool
	overrideApplied bool
	reverseApplied  bool
	preflightDone   bool
	recordingAPI    bool
	runtimeDir      string
	configPath      string
	flowsRoot       string

	apkPath, apkSHA string
	out             io.Writer // worker log (parallel) or nil
}

func (e *Engine) newRun(command string, worker int) *Run {
	id := NewRunID()
	return &Run{
		e: e, Worker: worker, ID: id, Command: command,
		locks:        e.newLocks(id),
		reg:          e.registry(),
		overrideAuto: !e.Opts.MapRemote && (command == "run" || e.Opts.Attach),
		apkPath:      e.Opts.SessionAPK,
		apkSHA:       e.Opts.SessionAPKSHA,
	}
}

func (r *Run) logf(format string, args ...any) {
	r.e.emit(Event{Kind: EvLog, Level: "info", Worker: r.Worker, RunID: r.ID, Text: fmt.Sprintf(format, args...)})
}

func (r *Run) warnf(format string, args ...any) {
	r.e.emit(Event{Kind: EvLog, Level: "warn", Worker: r.Worker, RunID: r.ID, Text: fmt.Sprintf(format, args...)})
}

func (r *Run) stage(stage, flow string) {
	registry.Stage(r.Dir, stage, flow)
	r.e.emit(Event{Kind: EvStage, Worker: r.Worker, RunID: r.ID, Stage: stage, Flow: flow, Device: r.Device, Port: r.Port})
}

// Setup: device, port, run dir, app, WireMock, port forward. Cleanup must run even on error.
func (r *Run) Setup(ctx context.Context, needDevice bool) error {
	e := r.e
	if needDevice {
		r.stage("DEVICE", "")
		serial, err := e.SelectDevice(ctx, r.locks)
		if err != nil {
			return err
		}
		r.Device = serial
	}
	port, err := e.AcquirePort(ctx, r.locks, r.Command, r.Device)
	if err != nil {
		return err
	}
	r.Port = port
	if r.Device != "" && !r.locks.Owns("device", r.Device) {
		return fmt.Errorf("device ownership was lost for %s", r.Device)
	}
	if err := r.initDir(); err != nil {
		return err
	}
	r.e.emit(Event{Kind: EvWorker, Worker: r.Worker, RunID: r.ID, Device: r.Device, Port: r.Port, Text: r.Dir})
	if r.Device != "" && r.overrideAuto {
		// Checked before the build: a leftover proxy would otherwise fail every flow minutes later.
		if err := r.checkProxy(ctx); err != nil {
			return err
		}
	}
	if r.Device != "" && e.Opts.Keyboard != "" {
		if err := e.Plat.SetKeyboard(ctx, r.Device, e.Opts.Keyboard == "shown"); err != nil {
			r.warnf("%v", err)
		} else {
			r.logf("On-screen keyboard %s on %s.", e.Opts.Keyboard, r.Device)
		}
	}
	if r.Device != "" {
		if err := r.ensureApp(ctx); err != nil {
			return err
		}
	}
	r.stage("WIREMOCK", "")
	if err := r.startWiremock(ctx); err != nil {
		return err
	}
	if r.Device != "" && r.overrideAuto {
		if err := e.Plat.AttachPort(ctx, r.Device, r.Port); err != nil {
			return err
		}
		r.reverseApplied = e.Opts.Platform != "ios"
	}
	return nil
}

func (r *Run) initDir() error {
	e := r.e
	r.Dir = filepath.Join(e.P.RunsRoot(), r.ID)
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "platform"), []byte(e.Opts.Platform), 0644)
	r.flowsRoot = filepath.Join(r.Dir, "maestro-flows")
	r.configPath = e.P.ConfigPath()
	// Maestro writes artefacts to testOutputDir; give each run a private copy. Paths are
	// absolute because Maestro resolves them relative to the config file.
	if b, err := os.ReadFile(e.P.ConfigPath()); err == nil {
		cfg := map[string]any{}
		if yaml.Unmarshal(b, &cfg) == nil {
			cfg["testOutputDir"] = filepath.Join(r.Dir, "results")
			cfg["flows"] = []string{filepath.Join(e.P.FlowsRoot(), "**")}
			if out, err := yaml.Marshal(cfg); err == nil {
				p := filepath.Join(r.Dir, "config.yaml")
				if os.WriteFile(p, out, 0o644) == nil {
					r.configPath = p
				}
			}
		}
	}
	username := os.Getenv("USER")
	if u, err := user.Current(); err == nil && username == "" {
		username = u.Username
	}
	r.info = &registry.Run{
		SchemaVersion: 1, RunID: r.ID, Owner: e.Owner, OwnerLabel: os.Getenv("MAESTRO_OWNER_LABEL"),
		User: username, PID: os.Getpid(), PIDStartedAt: lock.StartTime(context.Background(), e.Exec, os.Getpid()),
		Device: r.Device, Port: r.Port, Platform: e.Opts.Platform, Command: r.Command,
		Tags: strings.Join(e.Opts.Tags, ","), StartedAt: time.Now().UTC().Format(time.RFC3339),
		RunDir: r.Dir, Worktree: e.P.Root, Runner: "mockstro",
	}
	if err := r.reg.Save(r.info); err != nil {
		return err
	}
	r.stage("INIT", "")
	return nil
}

func (r *Run) progress(s string) { r.logf("%s", s) }

// checkProxy refuses a device with a device-wide proxy, or clears it (and verifies) when asked.
func (r *Run) checkProxy(ctx context.Context) error {
	e := r.e
	proxy := e.Plat.DeviceProxy(ctx, r.Device)
	if proxy == "" {
		return nil
	}
	if !e.Opts.ClearProxy {
		return e.Plat.AssertNoProxy(ctx, r.Device)
	}
	r.warnf("Clearing device-wide proxy on %s: %s", r.Device, proxy)
	if err := e.Plat.ClearProxy(ctx, r.Device); err != nil {
		return err
	}
	r.logf("Proxy cleared on %s (verified).", r.Device)
	return nil
}

// ensureApp builds (or reuses the parent's build), records provenance and installs.
func (r *Run) ensureApp(ctx context.Context) error {
	e := r.e
	if e.Opts.SkipBuild {
		if err := e.Plat.EnsureBooted(ctx, r.Device, e.Opts.Headless, r.progress); err != nil {
			return err
		}
		apk, err := e.Plat.InstalledProvenance(ctx, r.Device)
		if err != nil {
			return err
		}
		r.logf("Using the build already on %s: %s versionName=%s versionCode=%s installed=%s",
			r.Device, e.P.AppID, dash(apk.VersionName), dash(apk.VersionCode), dash(apk.InstalledAt))
		r.warnf("WARNING: --skip-build; nothing was compiled, so these results describe that build.")
		r.info.APK = apk
		_ = r.reg.Save(r.info)
		return e.Plat.AfterInstall(ctx, r.Device, r.overrideAuto)
	}
	if r.apkPath != "" {
		if !e.Plat.ArtifactExists(r.apkPath) {
			return fmt.Errorf("parent-built app is missing: %s", r.apkPath)
		}
		sum, _ := e.Plat.ArtifactSHA(r.apkPath)
		if sum != r.apkSHA {
			return fmt.Errorf("parent-built APK changed: expected %s, got %s", r.apkSHA, sum)
		}
	} else {
		r.stage("BUILD", "")
		path, sha, err := e.Plat.Build(ctx, r.Dir, e.outputWriter(r.Worker, r.ID))
		if err != nil {
			return err
		}
		r.apkPath, r.apkSHA = path, sha
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "apk.sha256"), []byte(r.apkSHA+"  "+r.apkPath+"\n"), 0o644)
	r.info.APK = &registry.APK{Path: r.apkPath, SHA256: r.apkSHA}
	_ = r.reg.Save(r.info)
	if err := e.Plat.EnsureBooted(ctx, r.Device, e.Opts.Headless, r.progress); err != nil {
		return err
	}
	if sum, _ := e.Plat.ArtifactSHA(r.apkPath); sum != r.apkSHA {
		return fmt.Errorf("session APK changed before installation")
	}
	r.stage("INSTALL", "")
	r.logf("Installing %s on %s (SHA-256 %s)...", filepath.Base(r.apkPath), r.Device, short(r.apkSHA))
	if err := e.Plat.Install(ctx, r.Device, r.apkPath); err != nil {
		return err
	}
	return e.Plat.AfterInstall(ctx, r.Device, r.overrideAuto)
}

func (r *Run) startWiremock(ctx context.Context) error {
	e := r.e
	jar, err := e.EnsureJar(ctx)
	if err != nil {
		return err
	}
	r.runtimeDir = filepath.Join(r.Dir, "wiremock-runtime")
	os.RemoveAll(r.runtimeDir)
	if err := os.MkdirAll(r.runtimeDir, 0o755); err != nil {
		return err
	}
	if err := os.Symlink(e.P.BodiesRoot(), filepath.Join(r.runtimeDir, "__files")); err != nil {
		return err
	}
	wm, err := wiremock.Start(ctx, e.Exec, jar, r.runtimeDir, r.Port, r.ID, filepath.Join(r.Dir, "wiremock.log"))
	if err != nil {
		return err
	}
	r.WM = wm
	return nil
}

// Cleanup only ever touches state this run owns.
func (r *Run) Cleanup() {
	e := r.e
	ctx, cancel := cleanupCtx()
	defer cancel()
	if r.Dir != "" {
		registry.Stage(r.Dir, "CLEANUP", "")
	}
	if r.Device != "" && r.locks.Owns("device", r.Device) {
		owned := true
		if r.overrideApplied {
			var msg string
			owned, msg = e.Plat.ResetApp(ctx, r.Device, r.ID)
			if msg != "" {
				r.warnf("%s", msg)
			}
			r.overrideApplied = false
		}
		if r.reverseApplied && owned {
			_ = e.Plat.DetachPort(ctx, r.Device, r.Port)
		}
		r.reverseApplied = false
	}
	if r.recordingAPI && r.WM != nil {
		_ = r.WM.StopRecording()
		r.recordingAPI = false
	}
	if r.WM != nil {
		r.WM.Stop()
		r.WM = nil
	}
	if r.runtimeDir != "" {
		os.RemoveAll(r.runtimeDir)
	}
	// The APK snapshot is ~70MB per run; once installed, the recorded SHA-256 is the provenance.
	if r.Dir != "" && r.apkPath != "" && strings.HasPrefix(r.apkPath, r.Dir+string(os.PathSeparator)) {
		stripArtifacts(r.Dir)
	}
	r.reg.Remove(r.ID)
	r.locks.ReleaseAll()
	if r.Dir != "" {
		registry.Stage(r.Dir, "DONE", "")
	}
}

func dash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// EnsureJar resolves the pinned WireMock jar once per engine.
func (e *Engine) EnsureJar(ctx context.Context) (string, error) {
	if e.jar != "" {
		return e.jar, nil
	}
	jar, err := wiremock.EnsureJarVersion(ctx, e.P.WireMockVersion, filepath.Join(e.P.RuntimeRoot(), "wiremock", "wiremock-standalone-"+e.P.WireMockVersion+".jar"), e.Logf)
	if err != nil {
		return "", err
	}
	e.jar = jar
	return jar, nil
}

// outputWriter turns a child process's output into EvOutput events, line by line.
func (e *Engine) outputWriter(worker int, runID string) io.Writer {
	return &lineWriter{emit: func(line string) {
		e.emit(Event{Kind: EvOutput, Worker: worker, RunID: runID, Text: line})
	}}
}

type lineWriter struct {
	emit func(string)
	buf  []byte
	tee  io.Writer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w.tee != nil {
		_, _ = w.tee.Write(p)
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		w.emit(stripCR(line))
	}
	return len(p), nil
}

func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(stripCR(string(w.buf)))
		w.buf = nil
	}
}

// stripCR keeps the last carriage-return segment (progress bars redraw with \r).
func stripCR(s string) string {
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		return s[i+1:]
	}
	return s
}
