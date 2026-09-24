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

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/platform"
	"github.com/feernandooff/trun/internal/project"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testApp(t *testing.T) *app {
	t.Setenv("TRUN_STATE_DIR", t.TempDir())
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
	key(a, "enter")
	if !a.cfg.active {
		t.Fatal("config did not open")
	}
	o, err := a.cfg.options()
	if err != nil {
		t.Fatal(err)
	}
	if got := commandLine(o); got != "trun run --tags smoke --boot-emulator --clear-proxy" {
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
	key(a, "g") // tag: all
	key(a, "f") // flow list
	key(a, " ")
	key(a, "enter")
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
	key(a, "enter")
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
