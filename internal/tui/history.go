package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/fixtures"
)

type histRun struct {
	dir     string
	size    int64
	active  bool
	err     string
	rd      engine.RunDir
	at      time.Time
	total   time.Duration
	results []*engine.Result
	passed  int
	failed  int
	stages  []string // live runs: what each runner is doing
}

type histScanMsg []*histRun

func unfinished(r *engine.Result) bool { return r.Stage == "RUNNING" || r.Stage == "PENDING" }

// historyModel lists past runs from .maestro/.runtime/runs.
type historyModel struct {
	a        *app
	runs     []*histRun
	cursor   int
	resCur   int
	focus    int // 0 runs, 1 results
	dirty    bool
	loaded   bool
	preview  []string
	scroll   int
	confirm  string // "delete" | "prune"
	linkedN  int    // files outside the run dir the pending delete will also remove
	linkedB  int64
	total    int64
	picked   map[string]bool
	scanning bool

	showStubs bool // right pane lists the selected flow's stubs instead of its log
	stubsFor  string
	stubs     []stubRef
	stubErr   error
	sCur      int
}

func (hm *historyModel) load() { hm.apply(hm.scan()) }

// scan reads every run dir; it touches no model state so it can run off the UI loop.
func (hm *historyModel) scan() []*histRun {
	var runs []*histRun
	for _, rd := range hm.a.query.RunDirs(context.Background(), true) {
		hr := &histRun{dir: rd.Path, at: rd.ModTime, size: rd.Size, active: rd.Active, err: engine.RunError(rd.Path), rd: rd}
		readResults(hr)
		runs = append(runs, hr)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].at.After(runs[j].at) })
	return runs
}

// readResults loads result.tsv, or for a run still going (or one that died) its progress.
func readResults(hr *histRun) {
	hr.results, hr.stages, hr.passed, hr.failed = nil, nil, 0, 0
	at, total, results, err := engine.LoadResults(filepath.Join(hr.dir, "result.tsv"))
	if err == nil {
		if !at.IsZero() {
			hr.at = at
		}
		hr.total, hr.results = total, results
	} else {
		hr.results, hr.stages = engine.LiveProgress(hr.dir)
		if !hr.active {
			hr.stages = nil
			for _, r := range hr.results {
				if unfinished(r) {
					r.Stage, r.Exit = "UNSTARTED", 1
				}
			}
		}
	}
	arts := engine.LoadArtifacts(hr.dir)
	for _, r := range hr.results {
		r.LogPath = filepath.Join(hr.dir, "logs", engine.FlowSlug(r.Feature, r.File)+".log")
		if fa, ok := arts[r.Key()]; ok {
			r.Artifacts, r.Shots = fa.Artifacts, fa.Shots
			if fa.Log != "" {
				r.LogPath = fa.Log
			}
		}
		switch {
		case unfinished(r):
		case r.Passed():
			hr.passed++
		default:
			hr.failed++
		}
	}
}

// apply swaps in a fresh scan, keeping the selected run and flow.
func (hm *historyModel) apply(runs []*histRun) {
	hm.loaded, hm.dirty = true, false
	sel := ""
	if r := hm.selected(); r != nil {
		sel = r.dir
	}
	hm.runs, hm.total = runs, 0
	for i, r := range runs {
		hm.total += r.size
		if r.dir == sel {
			hm.cursor = i
		}
	}
	if hm.cursor >= len(runs) {
		hm.cursor = max(0, len(runs)-1)
	}
	if r := hm.selected(); r != nil && r.dir != sel {
		hm.resCur, hm.focus = 0, 0
	}
}

// onTick follows live runs: their progress every second, a full rescan (new or finished
// runs) every few seconds in the background.
func (hm *historyModel) onTick(t time.Time) tea.Cmd {
	if !hm.loaded {
		return nil
	}
	for _, r := range hm.runs {
		if r.active {
			readResults(r)
		}
	}
	if run := hm.selected(); run != nil && run.active && hm.focus >= 1 {
		hm.resCur = min(hm.resCur, max(0, len(run.results)-1))
		scroll := hm.scroll
		hm.loadPreview()
		hm.scroll = scroll
	}
	if t.Second()%5 != 0 || hm.scanning {
		return nil
	}
	hm.scanning = true
	return func() tea.Msg { return histScanMsg(hm.scan()) }
}
func (hm *historyModel) selected() *histRun {
	if hm.cursor < len(hm.runs) {
		return hm.runs[hm.cursor]
	}
	return nil
}

