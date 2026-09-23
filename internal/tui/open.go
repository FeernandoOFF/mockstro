package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/fixtures"
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

// attachLegacyArtifacts finds artifacts for runs without artifacts.json: Maestro names each
// output dir after the flow's `name:`.
func attachLegacyArtifacts(dir string, results []*engine.Result, flows []*fixtures.Flow) {
	byFile := map[string]string{}
	for _, f := range flows {
		byFile[f.Feature+"/"+f.File] = f.Header.Name
	}
	for _, r := range results {
		name := byFile[r.Feature+"/"+r.File]
		if r.Artifacts != "" || name == "" {
			continue
		}
		for _, pattern := range []string{filepath.Join(dir, "results", "*", name), filepath.Join(dir, "worker-*", "results", "*", name)} {
			matches, _ := filepath.Glob(pattern)
			if len(matches) == 0 {
				continue
			}
			r.Artifacts = matches[len(matches)-1]
			shots, _ := filepath.Glob(filepath.Join(r.Artifacts, "screenshots", "*.png"))
			r.Shots = shots
			break
		}
	}
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
