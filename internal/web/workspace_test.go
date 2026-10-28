package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/mockapp"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/workspacecoord"
)

func fakeProject(t *testing.T, name string) *project.Project {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".maestro"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setupFile(root), []byte("cliVersion: v0.1.0\n"), 0o644); err != nil {
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
	ws := NewWorkspace(loaderFor(map[string]*project.Project{a.Root: a, b.Root: b}), filepath.Join(t.TempDir(), "ui.json"), &execx.Fake{})
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
	ws := NewWorkspace(loaderFor(projects), file, &execx.Fake{})
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
	ws := NewWorkspace(loaderFor(nil), "", &execx.Fake{})
	err := ws.Open("/Users/qa/Documents")
	if err == nil || !strings.Contains(err.Error(), "has no .maestro/mockstro.yaml") || strings.Contains(err.Error(), "init --help") {
		t.Fatalf("err = %v", err)
	}
	if p, m := ws.Current(); p != nil || m != nil {
		t.Fatal("a failed open must not change the workspace")
	}
}

func TestOpenRefusesWhileAMockIsRunningAndSwitchesWhenIdle(t *testing.T) {
	a, b := fakeProject(t, "alpha"), fakeProject(t, "beta")
	ws := NewWorkspace(loaderFor(map[string]*project.Project{a.Root: a, b.Root: b}), "", &execx.Fake{})
	if err := ws.Open(a.Root); err != nil {
		t.Fatal(err)
	}
	_, first := ws.Current()

	resume := blockWorkspaceStartup(t, ws)
	if err := ws.Open(b.Root); err == nil || !strings.Contains(err.Error(), "stop the running mock") {
		t.Fatalf("open while busy: %v", err)
	}
	if p, _ := ws.Current(); p != a {
		t.Fatal("project changed even though the open was refused")
	}

	resume()
	first.life.Wait()
	if err := ws.Open(b.Root); err != nil {
		t.Fatal(err)
	}
	if p, m := ws.Current(); p != b || m == first {
		t.Fatal("switch did not install the new project and manager")
	}
}