// logFor finds a flow's log: directly in a run dir, or in a parallel session's worker dirs.
func (hm *historyModel) logFor(run *histRun, r *engine.Result) string {
	if _, err := os.Stat(r.LogPath); err == nil {
		return r.LogPath
	}
	matches, _ := filepath.Glob(filepath.Join(run.dir, "worker-*", "logs", engine.FlowSlug(r.Feature, r.File)+".log"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func (hm *historyModel) loadPreview() {
	hm.preview, hm.scroll = nil, 0
	run := hm.selected()
	if run == nil || hm.resCur >= len(run.results) {
		return
	}
	path := hm.logFor(run, run.results[hm.resCur])
	if path == "" {
		hm.preview = []string{sMuted.Render("no log for this flow")}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		hm.preview = []string{err.Error()}
		return
	}
	hm.preview = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func (hm *historyModel) key(k tea.KeyMsg) tea.Cmd {
	if !hm.loaded || hm.dirty {
		hm.load()
	}
	run := hm.selected()
	if hm.confirm != "" {
		action := hm.confirm
		hm.confirm = ""
		if !confirmed(k) {
			return nil
		}
		switch action {
		case "delete":
			if run == nil {
				return nil
			}
			if err := hm.a.query.DeleteRunDir(context.Background(), run.dir); err != nil {
				hm.a.log("error", "delete: %v", err)
			} else {
				hm.a.log("info", "deleted %s (%s)", filepath.Base(run.dir), engine.HumanSize(run.size))
				hm.focus = 0
			}
		case "prune":
			removed, freed := hm.a.query.Prune(context.Background(), engine.KeepRuns())
			hm.a.log("info", "pruned %d run dir(s), freed %s", removed, engine.HumanSize(freed))
		case "all":
			removed, freed := hm.a.query.Prune(context.Background(), 0)
			hm.a.log("info", "deleted %d run dir(s), freed %s", removed, engine.HumanSize(freed))
		}
		hm.load()
		return nil
	}
	if hm.navKey(k.String(), run) {
		return nil
	}
	switch k.String() {
	case "x":
		if run != nil {
			if run.active {
				hm.a.log("warn", "%s belongs to an active run", filepath.Base(run.dir))
				return nil
			}
			paths, size := hm.a.query.Linked(run.rd)
			hm.linkedN, hm.linkedB = len(paths), size
			hm.confirm = "delete"
		}
		return nil
	case "P":
		hm.confirm = "prune"
		return nil
	case "X":
		hm.confirm = "all"
		return nil
	case "m":
		if hm.focus >= 1 {
			hm.showStubs = !hm.showStubs
		}
	case "enter", "right":
		if hm.focus == 2 && hm.showStubs && k.String() == "enter" {
			if hm.sCur < len(hm.stubs) {
				hm.a.tab = tabStubs
				hm.a.stubTab.show(hm.stubs[hm.sCur].name)
			}
			return nil
		}
		if hm.focus < 2 && run != nil {
			hm.focus++
			if hm.focus == 1 {
				for i, r := range run.results {
					if r.Stage == "RUNNING" {
						hm.resCur = i
						break
					}
				}
				hm.loadPreview()
			}
		}
	case "esc", "left":
		if hm.focus > 0 {
			hm.focus--
			if hm.focus == 0 {
				hm.picked = nil
			}
		}
	case " ":
		if run != nil && hm.focus >= 1 && hm.resCur < len(run.results) {
			if hm.picked == nil {
				hm.picked = map[string]bool{}
			}
			togglePick(hm.picked, run.results[hm.resCur])
			hm.resCur = min(hm.resCur+1, len(run.results)-1)
			hm.loadPreview()
		}
	case "y":
		if run != nil && hm.focus >= 1 {
			copyResults(hm.a, run.results, hm.picked, hm.resCur)
		}
	case "e":
		if run != nil && hm.focus >= 1 && hm.resCur < len(run.results) {
			return hm.a.editResult(run.results[hm.resCur])
		}
	case "s", "A":
		if run != nil && hm.focus >= 1 && hm.resCur < len(run.results) {
			if k.String() == "s" {
				openShot(hm.a, run.results[hm.resCur])
			} else {
				openArtifacts(hm.a, run.results[hm.resCur])
			}
		}
	case "o":
		if run != nil {
			_ = exec.Command("open", run.dir).Start()
		}
	case "F", "a":
		if run == nil {
			return nil
		}
		hm.a.cfg.openResults(run.results, k.String() == "F")
	}
	return nil
}

func (hm *historyModel) view(w, h int) (string, string) {
	if !hm.loaded || hm.dirty {
		hm.load()
	}
	listW := 44
	var lb strings.Builder
	s, e := window(len(hm.runs), hm.cursor, h-3)
	for i := s; i < e; i++ {
		r := hm.runs[i]
		cur := "  "
		if i == hm.cursor {
			cur = sSel.Render("› ")
		}
		res := sOK.Render(fmt.Sprintf("%d✔", r.passed))
		if r.failed > 0 {
			res += " " + sBad.Render(fmt.Sprintf("%d✘", r.failed))
		}
		if r.active && len(r.results) > 0 {
			res = sWarn.Render(fmt.Sprintf("%d/%d", r.passed+r.failed, len(r.results))) + " " + res
		}
		if len(r.results) == 0 {
			res = sMuted.Render("no results")
			if r.err != "" {
				res = sBad.Render("setup failed")
			} else if r.active {
				res = sWarn.Render("running")
			}
		}
		kind := ""
		if strings.HasPrefix(filepath.Base(r.dir), "session-") {
			kind = sMuted.Render(fmt.Sprintf(" ∥%d", len(r.rd.Workers)))
		}
		if r.active {
			kind += sWarn.Render(" ●")
		}
		fmt.Fprintf(&lb, "%s%s %s %s%s\n", cur, r.at.Local().Format("Jan 02 15:04"), pad(sMuted.Render(engine.HumanSize(r.size)), 5), res, kind)
	}
	if len(hm.runs) == 0 {
		lb.WriteString(sMuted.Render("no runs recorded yet"))
	}

	midW := 52
	var rb strings.Builder
	run := hm.selected()
	if run != nil {
		fmt.Fprintf(&rb, "%s\n%s\n\n", sMuted.Render(hm.a.p.Rel(run.dir)), sMuted.Render(fmt.Sprintf("total %s · runners %d", run.total, max(1, len(run.rd.Workers)))))
		for _, st := range run.stages {
			rb.WriteString(sWarn.Render("▶ "+truncate(st, midW-6)) + "\n")
		}
		if len(run.stages) > 0 {
			rb.WriteString("\n")
		}
		if run.err != "" {
			for _, l := range strings.Split(run.err, "\n") {
				rb.WriteString(sBad.Render(l) + "\n")
			}
			rb.WriteString("\n")
		}
		var rows []string
		focusLine := 0
		for i, r := range run.results {
			cur := "  "
			if hm.focus >= 1 && i == hm.resCur {
				cur = sSel.Render("› ")
				focusLine = len(rows)
			}
			if unfinished(r) {
				rows = append(rows, fmt.Sprintf("%s%s%s %s", cur, pickMark(hm.picked[resultKey(r)]), statusStyle(false, r.Stage), truncate(r.Key(), 34)))
				continue
			}
			rows = append(rows, fmt.Sprintf("%s%s%s %s %s%s", cur, pickMark(hm.picked[resultKey(r)]), statusStyle(r.Passed(), r.Stage), truncate(r.Key(), 34), sMuted.Render(fmt.Sprintf("%ds", int(r.Duration.Seconds()))), shotBadge(r)))
			if !r.Passed() {
				rows = append(rows, "     "+sMuted.Render(strings.TrimSpace(r.Stage+" "+r.Message)))
			}
		}
		used := strings.Count(rb.String(), "\n")
		rb.WriteString(strings.Join(visibleLines(rows, focusLine, h-3-used), "\n"))
	}

	logW := w - listW - midW
	lh := h - 3
	hm.scroll = min(hm.scroll, max(0, len(hm.preview)-lh))
	end := len(hm.preview) - hm.scroll
	start := max(0, end-lh)
	var preview string
	title := "Flow log · m stubs"
	switch {
	case hm.focus == 0:
		title = "Flow log"
		preview = sMuted.Render("enter to inspect a run's flows")
	case hm.showStubs:
		title = "Flow stubs · m log"
		preview = hm.stubsView(run, logW-4, lh)
	default:
		preview = strings.Join(hm.preview[start:end], "\n")
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		focusPanel(hm.focus == 0, fmt.Sprintf("Runs · %d · %s", len(hm.runs), engine.HumanSize(hm.total)), lb.String(), listW, h),
		focusPanel(hm.focus == 1, "Flows", rb.String(), midW, h),
		focusPanel(hm.focus == 2, title, preview, logW, h))
	keys := ""
	if run != nil && hm.focus >= 1 && hm.resCur < len(run.results) {
		keys = artifactKeys(run.results[hm.resCur])
	}
	help := fmt.Sprintf(navHelp+" · x delete · X delete all · P prune (keep %d)%s · a re-run all · F re-run failed · o open dir", engine.KeepRuns(), keys)
	switch hm.confirm {
	case "delete":
		if run != nil {
			extra := ""
			if len(run.rd.Workers) > 0 {
				extra += fmt.Sprintf(" with its %d worker run(s)", len(run.rd.Workers))
			}
			if hm.linkedN > 0 {
				extra += fmt.Sprintf(" + %d recording(s) outside the project (%s)", hm.linkedN, engine.HumanSize(hm.linkedB))
			}
			help = sWarn.Render(fmt.Sprintf("Delete %s (%s)%s? "+confirmHint, filepath.Base(run.dir), engine.HumanSize(run.size), extra))
		}
	case "prune":
		help = sWarn.Render(fmt.Sprintf("Keep the newest %d finished runs, delete the rest and strip APK snapshots? "+confirmHint, engine.KeepRuns()))
	case "all":
		help = sWarn.Render("Delete every finished run dir of this project? " + confirmHint)
	}
	return body, help
}

// navKey moves whichever column has focus; the log (focus 2) scrolls, offset from its tail.
func (hm *historyModel) navKey(k string, run *histRun) bool {
	switch hm.focus {
	case 0:
		c, ok := navKey(k, hm.cursor, len(hm.runs))
		if ok && c != hm.cursor {
			hm.cursor, hm.resCur = c, 0
		}
		return ok
	case 1:
		if run == nil {
			return false
		}
		c, ok := navKey(k, hm.resCur, len(run.results))
		if ok && c != hm.resCur {
			hm.resCur = c
			hm.loadPreview()
		}
		return ok
	}
	if hm.showStubs {
		hm.loadStubs(run)
		c, ok := navKey(k, hm.sCur, len(hm.stubs))
		hm.sCur = c
		return ok
	}
	switch k {
	case "up", "k":
		hm.scroll++
	case "down", "j":
		hm.scroll = max(0, hm.scroll-1)
	case "u":
		hm.scroll += pageSize
	case "d":
		hm.scroll = max(0, hm.scroll-pageSize)
	case "g", "home":
		hm.scroll = len(hm.preview)
	case "G", "end":
		hm.scroll = 0
	default:
		return false
	}
	return true
}

// loadStubs resolves the selected flow's stubs from its file as it is now, not as it ran.
func (hm *historyModel) loadStubs(run *histRun) {
	key := ""
	var flow *fixtures.Flow
	if run != nil && hm.resCur < len(run.results) {
		r := run.results[hm.resCur]
		key = r.Feature + "/" + r.File
		for _, f := range hm.a.flows {
			if f.Feature == r.Feature && f.File == r.File {
				flow = f
			}
		}
	}
	if key == hm.stubsFor {
		return
	}
	hm.stubsFor, hm.stubs, hm.stubErr, hm.sCur = key, nil, nil, 0
	switch {
	case key == "":
	case flow == nil:
		hm.stubErr = fmt.Errorf("%s no longer exists", key)
	default:
		hm.stubs, hm.stubErr = hm.a.targetStubs(hm.a.p.Rel(flow.Path))
	}
}

func (hm *historyModel) stubsView(run *histRun, w, h int) string {
	hm.loadStubs(run)
	head := sMuted.Render("from the flow file as it is now") + "\n\n"
	if hm.stubErr != nil {
		return head + sBad.Render(hm.stubErr.Error())
	}
	if len(hm.stubs) == 0 {
		return head + sMuted.Render("no stubs")
	}
	lines, at := stubListLines(hm.stubs, hm.sCur, hm.focus == 2, w)
	if hm.focus == 2 {
		st, en := window(len(lines), at, h-2)
		return head + strings.Join(lines[st:en], "\n")
	}
	st, en := window(len(lines), at, h-4)
	return head + strings.Join(lines[st:en], "\n") + "\n\n" + sMuted.Render("→ to pick a stub · enter opens it in Stubs")
}
