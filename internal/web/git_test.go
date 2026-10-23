package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/gitrepo"
	"github.com/feernandooff/mockstro/internal/project"
)

// gitIn runs git in dir with the user's own configuration kept out of the way.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", msg)
}

type repos struct{ origin, work, other string }

// newRepos makes a bare origin, a tester's checkout and a second clone that pushes changes.
func newRepos(t *testing.T) repos {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	r := repos{origin: filepath.Join(base, "origin.git"), work: filepath.Join(base, "work"), other: filepath.Join(base, "other")}
	gitIn(t, base, "init", "-q", "--bare", "-b", "main", r.origin)
	gitIn(t, base, "init", "-q", "-b", "main", r.work)
	gitIn(t, r.work, "remote", "add", "origin", r.origin)
	if err := os.MkdirAll(filepath.Join(r.work, ".maestro"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, r.work, "a.txt", "one\n", "first")
	gitIn(t, r.work, "push", "-q", "-u", "origin", "main")
	gitIn(t, base, "clone", "-q", r.origin, r.other)
	return r
}

func gitServer(t *testing.T, r repos) *Server {
	t.Helper()
	p := &project.Project{Root: r.work, MaestroRoot: filepath.Join(r.work, ".maestro"), Env: map[string]string{}, Platform: "ios", Integration: "lite"}
	ws := NewWorkspace(nil, "", execx.Real{})
	ws.Use(p, engine.DefaultOptions())
	s, err := NewServer(ws, testPort)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func gitPost(s *Server, path, body string) *httptest.ResponseRecorder {
	return do(s, "POST", path, goodHost, map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}, body)
}

func status(t *testing.T, s *Server) gitrepo.Status {
	t.Helper()
	var st gitrepo.Status
	rec := do(s, "GET", "/api/git", goodHost, nil, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestGitStatusOfACleanCheckout(t *testing.T) {
	s := gitServer(t, newRepos(t))
	st := status(t, s)
	if !st.Repo || st.Branch != "main" || st.Upstream != "origin/main" || st.Ahead != 0 || st.Behind != 0 || len(st.Dirty) != 0 ||
		st.Commit == nil || st.Commit.Subject != "first" {
		t.Fatalf("status = %+v", st)
	}
}

func TestFetchThenPullBringsInNewChanges(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	commitFile(t, r.other, "b.txt", "two\n", "add b")
	gitIn(t, r.other, "push", "-q")

	if st := status(t, s); st.Behind != 0 {
		t.Fatalf("behind before fetch = %d; the remote is not contacted until asked", st.Behind)
	}
	if rec := gitPost(s, "/api/git/fetch", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body)
	}
	if st := status(t, s); st.Behind != 1 {
		t.Fatalf("behind after fetch = %d", st.Behind)
	}
	rec := gitPost(s, "/api/git/pull", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("pull: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(r.work, "b.txt")); err != nil {
		t.Fatalf("pulled file missing: %v", err)
	}
	if st := status(t, s); st.Behind != 0 || st.Commit.Subject != "add b" {
		t.Fatalf("status after pull = %+v", st)
	}
}

func TestPullRefusesTrackedChangesButIgnoresUntrackedFiles(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	if err := os.MkdirAll(filepath.Join(r.work, ".maestro", "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.work, ".maestro", "local", "mine.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := gitPost(s, "/api/git/pull", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("an untracked saved stub must not block pull: %d %s", rec.Code, rec.Body)
	}

	if err := os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := gitPost(s, "/api/git/pull", "{}")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a.txt") || !strings.Contains(rec.Body.String(), "Ask a developer") {
		t.Fatalf("pull with a tracked change: %d %s", rec.Code, rec.Body)
	}
	got, _ := os.ReadFile(filepath.Join(r.work, "a.txt"))
	if string(got) != "edited\n" {
		t.Fatalf("the tester's edit was touched: %q", got)
	}
}

func TestPullNeverMergesDivergedHistory(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	commitFile(t, r.work, "mine.txt", "local\n", "local commit")
	commitFile(t, r.other, "theirs.txt", "remote\n", "remote commit")
	gitIn(t, r.other, "push", "-q")
	if rec := gitPost(s, "/api/git/fetch", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body)
	}
	before := gitIn(t, r.work, "rev-parse", "HEAD")
	rec := gitPost(s, "/api/git/pull", "{}")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "both changed") {
		t.Fatalf("diverged pull: %d %s", rec.Code, rec.Body)
	}
	if after := gitIn(t, r.work, "rev-parse", "HEAD"); after != before {
		t.Fatal("pull changed HEAD even though it could not fast-forward")
	}
	if st := status(t, s); st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestBranchListAndSwitch(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	gitIn(t, r.other, "switch", "-q", "-c", "feature/survey")
	commitFile(t, r.other, "s.txt", "survey\n", "survey work")
	gitIn(t, r.other, "push", "-q", "-u", "origin", "feature/survey")
	gitPost(s, "/api/git/fetch", "{}")

	var branches []gitrepo.Branch
	_ = json.Unmarshal(do(s, "GET", "/api/git/branches", goodHost, nil, "").Body.Bytes(), &branches)
	byName := map[string]gitrepo.Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	if !byName["main"].Current || byName["main"].Remote || !byName["feature/survey"].Remote || byName["feature/survey"].Current {
		t.Fatalf("branches = %+v", branches)
	}
	if _, dup := byName["origin/main"]; dup || len(branches) != 2 {
		t.Fatalf("a remote copy of a local branch must not be listed twice: %+v", branches)
	}

	if rec := gitPost(s, "/api/git/switch", `{"branch":"feature/survey"}`); rec.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body)
	}
	st := status(t, s)
	if st.Branch != "feature/survey" || st.Upstream != "origin/feature/survey" {
		t.Fatalf("after switch = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(r.work, "s.txt")); err != nil {
		t.Fatalf("branch contents missing: %v", err)
	}
	if rec := gitPost(s, "/api/git/switch", `{"branch":"main"}`); rec.Code != http.StatusOK || status(t, s).Branch != "main" {
		t.Fatalf("switch back: %d %s", rec.Code, rec.Body)
	}
}

func TestSwitchRefusesBadNamesAndDirtyCheckouts(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	gitIn(t, r.work, "branch", "other")
	for _, name := range []string{"", "--detach", "-f", "a..b", "x y", "main/", "bad.lock", "$(touch x)", "a;b", "--orphan=x"} {
		body, _ := json.Marshal(map[string]string{"branch": name})
		if rec := gitPost(s, "/api/git/switch", string(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("branch %q: %d, want 400", name, rec.Code)
		}
	}
	if st := status(t, s); st.Branch != "main" {
		t.Fatalf("a refused switch changed the branch: %+v", st)
	}

	if err := os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := gitPost(s, "/api/git/switch", `{"branch":"other"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a.txt") {
		t.Fatalf("dirty switch: %d %s", rec.Code, rec.Body)
	}
	if rec := gitPost(s, "/api/git/switch", `{"branch":"does-not-exist"}`); rec.Code == http.StatusOK {
		t.Fatalf("a missing branch must not succeed")
	}
}

func TestPullAndSwitchRefuseWhileAMockIsRunning(t *testing.T) {
	r := newRepos(t)
	s := gitServer(t, r)
	blockWorkspaceStartup(t, s.ws)
	for path, body := range map[string]string{"/api/git/pull": "{}", "/api/git/switch": `{"branch":"main"}`} {
		if rec := gitPost(s, path, body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stop the running mock") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestGitWithoutARepo(t *testing.T) {
	s, _ := testServer(t) // the project's folder is not a git checkout
	s.ws.runner = execx.Real{}
	if st := status(t, s); st.Repo {
		t.Fatalf("status = %+v", st)
	}
	if rec := gitPost(s, "/api/git/pull", "{}"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not a git checkout") {
		t.Fatalf("pull: %d %s", rec.Code, rec.Body)
	}
}

func TestCloneDownloadsIntoStateDirAndOpensTheProject(t *testing.T) {
	r := newRepos(t)
	state := t.TempDir()
	load := func(dir string) (*project.Project, engine.Options, error) {
		if _, err := os.Stat(filepath.Join(dir, ".maestro")); err != nil {
			return nil, engine.Options{}, project.ErrNotFound
		}
		if _, err := os.ReadFile(filepath.Join(dir, ".maestro", "mockstro.yaml")); err != nil {
			return nil, engine.Options{}, fmt.Errorf("%w\n\nTo set up a new suite, see: init --help", err)
		}
		return &project.Project{Root: dir, MaestroRoot: filepath.Join(dir, ".maestro"), Env: map[string]string{}, Platform: "ios", Integration: "lite"}, engine.DefaultOptions(), nil
	}
	ws := NewWorkspace(load, filepath.Join(state, "ui.json"), execx.Real{})
	s, err := NewServer(ws, testPort)
	if err != nil {
		t.Fatal(err)
	}
	clone := func(url string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"url": url})
		return gitPost(s, "/api/project/clone", string(b))
	}

	if rec := clone(filepath.Join(state, "missing.git")); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"kind":"notFound"`) {
		t.Fatalf("clone of missing repo: %d %s", rec.Code, rec.Body)
	}
	if rec := clone(r.origin); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"kind":"notProject"`) {
		t.Fatalf("clone without .maestro: %d %s", rec.Code, rec.Body)
	}
	commitFile(t, r.work, ".maestro/mockstro.yaml", "cliVersion: v0.1.0\n", "suite")
	gitIn(t, r.work, "push", "-q")
	os.RemoveAll(filepath.Join(state, "projects"))

	rec := clone(r.origin)
	want := filepath.Join(state, "projects", "origin")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body)
	}
	if p, _ := ws.Current(); p == nil || p.Root != want {
		t.Fatalf("opened %+v", p)
	}
	if rec := clone(r.origin); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"kind":"exists"`) {
		t.Fatalf("second clone: %d %s", rec.Code, rec.Body)
	}
	if rec := clone("ext::sh -c touch% /tmp/pwned"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsafe url: %d %s", rec.Code, rec.Body)
	}
}

func TestCloneWithoutSetupOffersBranchesThatHaveIt(t *testing.T) {
	r := newRepos(t)
	// main has plain Maestro flows but no mockstro.yaml.
	commitFile(t, r.work, ".maestro/config.yaml", "flows: []\n", "maestro")
	gitIn(t, r.work, "push", "-q")
	gitIn(t, r.work, "switch", "-q", "-c", "feature/mockstro")
	commitFile(t, r.work, ".maestro/mockstro.yaml", "cliVersion: v0.1.0\n", "suite")
	gitIn(t, r.work, "push", "-q", "-u", "origin", "feature/mockstro")
	gitIn(t, r.work, "switch", "-q", "-c", "other", "main")
	gitIn(t, r.work, "push", "-q", "-u", "origin", "other")

	state := t.TempDir()
	load := func(dir string) (*project.Project, engine.Options, error) {
		if _, err := os.Stat(filepath.Join(dir, ".maestro")); err != nil {
			return nil, engine.Options{}, project.ErrNotFound
		}
		if _, err := os.ReadFile(filepath.Join(dir, ".maestro", "mockstro.yaml")); err != nil {
			return nil, engine.Options{}, fmt.Errorf("%w\n\nTo set up a new suite, see: init --help", err)
		}
		return &project.Project{Root: dir, MaestroRoot: filepath.Join(dir, ".maestro"), Env: map[string]string{}, Platform: "ios", Integration: "lite"}, engine.DefaultOptions(), nil
	}
	ws := NewWorkspace(load, filepath.Join(state, "ui.json"), execx.Real{})
	s, err := NewServer(ws, testPort)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"url": "git clone " + r.origin})
	if rec := gitPost(s, "/api/project/clone", string(b)); !strings.Contains(rec.Body.String(), `"kind":"notProject"`) {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body)
	}
	dest := filepath.Join(state, "projects", "origin")
	if rec := gitPost(s, "/api/project/open", `{"path":"`+dest+`"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"kind":"notProject"`) {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}

	rec := do(s, "GET", "/api/project/setup-branches?path="+url.QueryEscape(dest), goodHost, nil, "")
	var got struct{ Branches []gitrepo.Branch }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Branches) != 1 || got.Branches[0].Name != "feature/mockstro" {
		t.Fatalf("setup branches: %d %s", rec.Code, rec.Body)
	}

	b, _ = json.Marshal(map[string]string{"path": dest, "branch": "feature/mockstro"})
	if rec := gitPost(s, "/api/project/open-branch", string(b)); rec.Code != http.StatusOK {
		t.Fatalf("open branch: %d %s", rec.Code, rec.Body)
	}
	if p, _ := ws.Current(); p == nil || p.Root != dest {
		t.Fatalf("opened %+v", p)
	}
	if rec := gitPost(s, "/api/project/open-branch", string(b)); rec.Code == http.StatusOK {
		t.Fatal("switched the open project behind its branch menu")
	}
}
