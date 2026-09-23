package paths

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Known projects: every repository trun has operated on. Their legacy test.sh lock roots are
// mirrored and checked too, so a test.sh run in another repo and a trun run never share a
// device.

var projectsMu sync.Mutex

func projectsFile() string { return filepath.Join(StateRoot(), "projects.json") }

// LegacyRoots returns the recorded legacy shared roots (<git-common-dir>/maestro) that exist.
func LegacyRoots() []string {
	projectsMu.Lock()
	defer projectsMu.Unlock()
	m := readProjects()
	var out []string
	for _, shared := range m {
		if st, err := os.Stat(filepath.Dir(shared)); err == nil && st.IsDir() {
			out = append(out, shared)
		}
	}
	sort.Strings(out)
	return out
}

// RememberProject records root -> its legacy shared root.
func RememberProject(root, legacyShared string) {
	projectsMu.Lock()
	defer projectsMu.Unlock()
	m := readProjects()
	if m[root] == legacyShared {
		return
	}
	m[root] = legacyShared
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(StateRoot(), 0o755)
	tmp := fmt.Sprintf("%s.tmp.%d", projectsFile(), os.Getpid())
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, projectsFile())
	}
}

func readProjects() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(projectsFile())
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}
