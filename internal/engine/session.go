package engine

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/build"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/scheduler"
)

// Run executes the selected flows, single-device or parallel, and always returns a summary
// (possibly with Err set) once every owned resource has been cleaned up.
func (e *Engine) Run(ctx context.Context) *Summary {
	flows, err := e.DiscoverFlows()
	if err != nil {
		return e.done(&Summary{Err: err})
	}
	return e.runPlanned(ctx, flows)
}

func (e *Engine) runPlanned(ctx context.Context, flows []*fixtures.Flow) *Summary {
	start := time.Now()
	if !e.Opts.managed {
		if removed, freed := e.Prune(ctx, KeepRuns()); removed > 0 || freed > 0 {
			e.Logf("Pruned %d old run dir(s), freed %s (keeping %d; MOCKSTRO_KEEP_RUNS to change).", removed, HumanSize(freed), KeepRuns())
		}
	}
	if e.Opts.Parallel != "" {
		return e.done(e.runParallel(ctx, flows, start))
	}
	return e.done(e.runSingle(ctx, flows, start))
}

// batched runs several flows per Maestro process; recording needs one process per flow.
func (e *Engine) batched() bool { return !e.Opts.Isolate && !e.Opts.Record }

func (e *Engine) done(s *Summary) *Summary {
	e.emit(Event{Kind: EvSessionDone, Summary: s})
	return s
}

