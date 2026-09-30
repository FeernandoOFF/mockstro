package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/registry"
)

func testProject(t *testing.T) *project.Project {
	t.Helper()
	t.Setenv("MOCKSTRO_STATE_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAESTRO_AGENT_AVDS", "")
	t.Setenv("MAESTRO_DEVICE_CLASS", "")
	root := t.TempDir()
	return &project.Project{
		Root: root, MaestroRoot: filepath.Join(root, ".maestro"), Env: map[string]string{},
		Platform: "android", AppID: "ie.example.envtest", GradleTask: "assembleDebugEnvTest",
		APKGlob:     "app/build/outputs/apk/debugEnvTest/*.apk",
		OverrideKey: project.DefaultOverrideKy, AcceptKey: project.DefaultAcceptKey,
	}
}

func testEngine(t *testing.T, owner string, h func(c execx.Cmd) (string, error)) (*Engine, *execx.Fake) {
	p := testProject(t)
	fake := &execx.Fake{Paths: map[string]string{"lsof": "/usr/sbin/lsof"}, Handler: func(c execx.Cmd) (string, error) {
		if c.Name == "ps" {
			return "Mon Sep 22 10:00:00 2026", nil
		}
		return h(c)
	}}
	o := DefaultOptions()
	o.Owner = owner
	e := New(p, o, fake, nil)
	return e, fake
}

func adbDevices(c execx.Cmd, avds map[string]string) (string, bool) {
	args := strings.Join(c.Args, " ")
	if c.Name != "adb" {
		return "", false
	}
	if args == "devices" {
		out := "List of devices attached\n"
		for _, s := range slices.Sorted(maps.Keys(avds)) {
			out += s + "\tdevice\n"
		}
		return out, true
	}
	for serial, name := range avds {
		if args == "-s "+serial+" shell getprop ro.boot.qemu.avd_name" {
			return name, true
		}
	}
	return "", false
}

func TestDesignation(t *testing.T) {
	avds := map[string]string{"emulator-5554": "Agent_AVD", "emulator-5556": "Personal_AVD", "phone": ""}
	h := func(c execx.Cmd) (string, error) {
		if out, ok := adbDevices(c, avds); ok {
			return out, nil
		}
		return "", errors.New("unexpected " + c.String())
	}
	ctx := context.Background()

	e, _ := testEngine(t, "agent", h)
	if err := e.requireDesignation(); err == nil {
		t.Fatal("agent run without designation accepted")
	}
	e.P.Env["MAESTRO_AGENT_AVDS"] = "Agent_AVD"
	if !e.Allowed(ctx, "emulator-5554") || e.Allowed(ctx, "emulator-5556") || e.Allowed(ctx, "phone") {
		t.Fatal("agent designation rules")
	}
	e.Opts.DeviceArg = "emulator-5556"
	if _, err := e.SelectDevice(ctx, e.newLocks("r1")); err == nil {
		t.Fatal("agent took a non-designated emulator")
	}
	e.Opts.DeviceArg = ""
	serial, err := e.SelectDevice(ctx, e.newLocks("r2"))
	if err != nil || serial != "emulator-5554" {
		t.Fatalf("select: %q %v", serial, err)
	}
	// The designated one is now locked; the agent must not fall back to the personal AVD.
	if _, err := e.SelectDevice(ctx, e.newLocks("r3")); err == nil {
		t.Fatal("agent fell back to a non-designated emulator")
	}

	hu, _ := testEngine(t, "human", h)
	if !hu.Allowed(ctx, "emulator-5556") || hu.Allowed(ctx, "phone") {
		t.Fatal("human rules: any emulator, never a phone without --map-remote")
	}
	hu.Opts.MapRemote = true
	if !hu.Allowed(ctx, "phone") {
		t.Fatal("--map-remote must allow a physical device")
	}
}

