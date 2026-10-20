package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testApp(t *testing.T) *app {
	t.Setenv("MOCKSTRO_STATE_DIR", t.TempDir())
	root := t.TempDir()
	m := filepath.Join(root, ".maestro")
	write(t, filepath.Join(m, "config.yaml"), "flows:\n  - flows/**\n")
	write(t, filepath.Join(m, "wiremock/stubs/shared/a.json"), `{"name":"shared/a","request":{"method":"GET","url":"/a"},"response":{"status":200}}`)
	write(t, filepath.Join(m, "wiremock/stubs/b.json"), `{"name":"b","extends":"shared/a","request":{"method":"GET","url":"/b"},"response":{"jsonBody":{"x":1}}}`)
	write(t, filepath.Join(m, "wiremock/__files/.keep"), "")
	write(t, filepath.Join(m, "wiremock/profiles/shared.json"), `{"mappings":["shared/a"]}`)
	write(t, filepath.Join(m, "wiremock/profiles/smart.json"), `{"mappings":["b"]}`)
	write(t, filepath.Join(m, "flows/account/account-smart.yaml"), "appId: x\nname: Account\ntags: [smoke, smart]\nproperties:\n  apiProfile: smart\n---\n- back\n")
	write(t, filepath.Join(m, "flows/top-up/top-up-smart.yaml"), "appId: x\nname: Top up\ntags: [regression, smart]\nproperties:\n  apiProfile: smart\n---\n- back\n")
	write(t, filepath.Join(m, "scenarios/usage.yaml"), "name: Usage\ndescription: flat usage\nproperties:\n  apiProfile: smart\n")
	p := &project.Project{Root: root, MaestroRoot: m, Env: map[string]string{}, Platform: "android", AppID: "x", GradleTask: "assemble"}

	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) { return "", nil }}
	o := engine.DefaultOptions()
	o.Owner, o.Platform = "human", "android"
	a := &app{p: p, opts: o, store: fixtures.New(p)}
	a.query = engine.New(p, o, fake, nil)
	a.dash = &dashModel{a: a}
	a.flowTab = newFlowsModel(a)
	a.cfg = newConfigModel(a)
	a.runv = &runModel{a: a}
	a.hist = &historyModel{a: a}
	a.mock = newMockModel(a)
	a.stubTab = newStubsModel(a)
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	a.Update(a.loadFixtures()())
	a.Update(statusMsg{&engine.Status{
		Devices:   []engine.StatusDevice{{Device: platform.Device{Serial: "emulator-5554", Name: "Pixel", Class: "emulator"}, Eligible: true, Designated: true}},
		Available: engine.Available{Eligible: []string{"emulator-5554"}},
		Bootable:  []string{"Pixel_B"},
	}})
	return a
}

func key(a *app, k string) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	a.Update(msg)
}

func TestTabsRender(t *testing.T) {
	a := testApp(t)
	if a.fixErr != nil {
		t.Fatalf("fixtures: %v", a.fixErr)
	}
	for i := range tabNames {
		key(a, string(rune('1'+i)))
		v := a.View()
		if !strings.Contains(v, tabNames[i]) {
			t.Fatalf("tab %s did not render", tabNames[i])
		}
	}
	key(a, "1")
	if v := a.View(); !strings.Contains(v, "emulator-5554") || !strings.Contains(v, "Pixel_B") {
		t.Fatalf("dashboard missing devices:\n%s", v)
	}
	key(a, "6")
	if v := a.View(); !strings.Contains(v, `"x": 1`) || !strings.Contains(v, "extends") {
		t.Fatalf("stub view missing resolved body:\n%s", v)
	}
}

