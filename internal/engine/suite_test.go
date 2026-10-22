package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/project"
)

func suiteProject(t *testing.T) *project.Project {
	t.Helper()
	t.Setenv("MOCKSTRO_STATE_DIR", t.TempDir())
	root := t.TempDir()
	_, err := project.Init(root, project.Config{Env: project.Variables{"USER": "suite"}, Platforms: map[string]project.TargetConfig{
		"android": {AppID: "example.android", Integration: "full", Env: project.Variables{"USER": "android"}},
		"ios":     {AppID: "example.ios", Integration: "full"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanPlatformSelectionAndVariables(t *testing.T) {
	p := suiteProject(t)
	o := DefaultOptions()
	o.Parallel = "2"
	vars := filepath.Join(t.TempDir(), "vars.yaml")
	os.WriteFile(vars, []byte("USER: file\nCOUNT: 2\n"), 0644)
	o.EnvFiles = []string{vars}
	o.Variables = project.Variables{"USER": "cli"}
	plans, seq, err := Plan(p, o)
	if err != nil || seq || len(plans) != 2 {
		t.Fatal(plans, seq, err)
	}
	for _, plan := range plans {
		if plan.Options.Parallel != "2" || plan.Options.Variables["USER"] != "cli" || plan.Options.Variables["COUNT"] != "2" {
			t.Fatal(plan)
		}
	}
	p.Targets["ios"].Integration = "lite"
	plans, seq, err = Plan(p, o)
	if err != nil || !seq || plans[1].Options.Parallel != "" || !plans[1].Options.MapRemote || !plans[1].Options.SkipBuild || plans[0].Options.MapRemote {
		t.Fatal(plans, seq, err)
	}
	o.Platform = "android"
	plans, _, err = Plan(p, o)
	if err != nil || len(plans) != 1 {
		t.Fatal(plans, err)
	}
	o.Platform = ""
	o.DeviceArg = "emulator-5554"
	if _, _, err := Plan(p, o); err == nil {
		t.Fatal("accepted ambiguous device")
	}
}

func TestSuiteConcurrentTargetsAndIsolatedPorts(t *testing.T) {
	p := suiteProject(t)
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.Name == "ps" {
			return "start", nil
		}
		return "", nil
	}}
	o := DefaultOptions()
	o.Parallel = "2"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan string, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	ports := map[string]int{}
	var events []Event
	result := make(chan *Summary, 1)
	go func() {
		result <- runSuite(ctx, p, o, fake, func(ev Event) { events = append(events, ev) }, func(e *Engine, planned []*fixtures.Flow) *Summary {
			if e.Opts.Parallel != "2" || !e.Opts.managed || !strings.Contains(e.P.RunsRoot(), "session-") {
				return &Summary{Err: fmt.Errorf("incorrect child options")}
			}
			locks := e.newLocks("suite-" + e.Opts.Platform)
			defer locks.ReleaseAll()
			port, err := e.AcquirePort(ctx, locks, "run", "emulator-5554")
			if err != nil {
				return &Summary{Err: err}
			}
			mu.Lock()
			ports[e.Opts.Platform] = port
			mu.Unlock()
			ready <- e.Opts.Platform
			select {
			case <-release:
			case <-ctx.Done():
				return &Summary{Err: ctx.Err()}
			}
			flows := planned
			f := flows[0]
			dir := filepath.Join(e.P.RunsRoot(), "worker")
			os.MkdirAll(dir, 0755)
			e.emit(Event{Kind: EvWorker, Device: e.Opts.Platform, Port: port, Text: dir})
			stage, exit := "PASS", 0
			if e.Opts.Platform == "ios" {
				stage, exit = "TEST", 1
			}
			r := &Result{Platform: e.Opts.Platform, Feature: f.Feature, File: f.File, Path: f.Path, Stage: stage, Exit: exit}
			e.emit(Event{Kind: EvFlowResult, Result: r})
			return NewSummary([]*Result{r}, time.Second, dir)
		})
	}()
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("platforms did not overlap")
		}
	}
	mu.Lock()
	snapshot := maps.Clone(ports)
	mu.Unlock()
	if snapshot["android"] == snapshot["ios"] || snapshot["android"] == 18080 || snapshot["ios"] == 18080 {
		t.Fatal("ports not isolated", snapshot)
	}
	close(release)
	s := <-result
	if s.Err != nil || s.Passed != 1 || s.Failed != 1 || len(s.Results) != 2 {
		t.Fatalf("summary: %+v", s)
	}
	if s.Results[0].Key() == s.Results[1].Key() {
		t.Fatal("platform identity collided")
	}
	_, _, saved, err := LoadResults(p.LastRunPath())
	if err != nil || len(saved) != 2 || saved[1].Platform != "ios" || saved[1].Path == "" {
		t.Fatal(saved, err)
	}
	var report struct {
		Results []*Result `json:"results"`
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "run.json"))
	if err := json.Unmarshal(b, &report); err != nil || len(report.Results) != 2 {
		t.Fatal(string(b), err)
	}
	b, _ = os.ReadFile(filepath.Join(s.Dir, "junit.xml"))
	if !strings.Contains(string(b), `classname="android.smoke"`) || !strings.Contains(string(b), `classname="ios.smoke"`) {
		t.Fatal(string(b))
	}
	only := FailedPairs(o, saved, p.Root)
	plans, _, err := Plan(p, only)
	if err != nil || len(plans) != 1 || plans[0].Project.Platform != "ios" {
		t.Fatal(plans, err)
	}
	workerIDs := map[int]bool{}
	for _, ev := range events {
		if ev.Kind == EvWorker {
			workerIDs[ev.Worker] = true
		}
	}
	if len(workerIDs) != 2 {
		t.Fatal("workers from different targets collided", events)
	}
	dirs := New(p, o, fake, nil).RunDirs(context.Background(), false)
	if len(dirs) != 1 {
		t.Fatal("combined run did not own child storage", dirs)
	}
	if err := New(p, o, fake, nil).DeleteRunDir(context.Background(), s.Dir); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteLiteSerializesAndSetupFailureDoesNotHideOtherTarget(t *testing.T) {
	p := suiteProject(t)
	p.Targets["ios"].Integration = "lite"
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) { return "", nil }}
	var order []string
	s := runSuite(context.Background(), p, DefaultOptions(), fake, nil, func(e *Engine, planned []*fixtures.Flow) *Summary {
		order = append(order, e.Opts.Platform)
		if e.Opts.Platform == "android" {
			return &Summary{Err: fmt.Errorf("build failed")}
		}
		flows := planned
		f := flows[0]
		return NewSummary([]*Result{{Path: f.Path, Feature: f.Feature, File: f.File, Stage: "PASS", Platform: "ios"}}, 0, "")
	})
	if strings.Join(order, ",") != "android,ios" || s.Failed != 1 || s.Passed != 1 || s.Results[0].Stage != "SETUP" || s.Results[0].Message != "build failed" {
		t.Fatal(order, s)
	}
}

