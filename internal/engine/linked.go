package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Files a run produced outside its own dir: Maestro's debug output in ~/.maestro/tests (runs
// before --debug-output, test.sh's included) and recordings written to --record-output-dir.

var runIDRef = regexp.MustCompile(`runs/(\d{8}T\d{6}Z-\d+-[0-9a-f]+)`)

func maestroTestsRoot() string {
	if v := os.Getenv("TRUN_MAESTRO_TESTS_DIR"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".maestro", "tests")
}

// runWindow is when a run dir's runs were alive, padded for clock skew.
func runWindow(dirs []string) (time.Time, time.Time) {
	var start, end time.Time
	for _, d := range dirs {
		name := filepath.Base(d)
		stamp := strings.TrimPrefix(name, "session-")
		if len(stamp) >= 16 {
			if t, err := time.Parse("20060102T150405Z", stamp[:16]); err == nil && (start.IsZero() || t.Before(start)) {
				start = t
			}
		}
		for _, f := range []string{"result.tsv", "stage", ""} {
			if st, err := os.Stat(filepath.Join(d, f)); err == nil && st.ModTime().After(end) {
				end = st.ModTime()
			}
		}
	}
	return start.Add(-time.Minute), end.Add(time.Minute)
}

// LinkedExternal lists files outside the project that belong only to the given run dirs.
func LinkedExternal(dirs []string) []string {
	ids := map[string]bool{}
	for _, d := range dirs {
		ids[filepath.Base(d)] = true
	}
	var out []string
	start, end := runWindow(dirs)
	var entries []os.DirEntry
	if start.Year() > 2000 { // no timestamp in the dir name: nothing to match safely
		entries, _ = os.ReadDir(maestroTestsRoot())
	}
	for _, ent := range entries {
		t, err := time.ParseInLocation("2006-01-02_150405", ent.Name(), time.Local)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		dir := filepath.Join(maestroTestsRoot(), ent.Name())
		b, err := os.ReadFile(filepath.Join(dir, "maestro.log"))
		if err != nil {
			continue
		}
		refs := runIDRef.FindAllStringSubmatch(string(b), -1)
		if len(refs) == 0 {
			continue
		}
		// Shared by runs in the same second: only when every run it mentions is going.
		mine := true
		for _, m := range refs {
			if !ids[m[1]] {
				mine = false
				break
			}
		}
		if mine {
			out = append(out, dir)
		}
	}
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
			for _, p := range []string{rec, strings.TrimSuffix(rec, ".mp4") + ".raw.mp4"} {
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
		size += DirSize(p)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			size += st.Size()
		}
	}
	return paths, size
}

func removeGroup(rd RunDir) int64 {
	var freed int64
	for _, p := range LinkedExternal(rd.group()) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			freed += st.Size()
		} else {
			freed += DirSize(p)
		}
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