func TestFlowPickerToConfigCommand(t *testing.T) {
	a := testApp(t)
	key(a, "2")
	if len(a.flowTab.visible) != 2 {
		t.Fatalf("visible %d", len(a.flowTab.visible))
	}
	key(a, "down") // tag: smoke
	if len(a.flowTab.visible) != 1 || a.flowTab.tags[a.flowTab.tagIdx] != "smoke" {
		t.Fatalf("tag filter: %d %v", len(a.flowTab.visible), a.flowTab.tags)
	}
	key(a, "c")
	if a.cfg.active {
		t.Fatal("config opened with nothing selected")
	}
	key(a, "enter")
	if !a.flowTab.focusList {
		t.Fatal("enter did not focus flow list")
	}
	key(a, "a")
	key(a, "c")
	if !a.cfg.active {
		t.Fatal("config did not open")
	}
	o, err := a.cfg.options()
	if err != nil {
		t.Fatal(err)
	}
	if got := commandLine(o); got != "mockstro run --tags smoke --boot-emulator --clear-proxy" {
		t.Fatalf("command: %s", got)
	}
	key(a, "down") // parallel
	key(a, "right")
	key(a, "right") // "2"
	o, _ = a.cfg.options()
	if o.Parallel != "2" {
		t.Fatalf("parallel %q", o.Parallel)
	}
	if v := a.View(); !strings.Contains(v, "--parallel 2") {
		t.Fatalf("config view:\n%s", v)
	}
	key(a, "esc")
	if a.cfg.active {
		t.Fatal("esc did not close config")
	}
	// Explicit selection becomes flow paths.
	key(a, "n")
	key(a, "f") // tag menu
	key(a, "g") // tag: all
	key(a, "f") // flow list
	key(a, " ")
	key(a, "c")
	o, _ = a.cfg.options()
	if len(o.FlowArgs) != 1 || !strings.HasSuffix(o.FlowArgs[0], ".yaml") {
		t.Fatalf("flow args %v", o.FlowArgs)
	}
}

func TestRunViewFollowsEvents(t *testing.T) {
	a := testApp(t)
	a.runv.done = make(chan struct{})
	a.runv.total = 2
	res := &engine.Result{Feature: "account", File: "account-smart.yaml", Stage: "PASS", Worker: 1}
	for _, ev := range []engine.Event{
		{Kind: engine.EvWorker, Worker: 1, Device: "emulator-5554", Port: 18081},
		{Kind: engine.EvFlowStart, Worker: 1, Flow: "account/account-smart.yaml", Text: "smart"},
		{Kind: engine.EvOutput, Worker: 1, Text: "Assert that \"Top Up\" is visible... COMPLETED"},
		{Kind: engine.EvFlowResult, Worker: 1, Result: res},
	} {
		a.Update(evMsg(ev))
	}
	key(a, "3")
	a.runv.selWorker = 1
	v := a.View()
	for _, want := range []string{"emulator-5554", "COMPLETED", "PASS", "1/2 done"} {
		if !strings.Contains(v, want) {
			t.Fatalf("run view missing %q:\n%s", want, v)
		}
	}
	a.Update(runDoneMsg{engine.NewSummary([]*engine.Result{res}, 0, "")})
	if a.runv.running() {
		t.Fatal("still running after done")
	}
}

