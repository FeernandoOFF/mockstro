package maestro

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

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
)

const batchHookJS = `var r = http.post(MOCKSTRO_URL + '/' + MOCKSTRO_ACTION + '?flow=' + encodeURIComponent(MOCKSTRO_FLOW), {body: ''})
if (r.status !== 200) throw new Error('mockstro ' + MOCKSTRO_ACTION + ': ' + r.body)
`

const batchPrefix = ".mockstro-batch-"

type batch struct {
	x          *Executor
	ctx        context.Context
	stop       context.CancelFunc
	flows      []Flow
	hooks      Hooks
	failFast   bool
	index      map[string]int
	mu         sync.Mutex
	cur        int
	passed     bool
	setupErr   error
	started    []bool
	halted     bool
	seen       map[string]bool
	outputRoot string
	curDir     string
	pending    int
	pendingDir string
	tail       *logTail
	outMu      sync.Mutex
	outFlow    string
	outLog     *os.File
}

// Batch shares one Maestro process across flows. All hook state and result publication
// are serialized; caller verification stays outside Maestro's implementation.
func (x *Executor) Batch(ctx context.Context, flows []Flow, hooks Hooks, failFast bool) bool {
	bctx, stop := context.WithCancel(ctx)
	defer stop()
	b := &batch{x: x, ctx: ctx, stop: stop, flows: flows, hooks: hooks, failFast: failFast, index: map[string]int{},
		cur: -1, pending: -1, started: make([]bool, len(flows)), seen: map[string]bool{}}
	fail := func(err error) bool {
		for i := range flows {
			hooks.Initialize(i)
			hooks.Complete(i, Outcome{Exit: 1, Err: err, Setup: true})
			hooks.Publish(i, Artifacts{})
		}
		return failFast
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(fmt.Errorf("batch control server: %w", err))
	}
	srv := &http.Server{Handler: b}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	script := filepath.Join(x.Workspace.FlowsRoot, ".mockstro-hook.js")
	if err := os.MkdirAll(x.Workspace.FlowsRoot, 0755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(script, []byte(batchHookJS), 0644); err != nil {
		return fail(err)
	}
	config, err := x.Workspace.BatchConfig()
	if err != nil {
		return fail(err)
	}
	var paths []string
	for i, f := range flows {
		b.index[f.Source.Rel] = i
		plain, err := x.Workspace.Sanitize(f.Source)
		if err != nil {
			return fail(err)
		}
		data, err := fixtures.BatchFlow(f.Source.Path, f.Variables, []string{"APP_ID", "WIREMOCK_PORT"}, fixtures.BatchHooks{
			Script: script, URL: "http://" + ln.Addr().String(), Flow: f.Source.Rel})
		if err != nil {
			return fail(fmt.Errorf("sanitize %s: %w", f.Source.Rel, err))
		}
		path := filepath.Join(filepath.Dir(plain), batchPrefix+filepath.Base(plain))
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fail(err)
		}
		paths = append(paths, path)
	}
	logDir := filepath.Join(x.Workspace.Dir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fail(err)
	}
	log, err := os.CreateTemp(logDir, "batch-*.log")
	if err != nil {
		return fail(err)
	}
	defer log.Close()
	lw := execx.NewLines(log, b.output)
	var args []string
	if x.Device != "" {
		args = append(args, "--device", x.Device)
	}
	debug := filepath.Join(x.Workspace.Dir, "results", strings.TrimSuffix(filepath.Base(log.Name()), ".log"))
	b.outputRoot = debug
	args = append(args, "test", "--config", config, "--debug-output", debug, "--flatten-debug-output")
	args = append(args, paths...)
	x.log(fmt.Sprintf("Running %d flow(s) in one Maestro session", len(flows)))
	b.tail = &logTail{dir: debug, w: execx.NewLines(nil, b.step)}
	tailStop, tailDone := make(chan struct{}), make(chan struct{})
	go func() { b.tail.follow(tailStop); close(tailDone) }()
	command, err := Command(x.Exec)
	if err == nil {
		err = x.Exec.Run(bctx, execx.Cmd{Name: command, Args: args, Stdout: lw, Stderr: lw})
	}
	close(tailStop)
	<-tailDone
	b.tail.close()
	lw.Flush()
	b.setOutput("", nil)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := outcome(ctx, err)
	if out.Exit <= 0 {
		out.Exit = 1
	}
	if b.cur >= 0 {
		if !out.Cancelled {
			out.Err = fmt.Errorf("Maestro exited before the flow completed; see %s", log.Name())
		}
		b.complete(out)
	}
	b.flush()
	if ctx.Err() == nil && !out.Cancelled && !b.halted {
		for i := range flows {
			if b.started[i] {
				continue
			}
			hooks.Initialize(i)
			hooks.Complete(i, Outcome{Exit: out.Exit, Err: fmt.Errorf("Maestro exited before the flow started; see %s", log.Name()), Unstarted: true})
			hooks.Publish(i, Artifacts{})
		}
	}
	return b.halted || out.Cancelled
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
		dirs := b.fresh()
		if b.cur >= 0 {
			b.complete(Outcome{Exit: 1, Err: fmt.Errorf("flow ended without running mockstro's completion hook")})
		}
		b.flush()
		if b.hooks.Stopped != nil && b.hooks.Stopped() {
			b.halted = true
			b.stop()
		}
		if b.halted || b.started[i] {
			http.Error(w, "batch stopped or flow already started", http.StatusConflict)
			return
		}
		b.cur, b.passed, b.setupErr, b.started[i] = i, false, nil, true
		b.curDir = ""
		if len(dirs) > 0 {
			b.curDir = dirs[0]
		}
		if err := os.MkdirAll(filepath.Dir(b.flows[i].LogPath), 0755); err != nil {
			b.setupErr = err
		}
		log, err := os.Create(b.flows[i].LogPath)
		if err != nil {
			b.setupErr = err
		}
		b.setOutput(rel, log)
		b.hooks.Initialize(i)
		if err := b.hooks.Start(i); err != nil {
			b.setupErr = err
		}
		if b.setupErr != nil {
			http.Error(w, b.setupErr.Error(), http.StatusInternalServerError)
			return
		}
	case "passed":
		if b.cur == i {
			b.passed = true
		}
	case "complete":
		if b.cur != i {
			break
		}
		out := Outcome{}
		if b.setupErr != nil {
			out.Exit, out.Err, out.Setup = 1, b.setupErr, true
		} else if !b.passed {
			out.Exit = 1
		}
		b.complete(out)
	default:
		http.NotFound(w, req)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *batch) complete(out Outcome) {
	b.setOutput("", nil)
	passed := b.hooks.Complete(b.cur, out)
	dirs := b.fresh()
	if b.curDir == "" && len(dirs) > 0 {
		b.curDir = dirs[0]
	}
	b.pending, b.pendingDir = b.cur, b.curDir
	b.cur = -1
	if !passed && !out.Cancelled && b.failFast && !b.halted {
		b.halted = true
		b.stop()
	}
}

