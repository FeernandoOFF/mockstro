package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
)

// Maestro runs this from each flow's hooks; it executes on the host, so loopback reaches us.
const batchHookJS = `var r = http.post(MOCKSTRO_URL + '/' + MOCKSTRO_ACTION + '?flow=' + encodeURIComponent(MOCKSTRO_FLOW), {body: ''})
if (r.status !== 200) throw new Error('mockstro ' + MOCKSTRO_ACTION + ': ' + r.body)
`

// batchPrefix marks the instrumented copy of a flow. The plain copy keeps the original name,
// so a flow another flow calls with runFlow runs without mockstro's hooks.
const batchPrefix = ".mockstro-batch-"

// batch is one Maestro process running several flows. Per-flow setup and checks happen in
// the flows' onFlowStart/onFlowComplete hooks, which call back into ServeHTTP.
type batch struct {
	r       *Run
	ctx     context.Context
	stop    context.CancelFunc
	stopped func() bool // another worker hit --fail-fast
	flows   []*fixtures.Flow
	index   map[string]int
	done    func(i int, res *Result)

	mu        sync.Mutex // flow state; held while a hook runs
	cur       *flowRun
	curIdx    int
	passed    bool
	setupFail bool
	started   []bool
	results   []*Result
	halted    bool
	seen      map[string]bool // Maestro output dirs already assigned
	pending   *flowRun        // finished, waiting for Maestro to write its output dir
	pendIdx   int
	tail      *logTail

	outMu   sync.Mutex // output routing; separate so output never waits on a hook
	outFlow string
	outLog  *os.File
}

// RunBatch runs flows in one Maestro process, reporting each result through done. It reports
// whether --fail-fast stopped the batch; flows Maestro never reached because of that or a
// cancellation get no result. stopped, when set, is checked before each flow starts.
func (r *Run) RunBatch(ctx context.Context, flows []*fixtures.Flow, stopped func() bool, done func(i int, res *Result)) (halted bool) {
	e := r.e
	bctx, stop := context.WithCancel(ctx)
	defer stop()
	b := &batch{r: r, ctx: ctx, stop: stop, stopped: stopped, flows: flows, index: map[string]int{}, done: done,
		started: make([]bool, len(flows)), results: make([]*Result, len(flows)), seen: r.resultDirs()}
	fail := func(err error) bool {
		for i, f := range flows {
			fr := r.beginFlow(f)
			fr.res.Message = err.Error()
			done(i, r.finishFlow(fr, "SETUP", 1))
		}
		return e.Opts.FailFast
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(fmt.Errorf("batch control server: %w", err))
	}
	srv := &http.Server{Handler: b}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	script := filepath.Join(r.flowsRoot, ".mockstro-hook.js")
	_ = os.MkdirAll(r.flowsRoot, 0o755)
	if err := os.WriteFile(script, []byte(batchHookJS), 0o644); err != nil {
		return fail(err)
	}
	config, err := r.batchConfig()
	if err != nil {
		return fail(err)
	}
	url := "http://" + ln.Addr().String()
	var paths []string
	for i, f := range flows {
		b.index[f.Rel] = i
		path, err := r.sanitizeBatch(f, fixtures.BatchHooks{Script: script, URL: url, Flow: f.Rel})
		if err != nil {
			return fail(fmt.Errorf("sanitize %s: %w", f.Rel, err))
		}
		paths = append(paths, path)
	}

	logDir := filepath.Join(r.Dir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	batchLog := filepath.Join(logDir, fmt.Sprintf("batch-%s.log", NewRunID()))
	logFile, _ := os.Create(batchLog)
	lw := &lineWriter{tee: logFile, emit: b.output}

	args := []string{}
	if r.Device != "" {
		args = append(args, "--device", r.Device)
	}
	debugDir := filepath.Join(r.Dir, "maestro-debug", strings.TrimSuffix(filepath.Base(batchLog), ".log"))
	args = append(args, "test", "--config", config, "--debug-output", debugDir)
	args = append(args, paths...)
	r.logf("Running %d flow(s) in one Maestro session", len(flows))
	// With several flows Maestro prints only a summary; its debug log still has every step.
	b.tail = &logTail{dir: debugDir, w: &lineWriter{emit: b.step}}
	tailStop := make(chan struct{})
	tailDone := make(chan struct{})
	go func() { b.tail.follow(tailStop); close(tailDone) }()
	runErr := e.Exec.Run(bctx, execx.Cmd{Name: "maestro", Args: args, Stdout: lw, Stderr: lw})
	close(tailStop)
	<-tailDone
	b.tail.close()
	lw.Flush()
	if logFile != nil {
		logFile.Close()
	}
	b.setOutput("", nil)

	b.mu.Lock()
	defer b.mu.Unlock()
	exit := execx.ExitCode(runErr)
	if exit <= 0 {
		exit = 1
	}
	if b.cur != nil {
		fr := b.cur
		if ctx.Err() != nil {
			b.settle(fr, "CANCELLED", 130)
		} else {
			fr.res.Message = "Maestro exited before the flow completed; see " + e.P.Rel(batchLog)
			b.settle(fr, "TEST", exit)
		}
	}
	b.flush()
	// Maestro can stop before a flow starts (e.g. it failed to parse one of them).
	if ctx.Err() == nil && !b.halted {
		for i, f := range flows {
			if b.started[i] {
				continue
			}
			fr := r.beginFlow(f)
			fr.res.LogPath = batchLog
			fr.res.Message = "Maestro exited before the flow started; see " + e.P.Rel(batchLog)
			b.results[i] = r.finishFlow(fr, "TEST", exit)
			done(i, b.results[i])
		}
	}
	return b.halted
}

// batchConfig copies the run's Maestro config without the keys that select or order flows:
// with several files Maestro applies them, and mockstro has already chosen what runs.
func (r *Run) batchConfig() (string, error) {
	cfg := map[string]any{}
	if data, err := os.ReadFile(r.configPath); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("%s: %w", r.configPath, err)
		}
	}
	for _, k := range []string{"flows", "includeTags", "excludeTags", "executionOrder"} {
		delete(cfg, k)
	}
	if _, ok := cfg["testOutputDir"]; !ok {
		cfg["testOutputDir"] = filepath.Join(r.Dir, "results")
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(r.Dir, "batch-config.yaml")
	return path, os.WriteFile(path, out, 0o644)
}

