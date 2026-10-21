package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

func fakeProject(t *testing.T, name string) *project.Project {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".maestro"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &project.Project{Root: root, MaestroRoot: filepath.Join(root, ".maestro"), Env: map[string]string{}, Platform: "ios", Integration: "lite"}
}

func loaderFor(projects map[string]*project.Project) Loader {
	return func(dir string) (*project.Project, engine.Options, error) {
		p, ok := projects[dir]
		if !ok {
			return nil, engine.Options{}, fmt.Errorf("%w: %s", project.ErrNotFound, dir)
		}
		return p, engine.DefaultOptions(), nil
	}
}

func TestOpenRecordsRecentsNewestFirstWithoutDuplicates(t *testing.T) {
	a, b := fakeProject(t, "alpha"), fakeProject(t, "beta")
	ws := NewWorkspace(loaderFor(map[string]*project.Project{a.Root: a, b.Root: b}), filepath.Join(t.TempDir(), "ui.json"))
	for _, dir := range []string{a.Root, b.Root, a.Root} {
		if err := ws.Open(dir); err != nil {
			t.Fatal(err)
		}
	}
	got := ws.Recent()
	if len(got) != 2 || got[0].Path != a.Root || got[1].Path != b.Root || got[0].Name != "alpha" || !got[0].Exists {
		t.Fatalf("recent = %+v", got)
	}
	if last, ok := ws.LastRecent(); !ok || last != a.Root {
		t.Fatalf("last = %q %v", last, ok)
	}
}

func TestRecentsAreCappedAndMissingFoldersAreSkippedOnLaunch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ui.json")
	projects := map[string]*project.Project{}
	ws := NewWorkspace(loaderFor(projects), file)
	var first *project.Project
	for i := range maxRecents + 3 {
		p := fakeProject(t, fmt.Sprintf("p%d", i))
		projects[p.Root] = p
		if first == nil {
			first = p
		}
		if err := ws.Open(p.Root); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(ws.Recent()); n != maxRecents {
		t.Fatalf("recents = %d, want %d", n, maxRecents)
	}
	newest := ws.Recent()[0]
	if err := os.RemoveAll(newest.Path); err != nil {
		t.Fatal(err)
	}
	last, ok := ws.LastRecent()
	if !ok || last == newest.Path {
		t.Fatalf("LastRecent should skip the deleted folder, got %q %v", last, ok)
	}
}

func TestOpenExplainsAFolderWithoutMaestro(t *testing.T) {
	ws := NewWorkspace(loaderFor(nil), "")
	err := ws.Open("/Users/qa/Documents")
	if err == nil || !strings.Contains(err.Error(), "has no .maestro folder") || strings.Contains(err.Error(), "init --help") {
		t.Fatalf("err = %v", err)
	}
	if p, m := ws.Current(); p != nil || m != nil {
		t.Fatal("a failed open must not change the workspace")
	}
}

func TestOpenRefusesWhileAMockIsRunningAndSwitchesWhenIdle(t *testing.T) {
	a, b := fakeProject(t, "alpha"), fakeProject(t, "beta")
	ws := NewWorkspace(loaderFor(map[string]*project.Project{a.Root: a, b.Root: b}), "")
	if err := ws.Open(a.Root); err != nil {
		t.Fatal(err)
	}
	_, first := ws.Current()

	first.mu.Lock()
	first.busy = true
	first.mu.Unlock()
	if err := ws.Open(b.Root); err == nil || !strings.Contains(err.Error(), "stop the running mock") {
		t.Fatalf("open while busy: %v", err)
	}
	if p, _ := ws.Current(); p != a {
		t.Fatal("project changed even though the open was refused")
	}

	first.mu.Lock()
	first.busy = false
	first.mu.Unlock()
	if err := ws.Open(b.Root); err != nil {
		t.Fatal(err)
	}
	if p, m := ws.Current(); p != b || m == first {
		t.Fatal("switch did not install the new project and manager")
	}
}

