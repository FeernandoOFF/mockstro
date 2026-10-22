package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/project"
	"gopkg.in/yaml.v3"
)

type TargetRun struct {
	Project *project.Project
	Options Options
	Flows   []*fixtures.Flow
}

// Plan resolves test inputs and target-specific validation before acquiring resources.
func Plan(p *project.Project, o Options) ([]TargetRun, bool, error) {
	targets, err := p.Selected(o.Platform)
	if err != nil {
		return nil, false, err
	}
	if o.Only != nil {
		selected := targets[:0]
		for _, target := range targets {
			if len(o.Only[target.Platform]) > 0 || len(o.Only[""]) > 0 {
				selected = append(selected, target)
			}
		}
		targets = selected
		if len(targets) == 0 {
			return nil, false, fmt.Errorf("no failed flow/platform pairs selected")
		}
	}
	values := project.Variables{}
	for _, file := range o.EnvFiles {
		v, err := project.ReadVariables(file)
		if err != nil {
			return nil, false, err
		}
		maps.Copy(values, v)
	}
	for k, v := range o.Variables {
		if err := project.ValidateVariable(k); err != nil {
			return nil, false, err
		}
		values[k] = v
	}
	o.Variables = values
	// Maestro workspace defaults remain supported, but cannot override runner inputs.
	if b, err := os.ReadFile(p.ConfigPath()); err == nil {
		var workspace struct {
			Env project.Variables `yaml:"env"`
		}
		if err = yaml.Unmarshal(b, &workspace); err != nil {
			return nil, false, fmt.Errorf("Maestro config: %w", err)
		}
	}
	if len(targets) > 1 && (o.DeviceArg != "" || o.BootAVD != "" || o.PortPinned) {
		return nil, false, fmt.Errorf("--device, a named --boot-emulator and --server-port require --platform when multiple platforms are selected")
	}
	var out []TargetRun
	sequential := o.MapRemote
	for _, target := range targets {
		child := o
		child.Platform = target.Platform
		child.Lite = target.Lite()
		if child.Lite {
			child.Parallel = ""
			sequential = true
		}
		if o.Only != nil {
			child.FlowArgs = o.Only[target.Platform]
			if len(child.FlowArgs) == 0 {
				child.FlowArgs = o.Only[""]
			}
			if len(child.FlowArgs) == 0 {
				continue
			}
		}
		if err := child.Validate("run"); err != nil {
			return nil, false, fmt.Errorf("%s: %w", target.Platform, err)
		}
		engine := New(target, child, nil, nil)
		if child.ProfileArg != "" && !engine.Store.ProfileExists(child.ProfileArg) {
			return nil, false, fmt.Errorf("unknown profile %q", child.ProfileArg)
		}
		flows, err := engine.DiscoverFlows()
		if err != nil {
			return nil, false, err
		}
		for _, flow := range flows {
			if flow.Header.AppID != "${APP_ID}" && flow.Header.AppID != target.AppID {
				return nil, false, fmt.Errorf("%s: %s must use appId: ${APP_ID} for platform %s", flow.Rel, flow.Header.AppID, target.Platform)
			}
		}
		out = append(out, TargetRun{target, child, flows})
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("no failed flow/platform pairs selected")
	}
	return out, sequential, nil
}

// RunSuite runs native platform engines with independent resources and one parent report.
// Preparation and test failures belong to their target; cancellation belongs to the suite.
func RunSuite(ctx context.Context, p *project.Project, o Options, ex execx.Runner, sink Sink) *Summary {
	var jarMu sync.Mutex
	jars := map[string]string{}
	return runSuite(ctx, p, o, ex, sink, func(e *Engine, flows []*fixtures.Flow) *Summary {
		if err := e.CheckEnvironment("run"); err != nil {
			return &Summary{Err: err}
		}
		jarMu.Lock()
		jar := jars[e.P.WireMockVersion]
		if jar == "" {
			var err error
			jar, err = e.EnsureJar(ctx)
			if err != nil {
				jarMu.Unlock()
				return &Summary{Err: err}
			}
			jars[e.P.WireMockVersion] = jar
		}
		jarMu.Unlock()
		e.jar = jar
		return e.runPlanned(ctx, flows)
	})
}