func emptyServer(t *testing.T, ex execx.Runner, projects map[string]*project.Project) *Server {
	t.Helper()
	ws := NewWorkspace(loaderFor(projects), "", ex)
	s, err := NewServer(ws, testPort)
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

	for name, body := range map[string]string{"relative": `{"path":"project"}`, "empty": `{"path":"  "}`} {
		if rec := do(s, "POST", "/api/project/open", goodHost, h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	// A folder without .maestro is described so the page can offer branches that have one.
	if rec := do(s, "POST", "/api/project/open", goodHost, h, `{"path":"/nowhere/at/all"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"kind":"notProject"`) {
		t.Errorf("unknown: %d %s", rec.Code, rec.Body)
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

// blockedGit pauses after the idle/dirty checks, at the checkout-changing command.
// This reproduces the old gap where Serve or Open could enter after gitGate.
type blockedGit struct {
	execx.Runner
	command string
	entered chan struct{}
	resume  chan struct{}
}

func (b *blockedGit) Output(ctx context.Context, c execx.Cmd) (string, error) {
	if c.Name == "git" && len(c.Args) > 4 && c.Args[4] == b.command {
		close(b.entered)
		select {
		case <-b.resume:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return b.Runner.Output(ctx, c)
}

func TestGitReservationExcludesServeStopOpenAndOtherGit(t *testing.T) {
	for _, command := range []string{"pull", "switch", "fetch"} {
		t.Run(command, func(t *testing.T) {
			r := newRepos(t)
			gitIn(t, r.work, "branch", "other")
			s := gitServer(t, r)
			ws := s.ws
			var loads atomic.Int32
			ws.load = func(string) (*project.Project, engine.Options, error) {
				loads.Add(1)
				return fakeProject(t, "new"), engine.DefaultOptions(), nil
			}
			block := &blockedGit{Runner: execx.Real{}, command: command, entered: make(chan struct{}), resume: make(chan struct{})}
			ws.runner = block
			resumed := false
			defer func() {
				if !resumed {
					close(block.resume)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch command {
				case "pull":
					_, _, err = ws.Pull(ctx)
				case "switch":
					_, err = ws.SwitchBranch(ctx, "other")
				case "fetch":
					_, err = ws.Fetch(ctx)
				}
				done <- err
			}()
			select {
			case <-block.entered:
			case err := <-done:
				t.Fatalf("git did not reach blocked command: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for name, invoke := range map[string]func() error{
				"serve": func() error { return ws.Serve("scenario", nil, nil) },
				"stop":  ws.Stop,
				"open":  func() error { return ws.Open("/different-project") },
				"use":   func() error { return ws.Use(fakeProject(t, "different"), engine.DefaultOptions()) },
				"fetch": func() error { _, err := ws.Fetch(ctx); return err },
			} {
				if err := invoke(); !errors.Is(err, workspacecoord.ErrBusy) {
					t.Errorf("%s during blocked %s: %v", name, command, err)
				}
			}
			if loads.Load() != 0 {
				t.Fatal("Open read project files during a Git operation")
			}
			if p, _ := ws.Current(); p.Root != r.work {
				t.Fatal("project changed while Git was still running")
			}
			close(block.resume)
			resumed = true
			if err := <-done; err != nil {
				t.Fatalf("git operation: %v", err)
			}
			if err := ws.Stop(); err != nil {
				t.Fatalf("reservation not released: %v", err)
			}
		})
	}
}

func TestShutdownClosesWorkspaceAdmission(t *testing.T) {
	s := gitServer(t, newRepos(t))
	s.ws.Shutdown()
	for name, invoke := range map[string]func() error{
		"serve": func() error { return s.ws.Serve("scenario", nil, nil) },
		"open":  func() error { return s.ws.Open("/different") },
		"pull":  func() error { _, _, err := s.ws.Pull(context.Background()); return err },
	} {
		if err := invoke(); !errors.Is(err, workspacecoord.ErrClosed) {
			t.Errorf("%s after shutdown: %v", name, err)
		}
	}
}

// Use the lifecycle's actual synchronous admission instead of setting private
// manager flags: busy must cover the whole asynchronous factory invocation.
func blockWorkspaceStartup(t *testing.T, ws *Workspace) func() {
	t.Helper()
	_, m := ws.Current()
	resume := make(chan struct{})
	m.life = mockapp.New(func(ctx context.Context) (mockapp.Backend, error) {
		select {
		case <-resume:
			return nil, errors.New("startup stopped by test")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	t.Cleanup(ws.Shutdown)
	if err := ws.Serve("scenario", nil, nil); err != nil {
		t.Fatal(err)
	}
	return func() { close(resume) }
}

func TestAcceptedAsyncStartupExcludesCheckoutMutationAndOpen(t *testing.T) {
	s := gitServer(t, newRepos(t))
	resume := blockWorkspaceStartup(t, s.ws)
	defer resume()
	for name, invoke := range map[string]func() error{
		"pull":   func() error { _, _, err := s.ws.Pull(context.Background()); return err },
		"switch": func() error { _, err := s.ws.SwitchBranch(context.Background(), "main"); return err },
		"open":   func() error { return s.ws.Open("/different") },
	} {
		if err := invoke(); !errors.Is(err, errWorkspaceRunning) {
			t.Errorf("%s during async startup: %v", name, err)
		}
	}
}

// The runner supplied at composition reaches managers opened now and after switching.
func TestWorkspaceMockStartupUsesInjectedRunnerAcrossProjects(t *testing.T) {
	a, b := fakeProject(t, "alpha"), fakeProject(t, "beta")
	runner := &execx.Fake{Paths: map[string]string{"lsof": "lsof"}}
	ws := NewWorkspace(loaderFor(map[string]*project.Project{a.Root: a, b.Root: b}), "", runner)
	defer ws.Shutdown()
	for _, p := range []*project.Project{a, b} {
		if err := ws.Open(p.Root); err != nil {
			t.Fatal(err)
		}
		if err := ws.Serve("any", nil, nil); err != nil {
			t.Fatal(err)
		}
		_, manager := ws.Current()
		manager.life.Wait()
		got := manager.State()
		if got.State != StateIdle || !strings.Contains(got.Error, "missing required command(s): java") || strings.Contains(got.Error, "lsof") {
			t.Fatalf("startup did not use injected prerequisites: %+v", got)
		}
	}
}

func TestRemoveDeletesOnlyClonesAndNeverTheOpenProject(t *testing.T) {
	state := t.TempDir()
	clones := filepath.Join(state, "projects")
	clone, bare, mine, open := filepath.Join(clones, "app"), filepath.Join(clones, "no-setup"), fakeProject(t, "mine"), fakeProject(t, "open")
	for _, d := range []string{filepath.Join(clone, ".maestro", "stubs", "local"), bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cloned := &project.Project{Root: clone, MaestroRoot: filepath.Join(clone, ".maestro")}
	ws := NewWorkspace(loaderFor(map[string]*project.Project{clone: cloned, mine.Root: mine, open.Root: open}), filepath.Join(state, "ui.json"), &execx.Fake{})
	for _, p := range []string{clone, mine.Root, open.Root} {
		if err := ws.Open(p); err != nil {
			t.Fatal(err)
		}
	}
	listed := map[string]RecentProject{}
	for _, r := range ws.Recent() {
		listed[r.Path] = r
	}
	if r := listed[bare]; !r.Folder || r.Exists || !r.Managed {
		t.Fatalf("unopened clone listed as %+v", r)
	}
	if listed[mine.Root].Managed || !listed[clone].Managed {
		t.Fatalf("managed flags: %+v", listed)
	}

	if err := ws.Remove(open.Root, false); !errors.Is(err, errOpenProject) {
		t.Fatalf("removed the open project: %v", err)
	}
	if err := ws.Remove(mine.Root, true); err == nil {
		t.Fatal("deleted a folder Mockstro did not clone")
	}
	if err := ws.Remove(filepath.Join(clones, "..", "ui.json"), true); err == nil {
		t.Fatal("deleted outside the clones folder")
	}
	link := filepath.Join(clones, "link")
	os.Symlink(mine.Root, link)
	if err := ws.Remove(link, true); err == nil {
		t.Fatal("followed a symlink out of the clones folder")
	}
	if err := ws.Remove(mine.Root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine.Root); err != nil {
		t.Fatal("removing from the list deleted the folder")
	}
	if err := ws.Remove(clone, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("clone folder still there")
	}
	for _, r := range ws.Recent() {
		if r.Path == clone || r.Path == mine.Root {
			t.Fatalf("still listed: %+v", r)
		}
	}
}
