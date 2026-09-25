package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/atotto/clipboard"

	"github.com/feernandooff/mockstro/internal/engine"
)

// openPath hands a file or folder to macOS (Preview for screenshots, Finder for dirs).
func openPath(p string) error { return exec.Command("open", p).Start() }

// openShot opens a result's last screenshot (the failing step, when it failed).
func openShot(a *app, r *engine.Result) {
	switch {
	case r == nil:
	case len(r.Shots) > 0:
		shot := r.Shots[len(r.Shots)-1]
		if err := openPath(shot); err != nil {
			a.log("error", "open %s: %v", shot, err)
		}
	case r.Artifacts != "":
		a.log("info", "%s/%s has no screenshots; opening its artifacts", r.Feature, r.File)
		_ = openPath(r.Artifacts)
	default:
		a.log("info", "no Maestro artifacts recorded for %s/%s", r.Feature, r.File)
	}
}

func openArtifacts(a *app, r *engine.Result) {
	if r == nil || r.Artifacts == "" {
		a.log("info", "no Maestro artifacts recorded for this flow")
		return
	}
	_ = openPath(r.Artifacts)
}

func shotBadge(r *engine.Result) string {
	if len(r.Shots) == 0 {
		return ""
	}
	return sMuted.Render(fmt.Sprintf(" 📷%d", len(r.Shots)))
}

// artifactKeys is the footer hint for the selected result: only the keys that do something.
func artifactKeys(r *engine.Result) string {
	if r == nil {
		return ""
	}
	out := ""
	if len(r.Shots) > 0 {
		out += " · s screenshot"
	}
	if r.Artifacts != "" {
		out += " · A artifacts"
	}
	return out
}

func resultKey(r *engine.Result) string { return r.Feature + "/" + r.File }

// pickMark prefixes a result row picked for copying.
func pickMark(picked bool) string {
	if picked {
		return sSel.Render("*")
	}
	return " "
}

func togglePick(picked map[string]bool, r *engine.Result) {
	if picked[resultKey(r)] {
		delete(picked, resultKey(r))
	} else {
		picked[resultKey(r)] = true
	}
}

// copyResults copies "STATUS feature/file" for the picked results, or the one under the cursor.
func copyResults(a *app, results []*engine.Result, picked map[string]bool, cur int) {
	copyText(a, resultLines(results, picked, cur))
}

func resultLines(results []*engine.Result, picked map[string]bool, cur int) string {
	var lines []string
	for _, r := range results {
		if picked[resultKey(r)] {
			lines = append(lines, statusText(r.Passed(), r.Stage)+" "+resultKey(r))
		}
	}
	if len(lines) == 0 && cur < len(results) {
		r := results[cur]
		lines = append(lines, statusText(r.Passed(), r.Stage)+" "+resultKey(r))
	}
	return strings.Join(lines, "\n")
}

func copyText(a *app, s string) {
	if s == "" {
		return
	}
	if err := clipboard.WriteAll(s); err != nil {
		a.log("error", "copy failed: %v", err)
		return
	}
	a.log("info", "copied %s", strings.ReplaceAll(s, "\n", ", "))
}
