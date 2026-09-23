package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/platform"
	"github.com/feernandooff/trun/internal/project"
)

func testProject(t *testing.T) *project.Project {
	t.Helper()
	t.Setenv("TRUN_STATE_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAESTRO_AGENT_AVDS", "")
	t.Setenv("MAESTRO_DEVICE_CLASS", "")
	t.Setenv("TRUN_MAESTRO_TESTS_DIR", t.TempDir())
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
		for _, s := range sortedKeys(avds) {
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

func (b *bootSpy) Boot(_ context.Context, name string, _ func(string)) (string, error) {
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

func TestDeleteRemovesLinkedMaestroOutput(t *testing.T) {
	e, _ := testEngine(t, "agent", func(c execx.Cmd) (string, error) { return "", nil })
	root := e.P.RunsRoot()
	tests := os.Getenv("TRUN_MAESTRO_TESTS_DIR")
	stamp := time.Now().Add(-time.Hour).UTC()
	id := stamp.Format("20060102T150405Z") + "-42-abc123"
	other := stamp.Format("20060102T150405Z") + "-43-def456"
	run := filepath.Join(root, id)
	os.MkdirAll(run, 0o755)
	os.WriteFile(filepath.Join(run, "result.tsv"), []byte("meta|x|1\n"), 0o644)
	os.Chtimes(run, stamp.Add(time.Minute), stamp.Add(time.Minute))
	os.Chtimes(filepath.Join(run, "result.tsv"), stamp.Add(time.Minute), stamp.Add(time.Minute))
	mk := func(name, log string) string {
		d := filepath.Join(tests, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "maestro.log"), []byte(log), 0o644)
		return d
	}
	local := stamp.Add(10 * time.Second).Local().Format("2006-01-02_150405")
	ours := mk(local, "Running .maestro/.runtime/runs/"+id+"/maestro-flows/flows/a.yaml")
	shared := mk(stamp.Add(20*time.Second).Local().Format("2006-01-02_150405"),
		"runs/"+id+"/x runs/"+other+"/y")
	unrelated := mk(stamp.Add(30*time.Second).Local().Format("2006-01-02_150405"), "runs/"+other+"/y")
	if err := e.DeleteRunDir(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ours); err == nil {
		t.Fatal("run's own Maestro output survived")
	}
	for _, d := range []string{shared, unrelated} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("deleted output another run still references: %s", d)
		}
	}
}
