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

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/fsx"
)

// Run executes the selected flows, single-device or parallel, and always returns a summary
// (possibly with Err set) once every owned resource has been cleaned up.
func (e *Engine) Run(ctx context.Context) *Summary {
	start := time.Now()
	if !e.Opts.managed {
		if removed, freed := e.Prune(ctx, KeepRuns()); removed > 0 || freed > 0 {
			e.Logf("Pruned %d old run dir(s), freed %s (keeping %d; MOCKSTRO_KEEP_RUNS to change).", removed, HumanSize(freed), KeepRuns())
		}
	}
	flows, err := e.DiscoverFlows()
	if err != nil {
		return e.done(&Summary{Err: err})
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
	var results []*Result
	done := make([]*Result, len(flows))
	writeProgress(run.Dir, flows, done)
	if e.batched() {
		run.RunBatch(ctx, flows, nil, func(i int, res *Result) {
			done[i] = res
			writeProgress(run.Dir, flows, done)
		})
		for _, res := range done {
			if res != nil {
				results = append(results, res)
			}
		}
	} else {
		for i, f := range flows {
			res := run.RunFlow(ctx, f)
			results = append(results, res)
			done[i] = res
			writeProgress(run.Dir, flows, done)
			if res.Stage == "CANCELLED" {
				break
			}
			if !res.Passed() && e.Opts.FailFast {
				e.Logf("Stopping after %s/%s failed.", f.Feature, f.File)
				break
			}
		}
	}
	if ctx.Err() == nil && e.Opts.Record && e.Opts.Optimize {
		e.OptimizeRecordings(ctx, results)
	}
	s := NewSummary(results, time.Since(start), run.Dir)
	e.persist(s, run.Dir)
	return s
}

// Parallel: workers are goroutines, each a full Run with its own device, port and WireMock.
// Flows are claimed one at a time because durations vary a lot, in `order` (nil = as listed).
type queue struct {
	mu      sync.Mutex
	flows   []*fixtures.Flow
	order   []int
	next    int
	claimed []bool
	results []*Result
	stop    bool
	dir     string // session dir that progress.tsv is written to
	workers int
}

func (q *queue) claim() (int, *fixtures.Flow, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stop || q.next >= len(q.flows) {
		return 0, nil, false
	}
	i := q.next
	if q.order != nil {
		i = q.order[q.next]
	}
	q.next++
	q.claimed[i] = true
	return i, q.flows[i], true
}

// claimBatch claims up to half a worker's fair share of what is left, so each Maestro
// startup is shared by several flows while workers still finish close together.
func (q *queue) claimBatch(workers int) []int {
	q.mu.Lock()
	left := len(q.flows) - q.next
	q.mu.Unlock()
	n := max(1, (left+2*workers-1)/(2*workers))
	var out []int
	for range n {
		i, _, ok := q.claim()
		if !ok {
			break
		}
		out = append(out, i)
	}
	return out
}

func (q *queue) put(i int, r *Result) {
	q.mu.Lock()
	q.results[i] = r
	writeProgress(q.dir, q.flows, q.results)
	q.mu.Unlock()
}

// collect walks the queue's items, not its results: missing work is reported, never hidden.
func (q *queue) collect() []*Result {
	q.mu.Lock()
	defer q.mu.Unlock()
	var results []*Result
	for i, f := range q.flows {
		r := q.results[i]
		if r == nil {
			stage := "UNSTARTED"
			if q.claimed[i] {
				stage = "INTERRUPTED"
			}
			r = &Result{Feature: f.Feature, File: f.File, Path: f.Path, Stage: stage, Exit: 1}
		}
		results = append(results, r)
	}
	return results
}

func (q *queue) halted() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stop
}

// unclaim marks a claimed flow as never started, so it is reported UNSTARTED.
func (q *queue) unclaim(i int) {
	q.mu.Lock()
	q.claimed[i] = false
	q.mu.Unlock()
}

func (q *queue) halt() {
	q.mu.Lock()
	q.stop = true
	q.mu.Unlock()
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
	if !e.Opts.SkipBuild && e.Opts.SessionAPK == "" {
		path, sha, err := e.Plat.Build(ctx, sessionDir, e.outputWriter(0, ""))
		if err != nil {
			return &Summary{Err: err, Dir: sessionDir}
		}
		e.Opts.SessionAPK, e.Opts.SessionAPKSHA = path, sha
	}

	q := &queue{flows: flows, claimed: make([]bool, len(flows)), results: make([]*Result, len(flows)), dir: sessionDir, workers: workers}
	if workers > 1 {
		order, known := longestFirst(flows, e.FlowEstimates(ctx))
		if known > 0 {
			q.order = order
			e.Logf("Scheduling longest flows first (%d/%d with past durations).", known, len(flows))
		}
	}
	var wg sync.WaitGroup
	var failMu sync.Mutex
	var workerFailures []string
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			if err := e.worker(ctx, w, q, sessionDir); err != nil {
				failMu.Lock()
				workerFailures = append(workerFailures, fmt.Sprintf("worker %d: %v", w, err))
				failMu.Unlock()
				e.emit(Event{Kind: EvLog, Level: "error", Worker: w, Text: err.Error()})
			}
		}(w)
		// Stagger slightly so workers do not race for the same device on the same tick.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	wg.Wait()
	stripArtifacts(sessionDir)

	s := NewSummary(q.collect(), time.Since(start), sessionDir)
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

func (e *Engine) worker(ctx context.Context, w int, q *queue, sessionDir string) error {
	run := e.newRun("run", w)
	defer run.Cleanup()
	if err := run.Setup(ctx, true); err != nil {
		writeRunError(run.Dir, err)
		return err
	}
	_ = os.Symlink(run.Dir, filepath.Join(sessionDir, fmt.Sprintf("worker-%d", w)))
	for ctx.Err() == nil && e.batched() {
		idx := q.claimBatch(q.workers)
		if len(idx) == 0 {
			return nil
		}
		chunk := make([]*fixtures.Flow, len(idx))
		for j, i := range idx {
			chunk[j] = q.flows[i]
		}
		got := make([]bool, len(idx))
		halted := run.RunBatch(ctx, chunk, q.halted, func(j int, res *Result) {
			got[j] = true
			q.put(idx[j], res)
		})
		for j, i := range idx {
			if !got[j] {
				q.unclaim(i)
			}
		}
		if halted {
			q.halt()
			return nil
		}
	}
	for ctx.Err() == nil && !e.batched() {
		i, f, ok := q.claim()
		if !ok {
			return nil
		}
		res := run.RunFlow(ctx, f)
		q.put(i, res)
		if res.Stage == "CANCELLED" {
			return nil
		}
		if !res.Passed() && e.Opts.FailFast {
			q.halt()
			run.logf("Stopping after %s/%s failed.", f.Feature, f.File)
			return nil
		}
	}
	return nil
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
