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
}

// historyModel lists past runs from .maestro/.runtime/runs.
type historyModel struct {
	a       *app
	runs    []*histRun
	cursor  int
	resCur  int
	focus   int // 0 runs, 1 results
	dirty   bool
	loaded  bool
	preview []string
	scroll  int
	confirm string // "delete" | "prune"
	linkedN int    // files outside the run dir the pending delete will also remove
	linkedB int64
	total   int64
	picked  map[string]bool
}

func (hm *historyModel) load() {
	hm.loaded, hm.dirty = true, false
	var runs []*histRun
	hm.total = 0
	for _, rd := range hm.a.query.RunDirs(context.Background(), true) {
		hm.total += rd.Size
		hr := &histRun{dir: rd.Path, at: rd.ModTime, size: rd.Size, active: rd.Active, err: engine.RunError(rd.Path), rd: rd}
		at, total, results, err := engine.LoadResults(filepath.Join(rd.Path, "result.tsv"))
		if err == nil {
			if !at.IsZero() {
				hr.at = at
			}
			hr.total, hr.results = total, results
		}
		arts := engine.LoadArtifacts(rd.Path)
		for _, r := range hr.results {
			r.LogPath = filepath.Join(rd.Path, "logs", engine.FlowSlug(r.Feature, r.File)+".log")
			if fa, ok := arts[r.Feature+"/"+r.File]; ok {
				r.Artifacts, r.Shots = fa.Artifacts, fa.Shots
				if fa.Log != "" {
					r.LogPath = fa.Log
				}
			}
			if r.Passed() {
				hr.passed++
			} else {
				hr.failed++
			}
		}
		runs = append(runs, hr)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].at.After(runs[j].at) })
	hm.runs = runs
	if hm.cursor >= len(runs) {
		hm.cursor = max(0, len(runs)-1)
	}
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
	case "enter", "right":
		if hm.focus < 2 && run != nil {
			hm.focus++
			if hm.focus == 1 {
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
	case "F":
		if run == nil {
			return nil
		}
		var failed []*fixtures.Flow
		for _, res := range run.results {
			if res.Passed() {
				continue
			}
			for _, f := range hm.a.flows {
				if f.Feature == res.Feature && f.File == res.File {
					failed = append(failed, f)
				}
			}
		}
		if len(failed) > 0 {
			hm.a.tab = tabFlows
			hm.a.cfg.open(failed, "", true)
		}
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
			rows = append(rows, fmt.Sprintf("%s%s%s %s %s%s", cur, pickMark(hm.picked[resultKey(r)]), statusStyle(r.Passed(), r.Stage), truncate(r.Feature+"/"+r.File, 34), sMuted.Render(fmt.Sprintf("%ds", int(r.Duration.Seconds()))), shotBadge(r)))
			if !r.Passed() {
				rows = append(rows, "     "+sMuted.Render(r.Stage))
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
	if hm.focus == 0 {
		preview = sMuted.Render("enter to inspect a run's flows")
	} else {
		preview = strings.Join(hm.preview[start:end], "\n")
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		focusPanel(hm.focus == 0, fmt.Sprintf("Runs · %d · %s", len(hm.runs), engine.HumanSize(hm.total)), lb.String(), listW, h),
		focusPanel(hm.focus == 1, "Flows", rb.String(), midW, h),
		focusPanel(hm.focus == 2, "Flow log", preview, logW, h))
	keys := ""
	if run != nil && hm.focus >= 1 && hm.resCur < len(run.results) {
		keys = artifactKeys(run.results[hm.resCur])
	}
	help := fmt.Sprintf(navHelp+" · x delete · X delete all · P prune (keep %d)%s · F re-run failed · o open dir", engine.KeepRuns(), keys)
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