func (e *Engine) runSingle(ctx context.Context, flows []*fixtures.Flow, start time.Time) *Summary {
	run := e.newRun("run", 0)
	defer run.Cleanup()
	if err := run.Setup(ctx, true); err != nil {
		writeRunError(run.Dir, err)
		s := NewSummary(nil, time.Since(start), run.Dir)
		s.Err = err
		return s
	}
	results, failures := e.executeFlows(ctx, flows, 1, run.Dir, func(context.Context, int) (*Run, error) { return run, nil })
	if ctx.Err() == nil && e.Opts.Record && e.Opts.Optimize {
		e.OptimizeRecordings(ctx, results)
	}
	s := NewSummary(results, time.Since(start), run.Dir)
	if len(failures) > 0 {
		s.Err = fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	e.persist(s, run.Dir)
	return s
}

// flowWorker adapts an owned run to the scheduler. Scheduling knows job indexes;
// only this adapter knows Maestro flows, mock activation and result persistence.
type flowWorker struct {
	run     *Run
	flows   []*fixtures.Flow
	publish func(int, *Result)
}

func (w *flowWorker) Close() { w.run.Cleanup() }
func (w *flowWorker) Execute(ctx context.Context, jobs []int, stopped func() bool, done func(int, bool)) bool {
	if w.run.e.batched() {
		cancelled := false
		chunk := make([]*fixtures.Flow, len(jobs))
		for j, i := range jobs {
			chunk[j] = w.flows[i]
		}
		halted := w.run.RunBatch(ctx, chunk, stopped, func(j int, res *Result) {
			i := jobs[j]
			w.publish(i, res)
			done(i, !res.Passed() && res.Stage != "CANCELLED")
			cancelled = cancelled || res.Stage == "CANCELLED"
		})
		return halted || cancelled
	}
	for _, i := range jobs {
		if ctx.Err() != nil || stopped() {
			return true
		}
		res := w.run.RunFlow(ctx, w.flows[i])
		w.publish(i, res)
		done(i, !res.Passed() && res.Stage != "CANCELLED")
		if res.Stage == "CANCELLED" {
			return true
		}
	}
	return false
}

func (e *Engine) executeFlows(ctx context.Context, flows []*fixtures.Flow, workers int, dir string, open func(context.Context, int) (*Run, error)) ([]*Result, []string) {
	plan := scheduler.Plan{Jobs: len(flows), Workers: workers, Batched: e.batched(), FailFast: e.Opts.FailFast}
	if workers > 1 {
		order, known := longestFirst(flows, e.FlowEstimates(ctx))
		if known > 0 {
			plan.Order = order
			e.Logf("Scheduling longest flows first (%d/%d with past durations).", known, len(flows))
		}
	}
	var mu sync.Mutex
	results := make([]*Result, len(flows))
	writeProgress(dir, flows, results)
	report, err := scheduler.Run(ctx, plan, func(ctx context.Context, w int) (scheduler.Worker, error) {
		run, err := open(ctx, w)
		if err != nil {
			return nil, err
		}
		return &flowWorker{run: run, flows: flows, publish: func(i int, res *Result) {
			mu.Lock()
			defer mu.Unlock()
			results[i] = res
			writeProgress(dir, flows, results)
		}}, nil
	})
	var failures []string
	if err != nil {
		failures = append(failures, err.Error())
	}
	for _, failure := range report.Errors {
		failures = append(failures, failure.Error())
		e.emit(Event{Kind: EvLog, Level: "error", Worker: failure.Worker, Text: failure.Err.Error()})
	}
	for i, f := range flows {
		if results[i] != nil {
			continue
		}
		stage := "UNSTARTED"
		if ctx.Err() != nil {
			stage = "CANCELLED"
		} else if len(report.States) > i && report.States[i] == scheduler.Claimed {
			stage = "INTERRUPTED"
		}
		results[i] = &Result{Platform: e.Opts.Platform, Feature: f.Feature, File: f.File, Path: f.Path, Stage: stage, Exit: 1}
		e.emit(Event{Kind: EvFlowResult, Flow: f.Rel, Result: results[i]})
	}
	return results, failures
}

func (e *Engine) runParallel(ctx context.Context, flows []*fixtures.Flow, start time.Time) *Summary {
	sessionDir := filepath.Join(e.P.RunsRoot(), "session-"+NewRunID())
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return &Summary{Err: err}
	}
	workers := 0
	if e.Opts.Parallel == "auto" {
		// auto means "use whatever is already free"; it only boots when nothing is.
		workers = len(e.FreeEligible(ctx))
		if workers == 0 {
			workers = 1
		}
	} else {
		workers, _ = strconv.Atoi(e.Opts.Parallel)
	}
	if workers > len(flows) {
		workers = len(flows)
	}
	if err := e.requireDesignation(); err != nil {
		return &Summary{Err: err, Dir: sessionDir}
	}
	if err := e.EnsureParallelDevices(ctx, workers); err != nil {
		return &Summary{Err: err, Dir: sessionDir}
	}
	e.Logf("Running %d flow(s) across %d worker(s).", len(flows), workers)

	writeProgress(sessionDir, flows, make([]*Result, len(flows)))
	// One build for the whole session; each worker re-hashes it before installing.
	if !e.Opts.SkipBuild && e.Opts.SessionArtifact.Path == "" {
		artifact, err := build.Build(ctx, e.P, e.Exec, sessionDir, e.outputWriter(0, ""))
		if err != nil {
			return &Summary{Err: err, Dir: sessionDir}
		}
		e.Opts.SessionArtifact = artifact
	}

	results, workerFailures := e.executeFlows(ctx, flows, workers, sessionDir, func(ctx context.Context, w int) (*Run, error) {
		run := e.newRun("run", w)
		if err := run.Setup(ctx, true); err != nil {
			writeRunError(run.Dir, err)
			run.Cleanup()
			return nil, err
		}
		_ = os.Symlink(run.Dir, filepath.Join(sessionDir, fmt.Sprintf("worker-%d", w)))
		return run, nil
	})
	stripArtifacts(sessionDir)

	s := NewSummary(results, time.Since(start), sessionDir)
	if len(workerFailures) > 0 {
		_ = os.WriteFile(filepath.Join(sessionDir, "worker-failures.txt"), []byte(strings.Join(workerFailures, "\n")+"\n"), 0o644)
	}
	status := "PASS\n"
	if s.Failed > 0 || len(workerFailures) > 0 {
		status = "FAILED: worker failure or incomplete/failed flows.\n"
	}
	_ = os.WriteFile(filepath.Join(sessionDir, "status.txt"), []byte(status), 0o644)
	e.persist(s, sessionDir)
	return s
}