func (b *batch) flush() {
	if b.pending < 0 {
		return
	}
	a := Artifacts{}
	if b.pendingDir != "" {
		a = artifacts(b.pendingDir)
	}
	b.hooks.Publish(b.pending, a)
	b.pending = -1
}

func (b *batch) fresh() []string {
	var dirs []string
	for d := range flowDirs(b.outputRoot) {
		if !b.seen[d] {
			b.seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return dirs
}

var stepLine = regexp.MustCompile(`CliConsoleListener\.onCommandFinished: (.*) (COMPLETED|FAILED|SKIPPED|WARNED)$`)

func (b *batch) step(line string) {
	m := stepLine.FindStringSubmatch(line)
	if m == nil || strings.Contains(m[1], ".mockstro-hook.js") || m[1] == "Define variables" || m[1] == "Apply configuration" {
		return
	}
	b.output(m[1] + "... " + m[2])
}

type logTail struct {
	mu  sync.Mutex
	dir string
	f   *os.File
	w   *execx.Lines
}

func (t *logTail) drain() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		t.f, _ = os.Open(filepath.Join(t.dir, "maestro.log"))
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
	b.x.output(flow, line)
}

func (b *batch) setOutput(flow string, log *os.File) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	if b.outLog != nil {
		b.outLog.Close()
	}
	b.outFlow, b.outLog = flow, log
}