func (r *Run) sanitizeBatch(f *fixtures.Flow, h fixtures.BatchHooks) (string, error) {
	plain, err := r.sanitize(f)
	if err != nil {
		return "", err
	}
	data, err := fixtures.BatchFlow(f.Path, r.e.flowVariables(f, r.Port), []string{"APP_ID", "WIREMOCK_PORT"}, h)
	if err != nil {
		return "", err
	}
	out := filepath.Join(filepath.Dir(plain), batchPrefix+filepath.Base(plain))
	return out, os.WriteFile(out, data, 0o644)
}

func (b *batch) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rel := req.URL.Query().Get("flow")
	i, ok := b.index[rel]
	if !ok {
		http.Error(w, "unknown flow "+rel, http.StatusNotFound)
		return
	}
	action := strings.TrimPrefix(req.URL.Path, "/")
	if action != "passed" {
		b.tail.drain()
	}
	switch action {
	case "start":
		// Maestro creates a flow's output dir just before its onFlowStart hooks.
		dirs := b.fresh()
		if b.cur != nil {
			// The previous flow's completion hook never ran (e.g. its own onFlowComplete failed).
			b.cur.res.Message = "flow ended without running mockstro's completion hook"
			b.r.finishFlow(b.cur, "TEST", 1)
			b.hold(b.cur)
		}
		b.flush()
		if b.stopped != nil && b.stopped() {
			b.halted = true
			b.stop()
		}
		if b.halted || b.started[i] {
			http.Error(w, "batch stopped or flow already started", http.StatusConflict)
			return
		}
		fr := b.r.beginFlow(b.flows[i])
		fr.held = true
		attach(fr, dirs)
		b.cur, b.curIdx, b.passed, b.setupFail, b.started[i] = fr, i, false, false, true
		log, _ := os.Create(fr.res.LogPath)
		b.setOutput(rel, log)
		b.r.logf("Running %s/%s [%s]", fr.f.Feature, fr.f.File, fr.profile)
		if b.r.activateFlow(b.ctx, fr) != nil {
			b.setupFail = true
			http.Error(w, fr.res.Message, http.StatusInternalServerError)
			return
		}
	case "passed":
		if b.cur != nil && b.curIdx == i {
			b.passed = true
		}
	case "complete":
		if b.cur == nil || b.curIdx != i {
			break
		}
		fr := b.cur
		b.setOutput("", nil)
		switch {
		case b.setupFail:
			b.settle(fr, "SETUP", 1)
		case b.passed:
			b.r.concludeFlow(b.ctx, fr, "TEST", 0)
			b.hold(fr)
		default:
			b.r.concludeFlow(b.ctx, fr, "TEST", 1)
			b.hold(fr)
		}
	default:
		http.NotFound(w, req)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// settle finishes the current flow; its result is published by the next flush.
func (b *batch) settle(fr *flowRun, stage string, exit int) {
	b.r.finishFlow(fr, stage, exit)
	b.hold(fr)
}

func (b *batch) hold(fr *flowRun) {
	b.setOutput("", nil)
	attach(fr, b.fresh())
	b.cur = nil
	b.pending, b.pendIdx = fr, b.curIdx
	if !fr.res.Passed() && fr.res.Stage != "CANCELLED" && b.r.e.Opts.FailFast && !b.halted {
		b.halted = true
		b.r.logf("Stopping after %s/%s failed.", fr.f.Feature, fr.f.File)
		b.stop()
	}
}

// flush publishes the held result, now that Maestro is done writing its output dir.
func (b *batch) flush() {
	fr := b.pending
	if fr == nil {
		return
	}
	b.pending = nil
	if fr.res.Artifacts != "" {
		fr.res.Shots, _ = filepath.Glob(filepath.Join(fr.res.Artifacts, "screenshots", "*.png"))
		sort.Strings(fr.res.Shots)
	}
	b.r.publishFlow(fr)
	b.results[b.pendIdx] = fr.res
	b.done(b.pendIdx, fr.res)
}

// fresh returns Maestro output dirs that appeared since the last call.
func (b *batch) fresh() []string {
	var out []string
	for d := range b.r.resultDirs() {
		if !b.seen[d] {
			b.seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func attach(fr *flowRun, dirs []string) {
	if fr.res.Artifacts == "" && len(dirs) > 0 {
		fr.res.Artifacts = dirs[0]
	}
}

var stepLine = regexp.MustCompile(`CliConsoleListener\.onCommandFinished: (.*) (COMPLETED|FAILED|SKIPPED|WARNED)$`)

// step turns a maestro.log line into the step line Maestro prints for a single flow.
func (b *batch) step(line string) {
	m := stepLine.FindStringSubmatch(line)
	if m == nil || strings.Contains(m[1], ".mockstro-hook.js") ||
		m[1] == "Define variables" || m[1] == "Apply configuration" {
		return
	}
	b.output(m[1] + "... " + m[2])
}

// logTail follows the maestro.log Maestro writes under dir. Hooks drain it before switching
// flows, so every step line lands in its own flow's log.
type logTail struct {
	mu  sync.Mutex
	dir string
	f   *os.File
	w   *lineWriter
}

func (t *logTail) drain() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		logs, _ := filepath.Glob(filepath.Join(t.dir, ".maestro", "tests", "*", "maestro.log"))
		if len(logs) == 0 {
			return
		}
		t.f, _ = os.Open(logs[0])
	}
	if t.f != nil {
		_, _ = io.Copy(t.w, t.f)
	}
}

func (t *logTail) follow(stop <-chan struct{}) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		t.drain()
		select {
		case <-stop:
			t.drain()
			return
		case <-tick.C:
		}
	}
}

func (t *logTail) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.w.Flush()
	if t.f != nil {
		t.f.Close()
	}
}

func (b *batch) output(line string) {
	b.outMu.Lock()
	flow, log := b.outFlow, b.outLog
	if log != nil {
		_, _ = io.WriteString(log, line+"\n")
	}
	b.outMu.Unlock()
	b.r.e.emit(Event{Kind: EvOutput, Worker: b.r.Worker, RunID: b.r.ID, Flow: flow, Text: line})
}

func (b *batch) setOutput(flow string, log *os.File) {
	b.outMu.Lock()
	if b.outLog != nil {
		b.outLog.Close()
	}
	b.outFlow, b.outLog = flow, log
	b.outMu.Unlock()
}