// persist writes result.tsv, last-run and junit.xml.
func (e *Engine) persist(s *Summary, dir string) {
	if dir == "" {
		return
	}
	for _, r := range s.Results {
		if r.Platform == "" {
			r.Platform = e.Opts.Platform
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "meta|%s|%d\n", time.Now().UTC().Format(time.RFC3339), int(s.Total.Seconds()))
	for _, r := range s.Results {
		b.WriteString("result|" + r.TSV() + "\n")
	}
	_ = fsx.WriteAtomic(filepath.Join(dir, "result.tsv"), []byte(b.String()))
	last := e.P.LastRunPath()
	if !e.Opts.managed {
		_ = fsx.WriteAtomic(last, []byte(b.String()))
		_ = fsx.WriteAtomic(filepath.Join(filepath.Dir(last), "last-run.link"), []byte(filepath.Base(dir)+"\n"))
	}
	writeArtifacts(dir, s.Results)
	junit := JUnit(s, e.P.Root)
	_ = fsx.WriteAtomic(filepath.Join(dir, "junit.xml"), junit)
	if !e.Opts.managed && e.Opts.JUnitPath != "" {
		if err := fsx.WriteAtomic(e.Opts.JUnitPath, junit); err != nil {
			e.Warnf("could not write JUnit report: %v", err)
		}
	}
}

// JUnit renders a summary as a JUnit XML report, one testcase per flow.
func JUnit(s *Summary, root string) []byte {
	type failure struct {
		Message string `xml:"message,attr"`
		Type    string `xml:"type,attr"`
		Body    string `xml:",chardata"`
	}
	type testcase struct {
		Name      string    `xml:"name,attr"`
		Classname string    `xml:"classname,attr"`
		File      string    `xml:"file,attr,omitempty"`
		Time      string    `xml:"time,attr"`
		Failure   *failure  `xml:"failure,omitempty"`
		Skipped   *struct{} `xml:"skipped,omitempty"`
		SystemOut string    `xml:"system-out,omitempty"`
	}
	type suite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Skipped  int        `xml:"skipped,attr"`
		Time     string     `xml:"time,attr"`
		Stamp    string     `xml:"timestamp,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	st := suite{Name: "maestro", Tests: len(s.Results), Time: fmt.Sprintf("%.1f", s.Total.Seconds()), Stamp: time.Now().UTC().Format(time.RFC3339)}
	for _, r := range s.Results {
		tc := testcase{Name: strings.TrimSuffix(r.File, ".yaml"), Classname: strings.TrimPrefix(r.Platform+"."+r.Feature, "."), Time: fmt.Sprintf("%.1f", r.Duration.Seconds())}
		if r.Path != "" {
			if rel, err := filepath.Rel(root, r.Path); err == nil {
				tc.File = rel
			}
		}
		var out []string
		if r.LogPath != "" {
			out = append(out, "log: "+r.LogPath)
		}
		if r.Recording != "" {
			out = append(out, "recording: "+r.Recording)
		}
		if r.Artifacts != "" {
			out = append(out, "artifacts: "+r.Artifacts)
		}
		// Jenkins/JUnit-attachments convention; most test-management importers understand it.
		for _, shot := range r.Shots {
			out = append(out, "[[ATTACHMENT|"+shot+"]]")
		}
		for _, u := range r.Unmatched {
			out = append(out, "unstubbed: "+u)
		}
		tc.SystemOut = strings.Join(out, "\n")
		switch {
		case r.Passed():
		case r.Stage == "UNSTARTED":
			tc.Skipped = &struct{}{}
			st.Skipped++
		default:
			var body []string
			if r.Message != "" {
				body = append(body, r.Message)
			}
			for _, v := range r.Verifs {
				if !v.Pass {
					body = append(body, fmt.Sprintf("%s: expected %s, got %d", v.Stub, v.Expectation, v.Got))
				}
			}
			tc.Failure = &failure{Message: fmt.Sprintf("%s (exit %d)", r.Stage, r.Exit), Type: r.Stage, Body: strings.Join(body, "\n")}
			st.Failures++
		}
		st.Cases = append(st.Cases, tc)
	}
	b, _ := xml.MarshalIndent(st, "", "  ")
	return append([]byte(xml.Header), append(b, '\n')...)
}

// LoadResults reads a result.tsv.
func LoadResults(path string) (time.Time, time.Duration, []*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, 0, nil, err
	}
	var at time.Time
	var total time.Duration
	var results []*Result
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "meta|"); ok {
			ts, secs, _ := strings.Cut(rest, "|")
			at, _ = time.Parse(time.RFC3339, ts)
			n, _ := strconv.Atoi(secs)
			total = time.Duration(n) * time.Second
		} else if rest, ok := strings.CutPrefix(line, "result|"); ok {
			results = append(results, ParseResultTSV(rest))
		}
	}
	dir := filepath.Dir(path)
	if filepath.Base(path) == "last-run.tsv" {
		if link := readLine(filepath.Join(dir, "last-run.link")); link != "" && filepath.Base(link) == link {
			dir = filepath.Join(filepath.Dir(dir), "runs", link)
		}
	}
	var report struct {
		Results []*Result `json:"results"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "run.json")); err == nil && json.Unmarshal(data, &report) == nil && report.Results != nil {
		results = report.Results
	}
	return at, total, results, nil
}