// runSuite keeps target scheduling independent from native build/device execution.
func runSuite(ctx context.Context, p *project.Project, o Options, ex execx.Runner, sink Sink, execute func(*Engine, []*fixtures.Flow) *Summary) *Summary {
	if sink == nil {
		sink = func(Event) {}
	}
	plans, sequential, err := Plan(p, o)
	if err != nil {
		return &Summary{Err: err}
	}
	if _, err := fixtures.New(p).ValidateAll(true); err != nil {
		return &Summary{Err: err}
	}
	parent := New(p, o, ex, sink)
	parent.Prune(ctx, KeepRuns())
	start := time.Now()
	dir := filepath.Join(p.RunsRoot(), "session-"+NewRunID())
	if err = os.MkdirAll(dir, 0755); err != nil {
		return &Summary{Err: err}
	}
	mode := "concurrent"
	if sequential {
		mode = "sequential (lite or map-remote uses port 18080)"
	}
	parent.Logf("Run plan: %d platform(s), %s; flows use APP_ID and native Maestro platform conditions.", len(plans), mode)
	var mu sync.Mutex
	completed := map[string]*Result{}
	writeSuiteProgress(dir, plans, completed)
	sums := make([]*Summary, len(plans))
	run := func(i int) {
		plan := plans[i]
		target := *plan.Project
		target.RunRoot = filepath.Join(dir, target.Platform)
		opts := plan.Options
		opts.managed = true
		if opts.RecordDir != "" {
			opts.RecordDir = filepath.Join(opts.RecordDir, target.Platform)
		}
		deliver := func(ev Event) {
			if ev.Kind == EvSessionDone {
				return
			}
			ev.Platform = target.Platform
			ev.Worker = i*100 + ev.Worker + 1
			mu.Lock()
			defer mu.Unlock()
			if ev.Result != nil {
				// Progress and observers own a snapshot; the child may finalize its result later.
				result := *ev.Result
				result.Platform, result.Worker = target.Platform, ev.Worker
				result.Shots = slices.Clone(result.Shots)
				result.Verifs = slices.Clone(result.Verifs)
				result.Unmatched = slices.Clone(result.Unmatched)
				ev.Result = &result
				completed[result.Key()] = ev.Result
				writeSuiteProgress(dir, plans, completed)
			}
			if ev.Kind == EvWorker {
				_ = os.Symlink(ev.Text, filepath.Join(dir, fmt.Sprintf("worker-%s-%d", target.Platform, ev.Worker)))
			}
			sink(ev)
		}
		e := New(&target, opts, ex, deliver)
		// Avoid concurrent interactive prompts. Explicit --boot-emulator is unattended.
		if ctx.Err() != nil {
			sums[i] = &Summary{Err: ctx.Err()}
		} else {
			sums[i] = execute(e, plan.Flows)
		}
		s := sums[i]
		seen := map[string]bool{}
		for _, r := range s.Results {
			r.Platform, r.Worker = target.Platform, i*100+r.Worker+1
			seen[r.Path] = true
		}
		for _, f := range plan.Flows {
			if seen[f.Path] {
				continue
			}
			stage, message := "UNSTARTED", "flow did not start"
			if ctx.Err() != nil {
				stage, message = "CANCELLED", ctx.Err().Error()
			} else if s.Err != nil {
				stage, message = "SETUP", s.Err.Error()
			}
			r := &Result{Platform: target.Platform, Path: f.Path, Feature: f.Feature, File: f.File, Stage: stage, Exit: 1, Message: message}
			s.Results = append(s.Results, r)
			deliver(Event{Kind: EvFlowResult, Result: r, Flow: f.Rel})
		}
	}
	if sequential {
		for i := range plans {
			run(i)
		}
	} else {
		var wg sync.WaitGroup
		for i := range plans {
			wg.Add(1)
			go func(i int) { defer wg.Done(); run(i) }(i)
		}
		wg.Wait()
	}
	var results []*Result
	for _, s := range sums {
		results = append(results, s.Results...)
	}
	summary := NewSummary(results, time.Since(start), dir)
	summary.Cancelled = summary.Cancelled || ctx.Err() != nil
	for _, r := range results {
		completed[r.Key()] = r
	}
	writeSuiteProgress(dir, plans, completed)
	parent.persist(summary, dir)
	report := struct {
		SchemaVersion int       `json:"schemaVersion"`
		Results       []*Result `json:"results"`
		Cancelled     bool      `json:"cancelled"`
	}{1, results, summary.Cancelled}
	if b, err := json.MarshalIndent(report, "", "  "); err == nil {
		_ = fsx.WriteAtomic(filepath.Join(dir, "run.json"), b)
	}
	sink(Event{Kind: EvSessionDone, Summary: summary})
	return summary
}

func writeSuiteProgress(dir string, plans []TargetRun, done map[string]*Result) {
	var b strings.Builder
	for _, plan := range plans {
		for _, f := range plan.Flows {
			key := (&Result{Platform: plan.Project.Platform, Feature: f.Feature, File: f.File}).Key()
			if r := done[key]; r != nil {
				b.WriteString("result|" + r.TSV() + "\n")
			} else {
				fmt.Fprintf(&b, "plan|%s|%s|%s|%s\n", f.Feature, f.File, f.Rel, plan.Project.Platform)
			}
		}
	}
	_ = fsx.WriteAtomic(filepath.Join(dir, "progress.tsv"), []byte(b.String()))
}
