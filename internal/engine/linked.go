package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// LinkedExternal lists recordings the given run dirs wrote outside the project (--record-output-dir).
func LinkedExternal(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		_, _, results, err := LoadResults(filepath.Join(d, "result.tsv"))
		if err != nil {
			continue
		}
		for _, r := range results {
			rec := r.Recording
			if rec == "" || strings.HasPrefix(rec, d+string(os.PathSeparator)) {
				continue
			}
			for _, p := range []string{rec, rawPath(rec)} {
				if _, err := os.Stat(p); err == nil {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// runGroup is a run dir plus, for a session, its workers.
func (rd RunDir) group() []string { return append([]string{rd.Path}, rd.Workers...) }

// Linked returns what deleting rd would remove outside the project, and its size.
func (e *Engine) Linked(rd RunDir) ([]string, int64) {
	paths := LinkedExternal(rd.group())
	var size int64
	for _, p := range paths {
		size += DirSize(p) // also counts a single file
	}
	return paths, size
}

func removeGroup(rd RunDir) int64 {
	var freed int64
	for _, p := range LinkedExternal(rd.group()) {
		freed += DirSize(p)
		os.RemoveAll(p)
	}
	for _, w := range rd.Workers {
		freed += DirSize(w)
		os.RemoveAll(w)
	}
	freed += DirSize(rd.Path)
	os.RemoveAll(rd.Path)
	return freed
}