func TestFlowInputPrecedenceAndNativePlatformConditions(t *testing.T) {
	p := suiteProject(t)
	path := filepath.Join(p.FlowsRoot(), "smoke/launch.yaml")
	source := "appId: ${APP_ID}\ntags: [smoke, ios]\nenv:\n  USER: case\n  CASE_ONLY: true\nproperties:\n  apiProfile: default\n---\n- runFlow:\n    when:\n      platform: iOS\n    commands:\n      - launchApp\n"
	os.WriteFile(path, []byte(source), 0644)
	p.Targets["android"].LocalVariables = project.Variables{"USER": "local"}
	o := DefaultOptions()
	plans, _, err := Plan(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || len(plans[0].Flows) != 1 || len(plans[1].Flows) != 1 {
		t.Fatal("ios tag filtered a platform", plans)
	}
	e := New(plans[0].Project, plans[0].Options, nil, nil)
	f := plans[0].Flows[0]
	values := e.flowVariables(f, 18123)
	if values["USER"] != "local" || values["APP_ID"] != "example.android" || values["WIREMOCK_PORT"] != "18123" || values["CASE_ONLY"] != "true" {
		t.Fatal(values)
	}
	e.Opts.Variables = project.Variables{"USER": "cli", "EMPTY": "", "SPACES": "one two=three"}
	values = e.flowVariables(f, 18124)
	if values["USER"] != "cli" || values["EMPTY"] != "" || values["SPACES"] != "one two=three" || f.Header.Variables["USER"] != "case" || e.P.Variables["USER"] != "suite" {
		t.Fatal("lost inputs or mutated a source layer", values)
	}
	sanitized, err := fixtures.SanitizedFlow(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sanitized), "appId: ${APP_ID}") || !strings.HasSuffix(string(sanitized), strings.SplitN(source, "---\n", 2)[1]) || strings.Contains(string(sanitized), "apiProfile") {
		t.Fatal(string(sanitized))
	}
	os.WriteFile(path, []byte(strings.ReplaceAll(source, "CASE_ONLY: true", "APP_ID: bad")), 0644)
	if _, _, err := Plan(p, o); err == nil {
		t.Fatal("flow header overrode APP_ID")
	}
}