// writeProgress records a run's plan as it goes: finished flows as result lines, the rest as
// plan lines, so History can follow runs started by other processes.
func writeProgress(dir string, flows []*fixtures.Flow, results []*Result) {
	if dir == "" {
		return
	}
	var b strings.Builder
	for i, f := range flows {
		if results[i] != nil {
			b.WriteString("result|" + results[i].TSV() + "\n")
		} else {
			fmt.Fprintf(&b, "plan|%s|%s|%s\n", f.Feature, f.File, f.Rel)
		}
	}
	_ = fsx.WriteAtomic(filepath.Join(dir, "progress.tsv"), []byte(b.String()))
}

// LiveProgress reads a run that has no result.tsv yet: finished flows, the ones in flight as
// RUNNING, the rest PENDING, plus each runner's current stage.
func LiveProgress(dir string) (results []*Result, stages []string) {
	b, _ := os.ReadFile(filepath.Join(dir, "progress.tsv"))
	runners, _ := filepath.Glob(filepath.Join(dir, "worker-*"))
	if len(runners) == 0 {
		runners = []string{dir}
	}
	inFlight := map[string]bool{}
	for _, r := range runners {
		stage := readLine(filepath.Join(r, "stage"))
		flow := readLine(filepath.Join(r, "current-flow"))
		if stage == "" || stage == "DONE" {
			continue
		}
		if stage == "FLOW" {
			inFlight[flow] = true
			inFlight[readLine(filepath.Join(r, "platform"))+"/"+flow] = true
		}
		stages = append(stages, strings.TrimSpace(readLine(filepath.Join(r, "platform"))+" "+stage+" "+flow))
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "result|"); ok {
			results = append(results, ParseResultTSV(rest))
		} else if rest, ok := strings.CutPrefix(line, "plan|"); ok {
			f := strings.Split(rest, "|")
			if len(f) < 3 {
				continue
			}
			stage := "PENDING"
			platform := ""
			if len(f) > 3 {
				platform = f[3]
			}
			key := f[2]
			if platform != "" {
				key = platform + "/" + key
			}
			if inFlight[key] {
				stage = "RUNNING"
			}
			results = append(results, &Result{Platform: platform, Feature: f[0], File: f[1], Stage: stage})
		}
	}
	return results, stages
}

func readLine(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// writeRunError records why a run produced no results (shown by History).
func writeRunError(dir string, err error) {
	if dir != "" && err != nil {
		_ = os.WriteFile(filepath.Join(dir, "error.txt"), []byte(err.Error()+"\n"), 0o644)
	}
}

// RunError reads a run dir's recorded setup error, if any.
func RunError(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "error.txt"))
	return strings.TrimSpace(string(b))
}

// FlowArtifacts is what a run recorded about one flow beyond result.tsv.
type FlowArtifacts struct {
	Platform  string   `json:"platform,omitempty"`
	Feature   string   `json:"feature"`
	File      string   `json:"file"`
	Log       string   `json:"log,omitempty"`
	Artifacts string   `json:"artifacts,omitempty"`
	Shots     []string `json:"screenshots,omitempty"`
}

func writeArtifacts(dir string, results []*Result) {
	var list []FlowArtifacts
	for _, r := range results {
		if r.Artifacts == "" && r.LogPath == "" {
			continue
		}
		list = append(list, FlowArtifacts{Platform: r.Platform, Feature: r.Feature, File: r.File, Log: r.LogPath, Artifacts: r.Artifacts, Shots: r.Shots})
	}
	if len(list) == 0 {
		return
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	_ = fsx.WriteAtomic(filepath.Join(dir, "artifacts.json"), b)
}

// LoadArtifacts reads artifacts.json, keyed by "feature/file".
func LoadArtifacts(dir string) map[string]FlowArtifacts {
	out := map[string]FlowArtifacts{}
	b, err := os.ReadFile(filepath.Join(dir, "artifacts.json"))
	if err != nil {
		return out
	}
	var list []FlowArtifacts
	if json.Unmarshal(b, &list) == nil {
		for _, a := range list {
			out[(&Result{Platform: a.Platform, Feature: a.Feature, File: a.File}).Key()] = a
		}
	}
	return out
}