func TestResultJumpsToItsOutput(t *testing.T) {
	a := testApp(t)
	a.runv.start2ForTest()
	first := &engine.Result{Feature: "a", File: "one.yaml", Stage: "PASS"}
	second := &engine.Result{Feature: "a", File: "two.yaml", Stage: "TEST", Exit: 1}
	for i := 0; i < 200; i++ {
		a.Update(evMsg{Kind: engine.EvOutput, Text: "noise"})
	}
	a.Update(evMsg{Kind: engine.EvFlowStart, Flow: "a/one.yaml", Text: "smart"})
	a.Update(evMsg{Kind: engine.EvOutput, Text: "ONE-OUTPUT"})
	a.Update(evMsg{Kind: engine.EvFlowResult, Result: first})
	a.Update(evMsg{Kind: engine.EvFlowStart, Flow: "a/two.yaml", Text: "smart"})
	a.Update(evMsg{Kind: engine.EvOutput, Text: "TWO-OUTPUT"})
	for i := 0; i < 200; i++ {
		a.Update(evMsg{Kind: engine.EvOutput, Text: "tail"})
	}
	a.Update(evMsg{Kind: engine.EvFlowResult, Result: second})
	key(a, "3")
	if strings.Contains(a.View(), "ONE-OUTPUT") {
		t.Fatal("tail view should not show the first flow")
	}
	key(a, "down") // select "two"
	if v := a.View(); !strings.Contains(v, "TWO-OUTPUT") || !strings.Contains(v, "▶ a/two.yaml") {
		t.Fatalf("did not jump to the second flow:\n%s", v)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if v := a.View(); !strings.Contains(v, "ONE-OUTPUT") {
		t.Fatalf("did not jump to the first flow:\n%s", v)
	}
	key(a, "G")
	if strings.Contains(a.View(), "ONE-OUTPUT") {
		t.Fatal("G should follow the tail again")
	}
}

func TestStubsOwnMergedAndBodyFile(t *testing.T) {
	a := testApp(t)
	write(t, filepath.Join(a.p.MaestroRoot, "wiremock/__files/big.json"), `{"served":"from-file"}`)
	write(t, filepath.Join(a.p.MaestroRoot, "wiremock/stubs/c.json"), `{"name":"c","request":{"method":"GET","url":"/c"},"response":{"status":201,"bodyFileName":"big.json"}}`)
	a.Update(a.loadFixtures()())
	key(a, "6")
	s := a.stubTab
	s.cursor = 0 // "b" extends shared/a
	v := a.View()
	if !strings.Contains(v, "Own JSON · b") || !strings.Contains(v, `"extends": "shared/a"`) {
		t.Fatalf("default should be the stub's own JSON:\n%s", v)
	}
	key(a, "e")
	if v := a.View(); !strings.Contains(v, "Own JSON · shared/a") {
		t.Fatalf("e should show the ancestor:\n%s", v)
	}
	key(a, "m")
	if v := a.View(); !strings.Contains(v, "Merged") || !strings.Contains(v, `"status": 200`) {
		t.Fatalf("merged view:\n%s", v)
	}
	key(a, "down")
	if v := a.View(); !strings.Contains(v, "from-file") || !strings.Contains(v, "HTTP 201") {
		t.Fatalf("bodyFileName not expanded:\n%s", v)
	}
}

func TestScreenshotHintOnlyWhenThereIsOne(t *testing.T) {
	a := testApp(t)
	a.runv.start2ForTest()
	plain := &engine.Result{Feature: "a", File: "one.yaml", Stage: "PASS"}
	shot := &engine.Result{Feature: "a", File: "two.yaml", Stage: "TEST", Exit: 1, Artifacts: "/tmp/x", Shots: []string{"/tmp/x/s.png"}}
	a.Update(evMsg{Kind: engine.EvFlowResult, Result: plain})
	a.Update(evMsg{Kind: engine.EvFlowResult, Result: shot})
	key(a, "3")
	if v := a.View(); strings.Contains(v, "s screenshot") || strings.Contains(v, "A artifacts") {
		t.Fatal("hint shown for a result without screenshots")
	}
	key(a, "down")
	if v := a.View(); !strings.Contains(v, "s screenshot") || !strings.Contains(v, "A artifacts") {
		t.Fatal("hint missing for a result with a screenshot")
	}
}

func TestTruncateKeepsColour(t *testing.T) {
	line := sOK.Render("PASS") + " " + strings.Repeat("x", 80)
	out := truncate(line, 20)
	if !strings.Contains(out, sOK.Render("PASS")) && !strings.Contains(out, "\x1b[") {
		t.Fatalf("colour lost: %q", out)
	}
	if w := lipgloss.Width(out); w > 20 {
		t.Fatalf("width %d", w)
	}
}

func TestHistoryFlowsScrollToCursor(t *testing.T) {
	a := testApp(t)
	var lines []string
	for i := 0; i < 80; i++ {
		lines = append(lines, fmt.Sprintf("result|home|flow-%02d.yaml|PASS|0|3s|", i))
	}
	dir := filepath.Join(a.p.RunsRoot(), "20260923T100000Z-1-abc")
	write(t, filepath.Join(dir, "result.tsv"), "meta|2026-09-23T10:00:00Z|60\n"+strings.Join(lines, "\n")+"\n")
	key(a, "4")
	key(a, "enter") // drill into the run's flows
	for i := 0; i < 70; i++ {
		key(a, "down")
	}
	v := a.View()
	if !strings.Contains(v, "flow-70.yaml") || strings.Contains(v, "flow-00.yaml") {
		t.Fatalf("flows panel did not follow the cursor:\n%s", v)
	}
}

// start2ForTest puts the model into a running state without an engine.
func (r *runModel) start2ForTest() {
	*r = runModel{a: r.a, started: time.Now(), done: make(chan struct{}), anchor: -1, marks: map[*engine.Result]outputMark{}}
}

func TestDashboardKeysFollowSelection(t *testing.T) {
	a := testApp(t)
	a.status.Devices[0].Keyboard = "shown"
	key(a, "1")
	v := a.View()
	if !strings.Contains(v, "v toggle keyboard") || strings.Contains(v, "b boot") || strings.Contains(v, "p clear") {
		t.Fatalf("device selected: wrong keys:\n%s", v)
	}
	key(a, "down") // onto the bootable entry
	v = a.View()
	if strings.Contains(v, "v toggle keyboard") || !strings.Contains(v, "b boot") {
		t.Fatalf("bootable selected: wrong keys:\n%s", v)
	}
}

func TestNavKey(t *testing.T) {
	cases := []struct {
		k       string
		cur, n  int
		want    int
		handled bool
	}{
		{"down", 0, 5, 1, true},
		{"up", 0, 5, 0, true},
		{"d", 2, 30, 12, true},
		{"d", 25, 30, 29, true},
		{"u", 5, 30, 0, true},
		{"G", 0, 30, 29, true},
		{"g", 20, 30, 0, true},
		{"x", 3, 30, 3, false},
	}
	for _, c := range cases {
		got, ok := navKey(c.k, c.cur, c.n)
		if got != c.want || ok != c.handled {
			t.Errorf("navKey(%q,%d,%d) = %d,%v want %d,%v", c.k, c.cur, c.n, got, ok, c.want, c.handled)
		}
	}
}

func TestMapRemoteDisablesProxyField(t *testing.T) {
	a := testApp(t)
	key(a, "2")
	key(a, "a")
	key(a, "c")
	for a.cfg.cursor != fIntercept {
		key(a, "down")
	}
	key(a, "right") // map remote
	key(a, "down")
	if a.cfg.cursor != fFBRC {
		t.Fatalf("cursor landed on %d, want FBRC (proxy skipped)", a.cfg.cursor)
	}
	o, _ := a.cfg.options()
	if o.ClearProxy || !o.MapRemote {
		t.Fatalf("map remote %v clear proxy %v", o.MapRemote, o.ClearProxy)
	}
	if v := a.View(); !strings.Contains(v, "n/a with map remote") {
		t.Fatalf("proxy not shown disabled:\n%s", v)
	}
}

func TestMockTreeCompactsFolders(t *testing.T) {
	ts := []*mockTarget{
		{kind: "flow", label: "Only", rel: "deep/nested/only.yaml"},
		{kind: "flow", label: "Home", rel: "home/home.yaml"},
		{kind: "scenario", label: "usage"},
	}
	var got []string
	for _, r := range buildRows(ts, map[string]bool{}, true) {
		got = append(got, strings.Repeat(" ", r.depth)+r.label)
	}
	want := "scenarios\n usage\nflows\n deep/nested\n  Only\n home\n  Home"
	if strings.Join(got, "\n") != want {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
}

func TestRunOnRealDeviceForcesMapRemote(t *testing.T) {
	a := testApp(t)
	a.status.Devices = append(a.status.Devices, engine.StatusDevice{Device: platform.Device{Serial: "R5CT123", Name: "Galaxy", Class: "physical"}})
	key(a, "2")
	key(a, "a")
	key(a, "c")
	for !a.cfg.realDevice() {
		key(a, "right")
	}
	o, err := a.cfg.options()
	if err != nil {
		t.Fatal(err)
	}
	if got := commandLine(o); !strings.Contains(got, "--device R5CT123 --map-remote") || strings.Contains(got, "--boot-emulator") {
		t.Fatalf("command: %s", got)
	}
	key(a, "down")
	if a.cfg.cursor != fBuild {
		t.Fatalf("parallel not skipped: cursor %d", a.cfg.cursor)
	}
	if v := a.View(); !strings.Contains(v, "n/a on a real device") || !strings.Contains(v, "required on a real device") {
		t.Fatalf("real-device fields not disabled:\n%s", v)
	}
}

func TestLiteRunForm(t *testing.T) {
	a := testApp(t)
	a.p.Integration = "lite"
	a.opts.Lite = true
	key(a, "2")
	key(a, "a")
	key(a, "c")
	o, err := a.cfg.options()
	if err != nil {
		t.Fatal(err)
	}
	if got := commandLine(o); !strings.Contains(got, "--skip-build") || !strings.Contains(got, "--map-remote") {
		t.Fatalf("command: %s", got)
	}
	key(a, "down")
	if a.cfg.cursor != fProfile {
		t.Fatalf("parallel/build not skipped: cursor %d", a.cfg.cursor)
	}
	v := a.View()
	for _, want := range []string{"lite · proxy", "mockstro does not build", "n/a in lite"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	key(a, "esc")
	key(a, "5")
	if v := a.View(); strings.Contains(v, "a attach device") || !strings.Contains(v, "attach needs full integration") {
		t.Fatalf("mock attach should be off in lite:\n%s", v)
	}
}

func TestResultLinesPickedOrCursor(t *testing.T) {
	rs := []*engine.Result{
		{Feature: "home", File: "a.yaml", Stage: "PASS"},
		{Feature: "home", File: "b.yaml", Stage: "TEST", Exit: 1},
		{Feature: "home", File: "c.yaml", Stage: "CANCELLED"},
	}
	if got := resultLines(rs, nil, 1); got != "FAIL home/b.yaml" {
		t.Fatalf("cursor: %q", got)
	}
	picked := map[string]bool{}
	togglePick(picked, rs[0])
	togglePick(picked, rs[2])
	if got := resultLines(rs, picked, 1); got != "PASS home/a.yaml\nCANCELLED home/c.yaml" {
		t.Fatalf("picked: %q", got)
	}
}

func TestMockShowsTargetStubsAndOpensThem(t *testing.T) {
	a := testApp(t)
	key(a, "5")
	m := a.mock
	for i, r := range m.visible {
		if r.target != nil && r.target.name == "usage" {
			m.cursor = i
		}
	}
	v := a.View()
	for _, want := range []string{"Stubs served", "profile smart", "profile shared", "GET url=/b", "shared/a"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "m unmatched only") {
		t.Fatalf("journal toggle shown while idle:\n%s", v)
	}
	key(a, "f")
	key(a, "down")
	key(a, "enter")
	if a.tab != tabStubs || a.stubTab.visible[a.stubTab.cursor] != "shared/a" {
		t.Fatalf("tab %d cursor %v", a.tab, a.stubTab.visible)
	}
}

func TestHistoryFollowsLiveRun(t *testing.T) {
	a := testApp(t)
	dir := filepath.Join(a.p.RunsRoot(), "run-live")
	write(t, filepath.Join(dir, "progress.tsv"), "result|a|1.yaml|PASS|0|3s|\nresult|a|2.yaml|TEST|1|5s|\nplan|b|3.yaml|b/3.yaml\nplan|b|4.yaml|b/4.yaml\n")
	write(t, filepath.Join(dir, "stage"), "FLOW\n")
	write(t, filepath.Join(dir, "current-flow"), "b/3.yaml\n")
	write(t, filepath.Join(dir, "logs", "b-3.log"), "tapping login\n")
	hr := &histRun{dir: dir, active: true}
	readResults(hr)
	key(a, "4")
	a.hist.apply([]*histRun{hr})
	key(a, "enter")
	v := a.View()
	for _, want := range []string{"2/4", "1✔", "1✘", "▶ FLOW b/3.yaml", "RUNNING b/3.yaml", "PENDING b/4.yaml", "tapping login"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	if a.hist.resCur != 2 {
		t.Fatalf("enter should land on the running flow, got %d", a.hist.resCur)
	}
}

func TestHistoryRerunAll(t *testing.T) {
	a := testApp(t)
	dir := filepath.Join(a.p.RunsRoot(), "run-done")
	write(t, filepath.Join(dir, "result.tsv"), "meta|2026-01-01T00:00:00Z|8\nresult|account|account-smart.yaml|PASS|0|3s|\nresult|top-up|top-up-smart.yaml|TEST|1|5s|\n")
	hr := &histRun{dir: dir}
	readResults(hr)
	key(a, "4")
	a.hist.apply([]*histRun{hr})
	key(a, "a")
	if a.tab != tabFlows || !a.cfg.active || len(a.cfg.flows) != 2 {
		t.Fatalf("tab %d active %v flows %d", a.tab, a.cfg.active, len(a.cfg.flows))
	}
}

func TestHistoryShowsFlowStubs(t *testing.T) {
	a := testApp(t)
	dir := filepath.Join(a.p.RunsRoot(), "run-done")
	write(t, filepath.Join(dir, "result.tsv"), "meta|2026-01-01T00:00:00Z|3\nresult|account|account-smart.yaml|PASS|0|3s|\n")
	hr := &histRun{dir: dir}
	readResults(hr)
	key(a, "4")
	a.hist.apply([]*histRun{hr})
	key(a, "enter")
	key(a, "m")
	v := a.View()
	for _, want := range []string{"Flow stubs", "profile smart", "GET url=/b", "profile shared", "shared/a"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	key(a, "enter")
	key(a, "down")
	key(a, "enter")
	if a.tab != tabStubs || a.stubTab.visible[a.stubTab.cursor] != "shared/a" {
		t.Fatalf("tab %d", a.tab)
	}
}

func TestMockPhoneAttachment(t *testing.T) {
	a := testApp(t)
	locked := "other-run"
	a.status.Devices = append(a.status.Devices,
		engine.StatusDevice{Device: platform.Device{Serial: "phone", Class: "physical"}},
		engine.StatusDevice{Device: platform.Device{Serial: "busy-phone", Class: "physical"}, LockedBy: &locked},
	)
	key(a, "5")
	key(a, "a")
	key(a, "a")
	if got := a.mock.attachDevice; got != "phone" {
		t.Fatalf("selected %q instead of phone", got)
	}
	if v := a.View(); !strings.Contains(v, "phone") || !strings.Contains(v, "installed app via adb reverse") {
		t.Fatalf("phone attachment instructions missing:\n%s", v)
	}
	o := a.mock.startOptions("phone")
	if !o.Attach || !o.SkipBuild || !o.ClearProxy || o.DeviceArg != "phone" {
		t.Fatalf("phone options: %+v", o)
	}
	if err := o.Validate("mock"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.mock.devices(), ","); strings.Contains(got, "busy-phone") {
		t.Fatal("locked phone offered for attachment")
	}
	if a.mock.startOptions("emulator-5554").SkipBuild {
		t.Fatal("emulator no longer builds by default")
	}
	if a.mock.startOptions("").Attach {
		t.Fatal("server-only attaches a device")
	}
	key(a, "a")
	if a.mock.attachDevice != "" {
		t.Fatal("device selector did not wrap to server-only")
	}
	a.query.Owner = "agent"
	if got := strings.Join(a.mock.devices(), ","); strings.Contains(got, "phone") {
		t.Fatal("agent offered a physical device")
	}
}

func TestMockAttachmentUnavailableAfterRefresh(t *testing.T) {
	for _, selected := range []string{"emulator-5554", "phone"} {
		t.Run(selected, func(t *testing.T) {
			a := testApp(t)
			phone := engine.StatusDevice{Device: platform.Device{Serial: "phone", Class: "physical"}}
			a.status.Devices = append(a.status.Devices, phone)
			key(a, "5")
			key(a, "a")
			if selected == "phone" {
				key(a, "a")
			}
			st := &engine.Status{Devices: []engine.StatusDevice{phone}}
			if selected == "phone" {
				// A locked phone becomes unavailable even while it remains connected.
				locked := "other-run"
				st.Devices[0].LockedBy = &locked
				st.Available.Eligible = []string{"emulator-5554"}
			}
			a.Update(statusMsg{st})
			if a.mock.attachDevice != selected {
				t.Fatalf("refresh changed selection to %q", a.mock.attachDevice)
			}
			if v := a.View(); !strings.Contains(v, selected+" (unavailable)") {
				t.Fatalf("missing unavailable device indicator:\n%s", v)
			}
			for i, row := range a.mock.visible {
				if row.target != nil {
					a.mock.cursor = i
					break
				}
			}
			if cmd := a.mock.key(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || a.mock.starting || a.mock.startErr == nil {
				t.Fatal("unavailable attachment did not prevent starting a mock")
			}
			key(a, "a")
			if a.mock.attachDevice == selected {
				t.Fatal("could not reselect an available device")
			}
		})
	}
}

func TestMockAttachmentSurvivesDeviceReordering(t *testing.T) {
	a := testApp(t)
	a.status.Available.Eligible = append(a.status.Available.Eligible, "emulator-5556")
	key(a, "5")
	key(a, "a")
	st := *a.status
	st.Available.Eligible = []string{"emulator-5556", "emulator-5554"}
	a.Update(statusMsg{&st})
	if a.mock.attachDevice != "emulator-5554" {
		t.Fatalf("refresh changed selected device to %q", a.mock.attachDevice)
	}
	key(a, "a")
	if a.mock.attachDevice != "" {
		t.Fatal("cycling did not follow the selected device's new position")
	}
}
