package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/gitrepo"
	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/workspacecoord"
)

const maxRecents = 8

// Loader turns a folder into a project and the engine options to serve it with. The CLI supplies
// it so this package never depends on flag handling.
type Loader func(dir string) (*project.Project, engine.Options, error)

// RecentProject is a project the UI opened before, offered on the welcome screen.
type RecentProject struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	Icon   bool   `json:"icon,omitempty"`
}

// recents remembers opened projects in a small JSON file; failures to read or write it are
// ignored because it is only a convenience.
type recents struct {
	path string
}

func (r *recents) load() []string {
	if r == nil || r.path == "" {
		return nil
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return nil
	}
	var f struct {
		Recent []string `json:"recent"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.Recent
}

func (r *recents) add(dir string) {
	if r == nil || r.path == "" {
		return
	}
	list := slices.DeleteFunc(r.load(), func(p string) bool { return p == dir })
	list = append([]string{dir}, list...)
	if len(list) > maxRecents {
		list = list[:maxRecents]
	}
	b, err := json.MarshalIndent(map[string]any{"recent": list}, "", "  ")
	if err != nil || os.MkdirAll(filepath.Dir(r.path), 0o755) != nil {
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, r.path)
	}
}

// Workspace holds the project the UI is working on and the mock manager for it. It starts empty
// when the UI is launched away from any project, and switches only while no mock is running.
type Workspace struct {
	operations workspacecoord.Coordinator
	runner     execx.Runner
	mu         sync.RWMutex
	load       Loader
	recents    *recents
	p          *project.Project
	mgr        *Manager
}

// NewWorkspace makes an empty workspace. recentsFile may be "" to keep no history.
func NewWorkspace(load Loader, recentsFile string, runner execx.Runner) *Workspace {
	return &Workspace{load: load, recents: &recents{path: recentsFile}, runner: runner}
}

// DefaultRecentsFile is where the UI remembers recent projects for this user.
func DefaultRecentsFile() string {
	if dir := os.Getenv("MOCKSTRO_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "ui.json")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mockstro", "ui.json")
}

// Use installs an already-loaded project, such as the one found from the working directory.
func (w *Workspace) Use(p *project.Project, o engine.Options) error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mgr != nil && !w.mgr.Idle() {
		return errWorkspaceRunning
	}
	w.install(p, o)
	return nil
}

func (w *Workspace) install(p *project.Project, o engine.Options) {
	if w.mgr != nil {
		w.mgr.Shutdown()
	}
	w.p, w.mgr = p, NewManager(p, o, w.runner)
	w.recents.add(p.Root)
}

// Current returns the open project and its manager, both nil when none is open.
func (w *Workspace) Current() (*project.Project, *Manager) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.p, w.mgr
}

// Open switches to the project containing dir. It refuses while a mock is running, so a tester
// cannot lose a session by clicking Change.
func (w *Workspace) Open(dir string) error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	// Reserve before loading: the loader reads files a Git mutation could change.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mgr != nil && !w.mgr.Idle() {
		return errWorkspaceRunning
	}
	if w.load == nil {
		return errors.New("opening projects is not available")
	}
	p, o, err := w.load(dir)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			return fmt.Errorf("%s has no .maestro folder. Choose the project folder that contains one", dir)
		}
		return err
	}
	w.install(p, o)
	return nil
}

// Recent lists remembered projects, newest first.
func (w *Workspace) Recent() []RecentProject {
	var out []RecentProject
	for _, path := range w.recents.load() {
		st, err := os.Stat(filepath.Join(path, ".maestro"))
		out = append(out, RecentProject{Path: path, Name: filepath.Base(path), Exists: err == nil && st.IsDir()})
	}
	if out == nil {
		out = []RecentProject{}
	}
	return out
}

// LastRecent is the most recent project that still exists, for opening on launch.
func (w *Workspace) LastRecent() (string, bool) {
	for _, r := range w.Recent() {
		if r.Exists {
			return r.Path, true
		}
	}
	return "", false
}

// Shutdown stops any mock the workspace started.
func (w *Workspace) Shutdown() {
	w.operations.Shutdown(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.mgr != nil {
			w.mgr.Shutdown()
		}
	})
}

var errWorkspaceRunning = errors.New("stop the running mock before changing project or changing the project's files")

// Serve admits startup or switching atomically with Open and checkout mutations.
// Manager.Serve must set busy before returning, and Shutdown must wait for async
// startup/stop completion; see workspacecoord.Coordinator's lifecycle contract.
func (w *Workspace) Serve(ref string, fbrc map[string]string, custom []mockruntime.CustomResponse) error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	_, m := w.Current()
	if m == nil {
		return errNoProject
	}
	return m.Serve(ref, fbrc, custom)
}

func (w *Workspace) Stop() error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	_, m := w.Current()
	if m == nil {
		return nil
	}
	return m.Stop()
}

// repository is called only under an operation reservation, so the project and
// its manager cannot change before the caller finishes using the checkout.
func (w *Workspace) repository(requireIdle bool) (*gitrepo.Repository, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.p == nil {
		return nil, errNoProject
	}
	if requireIdle && !w.mgr.Idle() {
		return nil, errWorkspaceRunning
	}
	return gitrepo.New(w.p.Root, w.runner), nil
}

func (w *Workspace) GitStatus(ctx context.Context) (gitrepo.Status, error) {
	release, err := w.operations.Reserve()
	if err != nil {
		return gitrepo.Status{}, err
	}
	defer release()
	repo, err := w.repository(false)
	if err != nil {
		return gitrepo.Status{}, err
	}
	return repo.Status(ctx)
}

func (w *Workspace) Branches(ctx context.Context) ([]gitrepo.Branch, error) {
	release, err := w.operations.Reserve()
	if err != nil {
		return nil, err
	}
	defer release()
	repo, err := w.repository(false)
	if err != nil {
		return nil, err
	}
	return repo.Branches(ctx)
}

func (w *Workspace) Fetch(ctx context.Context) (gitrepo.Status, error) {
	release, err := w.operations.Reserve()
	if err != nil {
		return gitrepo.Status{}, err
	}
	defer release()
	repo, err := w.repository(false)
	if err != nil {
		return gitrepo.Status{}, err
	}
	return repo.Fetch(ctx)
}

func (w *Workspace) Pull(ctx context.Context) (string, gitrepo.Status, error) {
	release, err := w.operations.Reserve()
	if err != nil {
		return "", gitrepo.Status{}, err
	}
	defer release()
	repo, err := w.repository(true)
	if err != nil {
		return "", gitrepo.Status{}, err
	}
	return repo.Pull(ctx)
}

func (w *Workspace) SwitchBranch(ctx context.Context, name string) (gitrepo.Status, error) {
	release, err := w.operations.Reserve()
	if err != nil {
		return gitrepo.Status{}, err
	}
	defer release()
	repo, err := w.repository(true)
	if err != nil {
		return gitrepo.Status{}, err
	}
	return repo.SwitchBranch(ctx, name)
}
