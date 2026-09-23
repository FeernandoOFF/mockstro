package tui

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
)

type runDoneMsg struct{ s *engine.Summary }

const maxLines = 4000

type workerState struct {
	id               int
	runID, device    string
	port             int
	stage, flow, dir string
	flowStart        time.Time
	flowLine         int // absolute index of the current flow's first output line
	lines            []string
	dropped          int // lines trimmed from the front; absolute index = dropped + i
	passed, failed   int
}

func (w *workerState) add(line string) {
	w.lines = append(w.lines, line)
	if len(w.lines) > maxLines {
		n := len(w.lines) - maxLines
		w.lines = w.lines[n:]
		w.dropped += n
	}
}

func (w *workerState) next() int { return w.dropped + len(w.lines) }

// outputMark is where a result's output starts: worker and absolute line.
type outputMark struct{ worker, line int }

// runModel follows a session live: workers, per-flow results and each worker's output.
type runModel struct {
	a         *app
	opts      engine.Options
	total     int
	started   time.Time
	cancel    context.CancelFunc
	done      chan struct{}
	workers   map[int]*workerState
	results   []*engine.Result
	summary   *engine.Summary
	selWorker int
	scroll    int // lines up from the bottom (following the tail)
	anchor    int // absolute line shown at the top; -1 follows the tail
	marks     map[*engine.Result]outputMark
	confirm   bool
	resCursor int
	focusLog  bool // arrows scroll the output instead of picking results
}

func (r *runModel) running() bool { return r.done != nil && r.summary == nil }

func (r *runModel) worker(id int) *workerState {
	if r.workers == nil {
		r.workers = map[int]*workerState{}
	}
	w, ok := r.workers[id]
	if !ok {
		w = &workerState{id: id}
		r.workers[id] = w
	}
	return w
}

func (r *runModel) start(o engine.Options, total int) tea.Cmd {
	a := r.a
	*r = runModel{a: a, opts: o, total: total, started: time.Now(), done: make(chan struct{}), anchor: -1, marks: map[*engine.Result]outputMark{}}
	r.worker(0)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	e := engine.New(a.p, o, execx.Real{}, func(ev engine.Event) { a.prog.Send(evMsg(ev)) })
	done := r.done
	go func() {
		defer close(done)
		send := func(s *engine.Summary) { a.prog.Send(runDoneMsg{s}) }
		if err := e.CheckEnvironment("run"); err != nil {
			send(&engine.Summary{Err: err})
			return
		}
		if _, err := e.Store.ValidateAll(true); err != nil {
			send(&engine.Summary{Err: err})
			return
		}
		if _, err := e.EnsureJar(ctx); err != nil {
			send(&engine.Summary{Err: err})
			return
		}
		send(e.Run(ctx))
	}()
	a.log("info", "run started: %s", commandLine(o))
	return nil
}

func (r *runModel) cancelAndWait() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.done != nil {
		select {
		case <-r.done:
		case <-time.After(90 * time.Second):
		}
	}
}

func (r *runModel) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case runDoneMsg:
		r.summary = m.s
		if m.s.Err != nil {
			r.worker(0).add(sBad.Render("error: " + m.s.Err.Error()))
			r.a.log("error", "run failed: %v", m.s.Err)
		} else {
			r.a.log("info", "run finished: %d pass / %d fail in %s", m.s.Passed, m.s.Failed, m.s.Total.Round(time.Second))
			r.results = m.s.Results
		}
		r.a.hist.dirty = true
		return r.a.refreshStatus()
	case evMsg:
		ev := engine.Event(m)
		w := r.worker(ev.Worker)
		switch ev.Kind {
		case engine.EvLog:
			line := ev.Text
			switch ev.Level {
			case "warn":
				line = sWarn.Render(line)
			case "error":
				line = sBad.Render(line)
			}
			for _, l := range strings.Split(line, "\n") {
				w.add(l)
			}
		case engine.EvOutput:
			w.add(ev.Text)
		case engine.EvStage:
			w.stage, w.runID = ev.Stage, ev.RunID
			if ev.Device != "" {
				w.device = ev.Device
			}
			if ev.Port != 0 {
				w.port = ev.Port
			}
			if ev.Stage != "FLOW" {
				w.flow = ""
			}
		case engine.EvWorker:
			w.device, w.port, w.dir, w.runID = ev.Device, ev.Port, ev.Text, ev.RunID
		case engine.EvFlowStart:
			w.flow, w.flowStart, w.flowLine = ev.Flow, time.Now(), w.next()
			w.add(sTitle.Render("▶ " + ev.Flow + "  [" + ev.Text + "]"))
		case engine.EvFlowResult:
			res := ev.Result
			r.results = append(r.results, res)
			if r.marks != nil {
				r.marks[res] = outputMark{ev.Worker, w.flowLine}
			}
			if res.Passed() {
				w.passed++
				w.add(sOK.Render(fmt.Sprintf("✔ %s/%s (%ds)", res.Feature, res.File, int(res.Duration.Seconds()))))
			} else {
				w.failed++
				w.add(sBad.Render(fmt.Sprintf("✘ %s/%s — %s (%ds)", res.Feature, res.File, res.Stage, int(res.Duration.Seconds()))))
			}
			w.flow = ""
		}
	}
	return nil
}

