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
	"time"

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
	Exists bool   `json:"exists"` // has a .maestro folder
	Folder bool   `json:"folder"` // the folder itself is there
	// Managed projects were cloned into ClonesDir, so the UI may delete their folder.
	Managed bool `json:"managed,omitempty"`
	Icon    bool `json:"icon,omitempty"`
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
	r.save(append([]string{dir}, list...))
}

func (r *recents) remove(dir string) {
	if r == nil || r.path == "" {
		return
	}
	r.save(slices.DeleteFunc(r.load(), func(p string) bool { return p == dir }))
}

func (r *recents) save(list []string) {
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
	return w.openReserved(dir)
}

// openReserved is Open for a caller that already holds the operation reservation.
func (w *Workspace) openReserved(dir string) error {
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
		// A .maestro folder of plain Maestro flows, without mockstro.yaml, is not set up either.
		if errors.Is(err, project.ErrNotFound) || errors.Is(err, os.ErrNotExist) && !isFile(setupFile(dir)) {
			return noMaestroError(dir)
		}
		return err
	}
	w.install(p, o)
	return nil
}

// ClonesDir is where Clone puts checkouts: a projects folder beside the recents file.
func (w *Workspace) ClonesDir() string {
	if w.recents == nil || w.recents.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(w.recents.path), "projects")
}

// Clone checks out url into ClonesDir and opens it. The clone stays when opening fails, so a
// repository without a .maestro folder is not downloaded twice.
func (w *Workspace) Clone(ctx context.Context, url string) (string, error) {
	dir := w.ClonesDir()
	if dir == "" {
		return "", errors.New("cloning is not available: mockstro has no folder to keep projects in")
	}
	if _, m := w.Current(); m != nil && !m.Idle() {
		return "", errWorkspaceRunning
	}
	dest, err := gitrepo.Clone(ctx, w.runner, url, dir)
	if err != nil {
		return dest, err
	}
	return dest, w.Open(dest)
}

// SetupBranches lists the branches of the checkout at dir that contain a .maestro folder, after a
// best-effort fetch, for a repository whose current branch has none.
func (w *Workspace) SetupBranches(ctx context.Context, dir string) ([]gitrepo.Branch, error) {
	repo := gitrepo.New(dir, w.runner)
	st, err := repo.Status(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Repo {
		return nil, &gitrepo.Error{Kind: gitrepo.ErrNotRepository, Message: dir + " is not a git checkout, so there are no other branches to try"}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = repo.Fetch(fetchCtx)
	cancel()
	return integrationBranches(ctx, repo)
}

// OpenBranch switches the checkout at dir, which is not the open project, to branch and opens it.
func (w *Workspace) OpenBranch(ctx context.Context, dir, branch string) error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	if p, m := w.Current(); p != nil && p.Root == dir {
		return errors.New("this project is already open; change its branch from the branch menu")
	} else if m != nil && !m.Idle() {
		return errWorkspaceRunning
	}
	if _, err := gitrepo.New(dir, w.runner).SwitchBranch(ctx, branch); err != nil {
		return err
	}
	return w.openReserved(dir)
}

// setupFile is the file that makes a folder a mockstro project.
func setupFile(dir string) string { return filepath.Join(dir, ".maestro", "mockstro.yaml") }

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

type noMaestroError string

func (e noMaestroError) Error() string {
	return string(e) + " is not set up for Mockstro: it has no .maestro/mockstro.yaml. Choose the project folder that contains one"
}
func (noMaestroError) Unwrap() error { return project.ErrNotFound }

// Recent lists remembered projects, newest first, then clones never opened, such as one whose
// branch has no .maestro folder yet.
func (w *Workspace) Recent() []RecentProject {
	paths := w.recents.load()
	if dir := w.ClonesDir(); dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if p := filepath.Join(dir, e.Name()); e.IsDir() && !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	out := []RecentProject{}
	for _, path := range paths {
		folder, err := os.Stat(path)
		out = append(out, RecentProject{Path: path, Name: filepath.Base(path), Exists: isFile(setupFile(path)),
			Folder: err == nil && folder.IsDir(), Managed: w.managed(path)})
	}
	return out
}

// managed reports whether path is a folder directly inside ClonesDir.
func (w *Workspace) managed(path string) bool {
	dir := w.ClonesDir()
	return dir != "" && filepath.IsAbs(path) && filepath.Dir(filepath.Clean(path)) == dir && filepath.Base(path) != "."
}

var errOpenProject = errors.New("this project is open; open another one before removing it")

// Remove forgets a project. With deleteFiles it also deletes the folder, which is allowed only
// for clones in ClonesDir, never for a folder the tester chose.
func (w *Workspace) Remove(path string, deleteFiles bool) error {
	release, err := w.operations.Reserve()
	if err != nil {
		return err
	}
	defer release()
	path = filepath.Clean(path)
	if p, _ := w.Current(); p != nil && p.Root == path {
		return errOpenProject
	}
	if deleteFiles {
		if !w.managed(path) {
			return errors.New("Mockstro only deletes projects it cloned; remove this one from the list instead")
		}
		st, err := os.Lstat(path)
		if err == nil && !st.IsDir() {
			return fmt.Errorf("%s is not a folder", path)
		}
		if err == nil {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	w.recents.remove(path)
	return nil
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
	return integrationBranches(ctx, repo)
}

// Both project setup and the version switcher offer only integrated branches.
func integrationBranches(ctx context.Context, repo *gitrepo.Repository) ([]gitrepo.Branch, error) {
	all, err := repo.Branches(ctx)
	if err != nil {
		return nil, err
	}
	with, err := repo.BranchesWithPath(ctx, ".maestro/mockstro.yaml")
	if err != nil {
		return nil, err
	}
	out := []gitrepo.Branch{}
	for _, b := range all {
		if with[b.Name] {
			out = append(out, b)
		}
	}
	return out, nil
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