func TestCleanupOnlyTouchesOwnOverride(t *testing.T) {
	var mu sync.Mutex
	var current string
	var calls []string
	h := func(c execx.Cmd) (string, error) {
		args := strings.Join(c.Args, " ")
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, args)
		if strings.HasSuffix(args, "settings get global "+project.DefaultOverrideKy) {
			return current, nil
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	payload := func(run string) string {
		b, _ := json.Marshal(map[string]string{"runId": run, "applicationId": e.P.AppID})
		return base64.StdEncoding.EncodeToString(b)
	}
	a := e.Plat.(*platform.Android)
	ctx := context.Background()

	current = payload("theirs")
	if owned, _ := a.ResetApp(ctx, "emulator-fake", "ours"); owned {
		t.Fatal("reset someone else's override")
	}
	if len(calls) != 1 {
		t.Fatalf("touched the device for a foreign override: %v", calls)
	}
	calls = nil
	current = payload("ours")
	if owned, _ := a.ResetApp(ctx, "emulator-fake", "ours"); !owned {
		t.Fatal("did not reset our own override")
	}
	want := []string{
		"-s emulator-fake shell settings get global " + project.DefaultOverrideKy,
		"-s emulator-fake shell am force-stop " + e.P.AppID,
		"-s emulator-fake shell settings delete global " + project.DefaultOverrideKy,
		"-s emulator-fake shell settings delete global " + project.DefaultAcceptKey,
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestOverrideAcceptance(t *testing.T) {
	var calls []string
	accepted := ""
	h := func(c execx.Cmd) (string, error) {
		args := strings.Join(c.Args, " ")
		calls = append(calls, args)
		if strings.HasSuffix(args, "settings get global "+project.DefaultAcceptKey) {
			return accepted, nil
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	r := e.newRun("run", 0)
	r.Device = "emulator-fake"
	r.Port = 18081
	ctx := context.Background()
	if err := e.Plat.WriteOverride(ctx, r.Device, r.override(nil)); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "-s emulator-fake shell settings delete global "+project.DefaultAcceptKey {
		t.Fatalf("stale acceptance not cleared first: %v", calls)
	}
	put := calls[1]
	enc := put[strings.LastIndex(put, " ")+1:]
	raw, _ := base64.StdEncoding.DecodeString(enc)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["schema"] != "1" || m["applicationId"] != e.P.AppID || m["runId"] != r.ID || m["apiUrl"] != "http://127.0.0.1:18081/api" {
		t.Fatalf("payload %v", m)
	}
	if r.acceptedOverride(ctx) {
		t.Fatal("accepted without a signal")
	}
	accepted = "theirs"
	if r.acceptedOverride(ctx) {
		t.Fatal("accepted another run's signal")
	}
	accepted = r.ID
	if !r.acceptedOverride(ctx) {
		t.Fatal("rejected our own signal")
	}
	r.overrideAuto = false
	accepted = "theirs"
	if !r.acceptedOverride(ctx) {
		t.Fatal("map-remote runs have no acceptance signal to check")
	}
}

func TestBuildProvenanceAndSessionAPK(t *testing.T) {
	var build string
	installs := 0
	var e *Engine
	h := func(c execx.Cmd) (string, error) {
		switch {
		case c.Name == "./gradlew":
			dir := filepath.Join(e.P.Root, "app/build/outputs/apk/debugEnvTest")
			os.MkdirAll(dir, 0o755)
			return "", os.WriteFile(filepath.Join(dir, "app-debugEnvTest.apk"), []byte(build), 0o644)
		case c.Name == "adb" && strings.Contains(strings.Join(c.Args, " "), "install -r -t"):
			installs++
			return "Success", nil
		case c.Name == "adb":
			return "", nil
		}
		return "", errors.New("unexpected " + c.String())
	}
	e, _ = testEngine(t, "agent", h)
	ctx := context.Background()
	var prev string
	for _, v := range []string{"A", "B", "A"} {
		build = v
		r := e.newRun("run", 0)
		r.Device = "emulator-fake"
		if err := r.initDir(); err != nil {
			t.Fatal(err)
		}
		if err := r.ensureApp(ctx); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(r.apkPath)
		if string(b) != v || r.apkSHA == prev {
			t.Fatalf("stale artifact for build %s", v)
		}
		prev = r.apkSHA
		if r.info.APK == nil || r.info.APK.SHA256 != r.apkSHA {
			t.Fatal("provenance not recorded")
		}
		r.Cleanup()
	}
	if installs != 3 {
		t.Fatalf("installs = %d", installs)
	}

	// A worker re-hashes the parent's artifact and refuses a changed one.
	r := e.newRun("run", 1)
	r.Device = "emulator-fake"
	_ = r.initDir()
	r.apkPath, r.apkSHA = filepath.Join(r.Dir, "parent.apk"), "deadbeef"
	os.WriteFile(r.apkPath, []byte("tampered"), 0o644)
	if err := r.ensureApp(ctx); err == nil || !strings.Contains(err.Error(), "parent-built APK changed") {
		t.Fatalf("tampered artifact accepted: %v", err)
	}
	r.Cleanup()
}

func TestSkipBuildRecordsInstalledBuild(t *testing.T) {
	dump := ""
	var calls []string
	h := func(c execx.Cmd) (string, error) {
		args := strings.Join(c.Args, " ")
		calls = append(calls, args)
		if strings.Contains(args, "dumpsys package") {
			return dump, nil
		}
		if c.Name == "./gradlew" || strings.Contains(args, "install") && !strings.Contains(args, "pm grant") {
			t.Fatalf("--skip-build built or installed: %s", c)
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	e.Opts.SkipBuild = true
	r := e.newRun("run", 0)
	r.Device = "emulator-fake"
	_ = r.initDir()
	defer r.Cleanup()
	if err := r.ensureApp(context.Background()); err == nil || !strings.Contains(err.Error(), "is not installed") {
		t.Fatalf("missing package accepted: %v", err)
	}
	dump = "    versionName=4.50\n    versionCode=4050003 minSdk=26\n    lastUpdateTime=2026-09-18 22:10:00"
	if err := r.ensureApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := r.info.APK; a.Source != "preinstalled" || a.VersionName != "4.50" || a.VersionCode != "4050003" {
		t.Fatalf("provenance %+v", a)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "pm grant "+e.P.AppID+" android.permission.WRITE_SECURE_SETTINGS") {
		t.Fatal("permission not granted")
	}
}

func TestEnsureParallelDevices(t *testing.T) {
	attached := map[string]string{"emulator-5554": "Agent_A"}
	var booted []string
	h := func(c execx.Cmd) (string, error) {
		if out, ok := adbDevices(c, attached); ok {
			return out, nil
		}
		if strings.HasSuffix(c.Name, "emulator") {
			return "Agent_A\nAgent_B\nPersonal", nil
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	e.P.Env["MAESTRO_AGENT_AVDS"] = "Agent_A,Agent_B"
	emu := filepath.Join(t.TempDir(), "emulator", "emulator")
	os.MkdirAll(filepath.Dir(emu), 0o755)
	os.WriteFile(emu, []byte("#!/bin/sh\n"), 0o755)
	e.P.Env["ANDROID_HOME"] = filepath.Dir(filepath.Dir(emu))
	e.Plat = &bootSpy{Platform: e.Plat, boot: func(name string) {
		booted = append(booted, name)
		attached["emulator-5556"] = "Agent_B"
	}}
	ctx := context.Background()
	if err := e.EnsureParallelDevices(ctx, 1); err != nil || len(booted) != 0 {
		t.Fatalf("booted with a device free: %v %v", err, booted)
	}
	if err := e.EnsureParallelDevices(ctx, 2); err != nil || strings.Join(booted, ",") != "Agent_B" {
		t.Fatalf("boot shortfall: %v %v", err, booted)
	}
	if err := e.EnsureParallelDevices(ctx, 3); err == nil || !strings.Contains(err.Error(), "needs 3 devices") {
		t.Fatalf("beyond designated list: %v", err)
	}
}

type bootSpy struct {
	platform.Platform
	boot func(string)
}

func (b *bootSpy) Boot(_ context.Context, name string, _ bool, _ func(string)) (string, error) {
	b.boot(name)
	return "", nil
}

func TestQueueReportsIncompleteWork(t *testing.T) {
	mk := func(n string) *fixtures.Flow { return &fixtures.Flow{Feature: "account", File: n} }
	q := &queue{flows: []*fixtures.Flow{mk("done.yaml"), mk("interrupted.yaml"), mk("unstarted.yaml")}, claimed: make([]bool, 3), results: make([]*Result, 3)}
	i, _, _ := q.claim()
	q.put(i, &Result{Feature: "account", File: "done.yaml", Stage: "PASS"})
	q.claim()
	q.halt()
	if _, _, ok := q.claim(); ok {
		t.Fatal("claimed after halt")
	}
	res := q.collect()
	got := []string{res[0].Stage, res[1].Stage, res[2].Stage}
	if strings.Join(got, ",") != "PASS,INTERRUPTED,UNSTARTED" {
		t.Fatalf("stages %v", got)
	}
	if s := NewSummary(res, 0, ""); s.Failed != 2 {
		t.Fatal("incomplete work must fail the session")
	}
}

func TestOptionRules(t *testing.T) {
	o := DefaultOptions()
	if err := o.Validate("mock"); err != nil || o.ServerPort != 18080 || !o.PortPinned {
		t.Fatalf("mock port: %v %+v", err, o)
	}
	o = DefaultOptions()
	o.ServerPort, o.PortPinned = 18081, true
	if o.Validate("mock") == nil {
		t.Fatal("mock accepted a non-reserved port")
	}
	o = DefaultOptions()
	o.DeviceArg = "emulator-5554"
	if o.Validate("mock") == nil {
		t.Fatal("mock --device without --attach accepted")
	}
	o = DefaultOptions()
	o.DeviceClass = "physical"
	if o.Validate("run") == nil {
		t.Fatal("physical without --map-remote accepted")
	}
	o = DefaultOptions()
	o.MapRemote, o.Parallel = true, "2"
	if o.Validate("run") == nil {
		t.Fatal("--map-remote with --parallel accepted")
	}
}

func TestEmulatorRunSkipsReservedPort(t *testing.T) {
	h := func(c execx.Cmd) (string, error) {
		if c.Name == "lsof" {
			return "", errors.New("no listener")
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	e.Opts.ServerPort = 18080
	port, err := e.AcquirePort(context.Background(), e.newLocks("p"), "run", "emulator-5554")
	if err != nil || port != 18081 {
		t.Fatalf("port %d %v", port, err)
	}
}

func TestJUnit(t *testing.T) {
	s := NewSummary([]*Result{
		{Feature: "a", File: "ok.yaml", Stage: "PASS"},
		{Feature: "a", File: "bad.yaml", Stage: "VERIFY", Exit: 1, Verifs: []VerificationResult{{Stub: "x", Expectation: "at least 1", Got: 0}}},
		{Feature: "a", File: "never.yaml", Stage: "UNSTARTED", Exit: 1},
	}, 0, "")
	out := string(JUnit(s, "/"))
	for _, want := range []string{`tests="3"`, `failures="1"`, `skipped="1"`, `type="VERIFY"`, "x: expected at least 1, got 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestPruneKeepsNewestAndStripsAPKs(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) { return "", nil })
	root := e.P.RunsRoot()
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		d := filepath.Join(root, fmt.Sprintf("run-%d", i))
		os.MkdirAll(filepath.Join(d, "artifacts"), 0o755)
		os.WriteFile(filepath.Join(d, "artifacts", "app.apk"), make([]byte, 1000), 0o644)
		os.WriteFile(filepath.Join(d, "result.tsv"), []byte("meta|x|1\n"), 0o644)
		ts := old.Add(time.Duration(i) * time.Minute)
		os.Chtimes(d, ts, ts)
	}
	// In progress: no result yet and fresh, so never pruned.
	os.MkdirAll(filepath.Join(root, "fresh"), 0o755)
	removed, freed := e.Prune(context.Background(), 2)
	if removed != 3 || freed < 5000 {
		t.Fatalf("removed %d freed %d", removed, freed)
	}
	for _, n := range []string{"run-4", "run-3", "fresh"} {
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			t.Fatalf("%s pruned", n)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "run-4", "artifacts")); err == nil {
		t.Fatal("kept run still has its APK snapshot")
	}
	if err := e.DeleteRunDir(context.Background(), filepath.Join(root, "fresh")); err == nil {
		t.Fatal("deleted an in-progress run dir")
	}
	if err := e.DeleteRunDir(context.Background(), e.P.Root); err == nil {
		t.Fatal("deleted something outside runs/")
	}
}

func TestProxyRefusedOrClearedBeforeBuild(t *testing.T) {
	settings := map[string]string{"http_proxy": "192.168.1.20:9090", "global_http_proxy_host": "192.168.1.20"}
	h := func(c execx.Cmd) (string, error) {
		a := c.Args
		if c.Name == "adb" && len(a) >= 6 && a[2] == "shell" && a[3] == "settings" {
			switch a[4] {
			case "get":
				if v, ok := settings[a[6]]; ok {
					return v, nil
				}
				return "null", nil
			case "put":
				settings[a[6]] = a[7]
			case "delete":
				delete(settings, a[6])
			}
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	r := e.newRun("run", 0)
	r.Device = "emulator-5554"
	ctx := context.Background()
	if err := r.checkProxy(ctx); err == nil || !strings.Contains(err.Error(), "device-wide proxy") {
		t.Fatalf("proxy not refused: %v", err)
	}
	e.Opts.ClearProxy = true
	if err := r.checkProxy(ctx); err != nil {
		t.Fatal(err)
	}
	if p := e.Plat.DeviceProxy(ctx, "emulator-5554"); p != "" {
		t.Fatalf("proxy left behind: %s", p)
	}
}

func TestDeadRunDirIsDeletable(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) { return "", nil })
	d := filepath.Join(e.P.RunsRoot(), "setup-failed")
	os.MkdirAll(d, 0o755)
	// Written by a process that no longer exists, with no result.tsv (setup failed).
	os.WriteFile(filepath.Join(d, "run.json"), []byte(`{"runId":"x","pid":999999,"pidStartedAt":"then"}`), 0o644)
	if err := e.DeleteRunDir(context.Background(), d); err != nil {
		t.Fatalf("dead run dir refused: %v", err)
	}
}

func TestNewArtifactsFindsThisFlowsScreenshots(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) { return "", nil })
	r := e.newRun("run", 0)
	r.Dir = t.TempDir()
	old := filepath.Join(r.Dir, "results", "2026-09-23_100000", "Earlier flow")
	os.MkdirAll(filepath.Join(old, "screenshots"), 0o755)
	before := r.resultDirs()
	cur := filepath.Join(r.Dir, "results", "2026-09-23_100100", "Account home")
	os.MkdirAll(filepath.Join(cur, "screenshots"), 0o755)
	for _, n := range []string{"step-009-assert.png", "step-003-runFlow.png"} {
		os.WriteFile(filepath.Join(cur, "screenshots", n), []byte("png"), 0o644)
	}
	dir, shots := r.newArtifacts(before)
	if dir != cur || len(shots) != 2 || !strings.HasSuffix(shots[1], "step-009-assert.png") {
		t.Fatalf("artifacts %q %v", dir, shots)
	}
	s := NewSummary([]*Result{{Feature: "a", File: "x.yaml", Stage: "TEST", Exit: 1, Artifacts: dir, Shots: shots}}, 0, "")
	if out := string(JUnit(s, "/")); !strings.Contains(out, "[[ATTACHMENT|"+shots[1]+"]]") {
		t.Fatalf("junit lacks attachment:\n%s", out)
	}
}

func TestSessionFoldsItsWorkerDirs(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) { return "", nil })
	root := e.P.RunsRoot()
	mk := func(n string) string {
		d := filepath.Join(root, n)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "result.tsv"), []byte("meta|x|1\n"), 0o644)
		return d
	}
	w1, w2, sess := mk("w1"), mk("w2"), mk("session-20260923T000000Z-1-abc")
	os.Symlink(w1, filepath.Join(sess, "worker-1"))
	os.Symlink(w2, filepath.Join(sess, "worker-2"))
	mk("single")
	dirs := e.RunDirs(context.Background(), true)
	if len(dirs) != 2 {
		t.Fatalf("workers not folded: %+v", dirs)
	}
	if err := e.DeleteRunDir(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{w1, w2, sess} {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("%s survived deleting its session", d)
		}
	}
}

func TestKeyboardAndroid(t *testing.T) {
	ids := map[string]string{
		"com.google.android.inputmethod.latin": "com.google.android.inputmethod.latin/com.android.inputmethod.latin.LatinIME",
		"com.google.android.tts":               "com.google.android.tts/.VoiceInputMethodService",
		"dev.mobile.maestro":                   "dev.mobile.maestro/.input.MaestroInputMethodService",
	}
	disabled := map[string]bool{}
	setting := "null"
	h := func(c execx.Cmd) (string, error) {
		a := strings.Join(c.Args, " ")
		last := c.Args[len(c.Args)-1]
		switch {
		case strings.Contains(a, "ime list -s"):
			var out []string
			for pkg, id := range ids {
				if !disabled[pkg] {
					out = append(out, id)
				}
			}
			return strings.Join(out, "\n"), nil
		case strings.Contains(a, "pm disable-user"):
			disabled[last] = true
		case strings.Contains(a, "pm enable"):
			disabled[last] = false
		case strings.Contains(a, "settings get global mockstro_disabled_ime_packages"):
			return setting, nil
		case strings.Contains(a, "settings put global mockstro_disabled_ime_packages"):
			setting = last
		case strings.Contains(a, "settings delete global mockstro_disabled_ime_packages"):
			setting = "null"
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	ctx := context.Background()
	if got := e.Plat.Keyboard(ctx, "emulator-5554"); got != "shown" {
		t.Fatalf("state %s", got)
	}
	if err := e.Plat.SetKeyboard(ctx, "emulator-5554", false); err != nil || e.Plat.Keyboard(ctx, "emulator-5554") != "hidden" {
		t.Fatalf("hide: %v %v", err, disabled)
	}
	if disabled["dev.mobile.maestro"] {
		t.Fatal("Maestro's own input method must stay enabled")
	}
	// Hiding again (packages already gone from the list) must keep the record.
	if err := e.Plat.SetKeyboard(ctx, "emulator-5554", false); err != nil || !strings.Contains(setting, "latin") {
		t.Fatalf("re-hide lost the record: %v %q", err, setting)
	}
	if err := e.Plat.SetKeyboard(ctx, "emulator-5554", true); err != nil || e.Plat.Keyboard(ctx, "emulator-5554") != "shown" {
		t.Fatalf("show: %v %v", err, disabled)
	}
}

func TestKeyboardIOSWritesPerSimulatorPreference(t *testing.T) {
	var calls []string
	value := "true"
	h := func(c execx.Cmd) (string, error) {
		a := strings.Join(c.Args, " ")
		calls = append(calls, c.Name+" "+a)
		switch {
		case c.Name == "plutil" && strings.HasPrefix(a, "-extract"):
			return value, nil
		case c.Name == "plutil" && strings.HasPrefix(a, "-replace"):
			value = map[string]string{"YES": "true", "NO": "false"}[c.Args[3]]
		case c.Name == "xcrun" && strings.Contains(a, "list devices"):
			return `{"devices":{"r":[{"udid":"UDID-1","name":"iPhone","state":"Booted","isAvailable":true}]}}`, nil
		}
		return "", nil
	}
	e, _ := testEngine(t, "agent", h)
	e.Plat = platform.New(e.P, "ios", e.Exec)
	ctx := context.Background()
	if e.Plat.Keyboard(ctx, "UDID-1") != "hidden" {
		t.Fatal("hardware keyboard connected means the on-screen one is hidden")
	}
	if err := e.Plat.SetKeyboard(ctx, "UDID-1", true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"-replace DevicePreferences.UDID-1.ConnectHardwareKeyboard -bool NO", "simctl shutdown UDID-1", "simctl boot UDID-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	if e.Plat.Keyboard(ctx, "UDID-1") != "shown" {
		t.Fatal("state not updated")
	}
}

func TestIOSBootAppliesKeyboardWithoutSecondBoot(t *testing.T) {
	var calls []string
	value, state := "true", "Shutdown"
	h := func(c execx.Cmd) (string, error) {
		a := strings.Join(c.Args, " ")
		calls = append(calls, c.Name+" "+a)
		switch {
		case c.Name == "plutil" && strings.HasPrefix(a, "-extract"):
			return value, nil
		case c.Name == "plutil" && strings.HasPrefix(a, "-replace"):
			value = map[string]string{"YES": "true", "NO": "false"}[c.Args[3]]
		case c.Name == "xcrun" && strings.Contains(a, "list devices"):
			return `{"devices":{"r":[{"udid":"UDID-1","name":"iPhone","state":"` + state + `","isAvailable":true}]}}`, nil
		case c.Name == "xcrun" && strings.Contains(a, "simctl boot"):
			state = "Booted"
		}
		return "", nil
	}
	p := testProject(t)
	p.Platform = "ios"
	fake := &execx.Fake{Handler: h}
	o := DefaultOptions()
	o.Platform, o.Keyboard = "ios", "shown"
	e := New(p, o, fake, nil)
	ctx := context.Background()
	if _, err := e.Plat.Boot(ctx, "iPhone", false, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := e.Plat.SetKeyboard(ctx, "UDID-1", true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if strings.Count(joined, "simctl boot UDID-1") != 1 || strings.Contains(joined, "simctl shutdown") {
		t.Fatalf("expected exactly one boot and no reboot:\n%s", joined)
	}
	if !strings.Contains(joined, "-replace DevicePreferences.UDID-1.ConnectHardwareKeyboard -bool NO") {
		t.Fatalf("preference not written before boot:\n%s", joined)
	}
}

func TestLongestFirst(t *testing.T) {
	mk := func(n string) *fixtures.Flow { return &fixtures.Flow{Feature: "f", File: n} }
	flows := []*fixtures.Flow{mk("a"), mk("b"), mk("c"), mk("d")}
	if order, known := longestFirst(flows, nil); known != 0 || fmt.Sprint(order) != "[0 1 2 3]" {
		t.Fatalf("no history should keep order, got %v", order)
	}
	est := map[string]time.Duration{"f/a": 10 * time.Second, "f/c": 90 * time.Second, "f/d": 30 * time.Second}
	// b has no history: gets the median (30s), stays behind d via stable sort.
	if order, known := longestFirst(flows, est); known != 3 || fmt.Sprint(order) != "[2 1 3 0]" {
		t.Fatalf("got %v known %d", order, known)
	}
}

func TestLiteIsMapRemoteOnInstalledApp(t *testing.T) {
	o := DefaultOptions()
	o.Lite, o.Platform = true, "ios"
	if err := o.Validate("run"); err != nil {
		t.Fatal(err)
	}
	if !o.MapRemote || !o.SkipBuild {
		t.Fatalf("map remote %v skip build %v", o.MapRemote, o.SkipBuild)
	}
	o = DefaultOptions()
	o.Lite, o.Parallel = true, "2"
	if err := o.Validate("run"); err == nil || !strings.Contains(err.Error(), "full integration") {
		t.Fatalf("parallel in lite: %v", err)
	}
	o = DefaultOptions()
	o.Lite, o.Attach = true, true
	if err := o.Validate("mock"); err == nil || !strings.Contains(err.Error(), "full integration") {
		t.Fatalf("attach in lite: %v", err)
	}
}

func TestLiteTakesAppIDFromFlows(t *testing.T) {
	p := testProject(t)
	p.AppID, p.Integration = "", "lite"
	for i, id := range []string{"com.a", "com.b", "com.b"} {
		path := filepath.Join(p.FlowsRoot(), "f", fmt.Sprintf("%d.yaml", i))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("appId: "+id+"\n---\n- back\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	New(p, DefaultOptions(), &execx.Fake{}, nil)
	if p.AppID != "com.b" {
		t.Fatalf("app id %q", p.AppID)
	}
}

func TestLiveProgress(t *testing.T) {
	dir := t.TempDir()
	flows := []*fixtures.Flow{
		{Feature: "a", File: "1.yaml", Rel: "a/1.yaml"},
		{Feature: "b", File: "2.yaml", Rel: "b/sub/2.yaml"},
		{Feature: "c", File: "3.yaml", Rel: "c/3.yaml"},
	}
	results := make([]*Result, 3)
	writeProgress(dir, flows, results)
	results[0] = &Result{Feature: "a", File: "1.yaml", Stage: "PASS"}
	writeProgress(dir, flows, results)
	w := filepath.Join(t.TempDir(), "run")
	os.MkdirAll(w, 0o755)
	os.Symlink(w, filepath.Join(dir, "worker-1"))
	registry.Stage(w, "FLOW", "b/sub/2.yaml")

	got, stages := LiveProgress(dir)
	var st []string
	for _, r := range got {
		st = append(st, r.Stage)
	}
	if strings.Join(st, ",") != "PASS,RUNNING,PENDING" || strings.Join(stages, ",") != "FLOW b/sub/2.yaml" {
		t.Fatalf("stages %v runners %v", st, stages)
	}
}