func TestCancellationStopsBothTargetsAndProducesResults(t *testing.T) {
	p := suiteProject(t)
	fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	cleaned := make(chan struct{}, 2)
	done := make(chan *Summary, 1)
	go func() {
		done <- runSuite(ctx, p, DefaultOptions(), fake, nil, func(e *Engine, planned []*fixtures.Flow) *Summary {
			defer func() { cleaned <- struct{}{} }()
			started <- struct{}{}
			<-ctx.Done()
			return &Summary{Err: ctx.Err()}
		})
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("target did not start")
		}
	}
	cancel()
	select {
	case s := <-done:
		if !s.Cancelled || len(s.Results) != 2 || s.Results[0].Stage != "CANCELLED" || s.Results[1].Stage != "CANCELLED" {
			t.Fatal(s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("suite did not stop after cancellation")
	}
	if len(cleaned) != 2 {
		t.Fatal("returned before both target executors cleaned up")
	}
}

func TestSuitePublishesIndependentResultSnapshots(t *testing.T) {
	p := suiteProject(t)
	var published []*Result
	summary := runSuite(context.Background(), p, DefaultOptions(), &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }}, func(ev Event) {
		if ev.Result != nil {
			published = append(published, ev.Result)
		}
	}, func(e *Engine, flows []*fixtures.Flow) *Summary {
		f := flows[0]
		result := &Result{Feature: f.Feature, File: f.File, Path: f.Path, Stage: "PASS", Worker: 2, Shots: []string{"captured.png"}, Verifs: []VerificationResult{{Stub: "original"}}, Unmatched: []string{"original"}}
		e.emit(Event{Kind: EvFlowResult, Worker: 2, Result: result})
		result.Message = "finalized"
		result.Shots[0] = "changed.png"
		result.Verifs[0].Stub = "changed"
		result.Unmatched[0] = "changed"
		return NewSummary([]*Result{result}, 0, "")
	})
	if summary.Failed != 0 || len(published) != 2 {
		t.Fatalf("summary %+v, published %d", summary, len(published))
	}
	for _, result := range published {
		if result.Message != "" || result.Shots[0] != "captured.png" || result.Verifs[0].Stub != "original" || result.Unmatched[0] != "original" {
			t.Fatalf("published result was changed by child: %+v", result)
		}
	}
	for _, result := range summary.Results {
		worker := 3
		if result.Platform == "ios" {
			worker = 103
		}
		if result.Worker != worker {
			t.Fatalf("summary worker %d for %s", result.Worker, result.Platform)
		}
	}
}