func emptyServer(t *testing.T, ex execx.Runner, projects map[string]*project.Project) *Server {
	t.Helper()
	ws := NewWorkspace(loaderFor(projects), "")
	s, err := NewServer(ws, ex, testPort)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEverythingExplainsItselfWithNoProjectOpen(t *testing.T) {
	s := emptyServer(t, &execx.Fake{}, nil)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}

	var st StateView
	_ = json.Unmarshal(do(s, "GET", "/api/state", goodHost, nil, "").Body.Bytes(), &st)
	if !st.NoProject || st.State != StateIdle {
		t.Fatalf("state = %+v", st)
	}
	if rec := do(s, "GET", "/api/catalog", goodHost, nil, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "choose a project folder") {
		t.Errorf("catalog: %d %s", rec.Code, rec.Body)
	}
	if rec := do(s, "POST", "/api/serve", goodHost, h, `{"kind":"scenario","ref":"x"}`); rec.Code != http.StatusConflict {
		t.Errorf("serve: %d", rec.Code)
	}
	if rec := do(s, "POST", "/api/stop", goodHost, h, `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("stop with nothing open: %d", rec.Code)
	}
	if rec := do(s, "GET", "/api/journal", goodHost, nil, ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("journal: %d %s", rec.Code, rec.Body)
	}
	var v ProjectView
	_ = json.Unmarshal(do(s, "GET", "/api/project", goodHost, nil, "").Body.Bytes(), &v)
	if v.Open || v.Recent == nil {
		t.Errorf("project = %+v", v)
	}
}

func TestOpenProjectEndpoint(t *testing.T) {
	p := fakeProject(t, "project")
	s := emptyServer(t, &execx.Fake{}, map[string]*project.Project{p.Root: p})
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}

	for name, body := range map[string]string{"relative": `{"path":"project"}`, "empty": `{"path":"  "}`, "unknown": `{"path":"/nowhere/at/all"}`} {
		if rec := do(s, "POST", "/api/project/open", goodHost, h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := do(s, "POST", "/api/project/open", goodHost, h, `{"path":"`+p.Root+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	var v ProjectView
	_ = json.Unmarshal(do(s, "GET", "/api/project", goodHost, nil, "").Body.Bytes(), &v)
	if !v.Open || v.Name != "project" || v.Platform != "ios" {
		t.Fatalf("project = %+v", v)
	}
}

func TestPickProjectUsesTheNativeChooser(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the chooser is macOS only")
	}
	var script string
	ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		script = strings.Join(c.Args, " ")
		return "/Users/qa/Code/project/\n", nil
	}}
	s := emptyServer(t, ex, nil)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	rec := do(s, "POST", "/api/project/pick", goodHost, h, `{}`)
	var out struct{ Path string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Path != "/Users/qa/Code/project" || !strings.Contains(script, "choose folder") {
		t.Fatalf("pick: %d %s (script %q)", rec.Code, rec.Body, script)
	}

	cancel := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", errors.New("execution error: User canceled. (-128)") }}
	s = emptyServer(t, cancel, nil)
	h = map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	if rec := do(s, "POST", "/api/project/pick", goodHost, h, `{}`); !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Fatalf("cancel should be reported as cancelled, not an error: %d %s", rec.Code, rec.Body)
	}
}

func TestPingAndQuit(t *testing.T) {
	s := emptyServer(t, &execx.Fake{}, nil)
	quit := make(chan struct{}, 1)
	s.quit = func() { quit <- struct{}{} }

	if rec := do(s, "GET", "/api/ping", goodHost, nil, ""); !strings.Contains(rec.Body.String(), appName) {
		t.Fatalf("ping: %s", rec.Body)
	}
	if rec := do(s, "POST", "/api/quit", goodHost, map[string]string{"Content-Type": "application/json"}, "{}"); rec.Code != http.StatusForbidden {
		t.Fatalf("quit without token: %d", rec.Code)
	}
	select {
	case <-quit:
		t.Fatal("quit ran without a token")
	default:
	}
	do(s, "POST", "/api/quit", goodHost, map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}, "{}")
	<-quit
}