func (r *runModel) workerIDs() []int {
	var ids []int
	for id := range r.workers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (r *runModel) key(k tea.KeyMsg) tea.Cmd {
	if r.confirm {
		r.confirm = false
		if k.String() == "y" || k.String() == "Y" {
			r.a.log("warn", "cancelling run…")
			if r.cancel != nil {
				r.cancel()
			}
		}
		return nil
	}
	ids := r.workerIDs()
	switch k.String() {
	case "c":
		if r.running() {
			r.confirm = true
		}
	case "left", "[":
		for i, id := range ids {
			if id == r.selWorker && i > 0 {
				r.selWorker, r.scroll, r.anchor = ids[i-1], 0, -1
				break
			}
		}
	case "right", "]":
		for i, id := range ids {
			if id == r.selWorker && i < len(ids)-1 {
				r.selWorker, r.scroll, r.anchor = ids[i+1], 0, -1
				break
			}
		}
	case "up", "k":
		if r.focusLog {
			r.scrollLog(-1)
		} else if r.resCursor > 0 {
			r.resCursor--
			r.jumpToResult()
		}
	case "down", "j":
		if r.focusLog {
			r.scrollLog(1)
		} else if r.resCursor < len(r.results)-1 {
			r.resCursor++
			r.jumpToResult()
		}
	case "pgup":
		r.scrollLog(-20)
	case "pgdown":
		r.scrollLog(20)
	case "home", "g":
		r.resCursor = 0
		r.jumpToResult()
	case "G", "end":
		r.anchor, r.scroll = -1, 0
	case "f":
		r.focusLog = !r.focusLog
	case "enter":
		r.jumpToResult()
	case "s":
		if r.resCursor < len(r.results) {
			openShot(r.a, r.results[r.resCursor])
		}
	case "A":
		if r.resCursor < len(r.results) {
			openArtifacts(r.a, r.results[r.resCursor])
		}
	case "o":
		dir := ""
		if r.summary != nil {
			dir = r.summary.Dir
		}
		if w := r.workers[r.selWorker]; dir == "" && w != nil {
			dir = w.dir
		}
		if dir != "" {
			_ = exec.Command("open", dir).Start()
		}
	case "F":
		if r.summary == nil || r.running() {
			return nil
		}
		var failed []*fixtures.Flow
		for _, res := range r.results {
			if res.Passed() {
				continue
			}
			for _, f := range r.a.flows {
				if f.Path == res.Path || (f.Feature == res.Feature && f.File == res.File) {
					failed = append(failed, f)
					break
				}
			}
		}
		if len(failed) > 0 {
			r.a.tab = tabFlows
			r.a.cfg.open(failed, "", true)
		}
	}
	return nil
}

// jumpToResult shows the selected result's worker output from where that flow started.
func (r *runModel) jumpToResult() {
	if r.resCursor >= len(r.results) {
		return
	}
	m, ok := r.marks[r.results[r.resCursor]]
	if !ok {
		return
	}
	r.selWorker, r.anchor, r.scroll = m.worker, m.line, 0
}

// scrollLog moves the view by delta lines (negative = up), leaving tail-follow when needed.
func (r *runModel) scrollLog(delta int) {
	wk := r.workers[r.selWorker]
	if wk == nil {
		return
	}
	if r.anchor < 0 {
		r.scroll = max(0, r.scroll-delta)
		return
	}
	r.anchor = min(max(wk.dropped, r.anchor+delta), wk.next()-1)
}

func (r *runModel) view(w, h int) (string, string) {
	if r.done == nil {
		return panel("Run", sMuted.Render("No run yet. Pick flows in tab 2 and press enter."), w, h), "2 to pick flows"
	}
	// Header.
	state := sWarn.Render("running")
	if r.summary != nil {
		switch {
		case r.summary.Err != nil:
			state = sBad.Render("error")
		case r.summary.Cancelled:
			state = sWarn.Render("cancelled")
		case r.summary.Failed > 0:
			state = sBad.Render(fmt.Sprintf("failed %d/%d", r.summary.Failed, len(r.summary.Results)))
		default:
			state = sOK.Render("passed")
		}
	}
	passed, failed := 0, 0
	for _, res := range r.results {
		if res.Passed() {
			passed++
		} else {
			failed++
		}
	}
	elapsed := time.Since(r.started).Round(time.Second)
	if r.summary != nil {
		elapsed = r.summary.Total.Round(time.Second)
	}
	header := fmt.Sprintf(" %s  %d/%d done · %s · %s  %s", state, len(r.results), r.total,
		sOK.Render(fmt.Sprintf("%d pass", passed)), sBad.Render(fmt.Sprintf("%d fail", failed)), sMuted.Render(elapsed.String()))

	// Workers.
	var wb strings.Builder
	for _, id := range r.workerIDs() {
		wk := r.workers[id]
		if id == 0 && len(r.workers) > 1 && wk.device == "" {
			continue
		}
		name := "session"
		if id > 0 {
			name = fmt.Sprintf("worker %d", id)
		} else if wk.device != "" {
			name = "run"
		}
		cur := "  "
		if id == r.selWorker {
			cur = sSel.Render("› ")
			name = sSel.Render(name)
		}
		flow := wk.flow
		if flow != "" && !wk.flowStart.IsZero() {
			flow += sMuted.Render(fmt.Sprintf(" %ds", int(time.Since(wk.flowStart).Seconds())))
		}
		fmt.Fprintf(&wb, "%s%s %s :%d %s %s\n", cur, pad(name, 9), pad(d2(wk.device), 14), wk.port, pad(sWarn.Render(d2(wk.stage)), 9), flow)
	}
	workersH := min(len(r.workers)+3, 8)

	// Results list.
	var rb strings.Builder
	resH := h - workersH - 1
	rs, re := window(len(r.results), r.resCursor, resH-3)
	for i := rs; i < re; i++ {
		res := r.results[i]
		cur := "  "
		name := truncate(res.Feature+"/"+res.File, 40)
		if i == r.resCursor {
			cur = sSel.Render("› ")
			if !r.focusLog {
				name = sSel.Render(name)
			}
		}
		fmt.Fprintf(&rb, "%s%s %s%s\n", cur, statusStyle(res.Passed(), res.Stage), name, shotBadge(res))
		if !res.Passed() && res.Stage != "TEST" && res.Stage != "UNSTARTED" {
			fmt.Fprintf(&rb, "     %s\n", sMuted.Render(res.Stage))
		}
	}
	if r.summary != nil && r.summary.Dir != "" {
		rb.WriteString("\n" + sMuted.Render(r.a.p.Rel(r.summary.Dir)))
	}

	// Log.
	logW := w - 50
	wk := r.workers[r.selWorker]
	var lines []string
	if wk != nil {
		lines = wk.lines
	}
	logH := resH - 3
	var start, end int
	if r.anchor >= 0 && wk != nil {
		start = min(max(0, r.anchor-wk.dropped), len(lines))
		end = min(len(lines), start+logH)
	} else {
		end = max(0, len(lines)-r.scroll)
		start = max(0, end-logH)
	}
	logTitle := "Output"
	if r.selWorker > 0 {
		logTitle = fmt.Sprintf("Output · worker %d", r.selWorker)
	}
	switch {
	case r.anchor >= 0:
		logTitle += sMuted.Render(" (pinned · G to follow)")
	case r.scroll > 0:
		logTitle += sMuted.Render(fmt.Sprintf(" (scrolled %d · G to follow)", r.scroll))
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		panel("Workers", wb.String(), w, workersH),
		lipgloss.JoinHorizontal(lipgloss.Top,
			panel("Results", rb.String(), 50, resH),
			panel(logTitle, strings.Join(lines[start:end], "\n"), logW, resH)))
	arrows := "↑↓ result → output"
	if r.focusLog {
		arrows = "↑↓ scroll output"
	}
	var sel *engine.Result
	if r.resCursor < len(r.results) {
		sel = r.results[r.resCursor]
	}
	help := arrows + " · f toggle results/output · G follow · ←→ worker · c cancel" + artifactKeys(sel) + " · o open dir"
	if r.summary != nil {
		help = arrows + " · f toggle results/output · G follow · ←→ worker · F re-run failed" + artifactKeys(sel) + " · o open dir"
	}
	if r.confirm {
		help = sWarn.Render("Cancel the run? Devices and locks are cleaned up. [y/N]")
	}
	return body, help
}

// start2ForTest puts the model into a running state without an engine.
func (r *runModel) start2ForTest() {
	*r = runModel{a: r.a, started: time.Now(), done: make(chan struct{}), anchor: -1, marks: map[*engine.Result]outputMark{}}
}
