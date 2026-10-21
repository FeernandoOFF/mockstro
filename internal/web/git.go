package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
)

// The UI can update the project's checkout so a tester does not need git. It is deliberately
// narrow: pulls are fast-forward only, nothing is ever merged, stashed, reset or discarded, and
// both pull and switch refuse while tracked files have local changes or a mock is running.

const (
	gitTimeout    = 60 * time.Second
	maxBranches   = 60
	maxDirtyShown = 10
)

// GitStatus is what the page's version dialog and header chip show.
type GitStatus struct {
	Repo     bool       `json:"repo"`
	Branch   string     `json:"branch,omitempty"`
	Detached bool       `json:"detached,omitempty"`
	Upstream string     `json:"upstream,omitempty"`
	Ahead    int        `json:"ahead"`
	Behind   int        `json:"behind"`
	Dirty    []string   `json:"dirty"`
	Commit   *GitCommit `json:"commit,omitempty"`
}

type GitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}

// GitBranch is one entry in the branch picker.
type GitBranch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Remote  bool   `json:"remote"` // only on the remote so far; switching creates a local copy
	When    string `json:"when"`
}

var (
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]*$`)

	signIn    = regexp.MustCompile(`(?i)could not read from remote|permission denied|authentication failed|could not read username|terminal prompts disabled|host key verification`)
	noFF      = regexp.MustCompile(`(?i)not possible to fast-forward|diverging branches|divergent branches|need to specify how to reconcile`)
	noTracked = regexp.MustCompile(`(?i)no tracking information|no upstream|not currently on a branch`)
)

func friendlyGitError(stderr string) string {
	switch {
	case signIn.MatchString(stderr):
		return "Git could not sign in to the remote. Run git pull once in a terminal to sign in, or ask a developer."
	case noFF.MatchString(stderr):
		return "Your copy and the remote have both changed, so it cannot update by itself. Ask a developer."
	case noTracked.MatchString(stderr):
		return "This branch is not linked to a remote branch, so there is nothing to pull."
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, " ")
}

// git runs one non-interactive git command in the project's checkout. Arguments are never
// passed through a shell.
func (s *Server) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.quotepath=false", "-C", dir}, args...)
	out, err := s.ex.Output(ctx, execx.Cmd{Name: "git", Args: full,
		Env: []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "LC_ALL=C"}})
	if err != nil {
		var ee *execx.Error
		if errors.As(err, &ee) && ee.Stderr != "" {
			return out, errors.New(friendlyGitError(ee.Stderr))
		}
		return out, err
	}
	return out, nil
}

func (s *Server) gitStatus(ctx context.Context, dir string) GitStatus {
	st := GitStatus{Dirty: []string{}}
	if _, err := s.git(ctx, dir, "rev-parse", "--show-toplevel"); err != nil {
		return st
	}
	st.Repo = true
	if b, err := s.git(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && b != "" {
		st.Branch = b
	} else {
		st.Detached = true
		st.Branch, _ = s.git(ctx, dir, "rev-parse", "--short", "HEAD")
	}
	if up, err := s.git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && up != "" {
		st.Upstream = up
		if counts, err := s.git(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			if f := strings.Fields(counts); len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(f[0])
				st.Behind, _ = strconv.Atoi(f[1])
			}
		}
	}
	// Tracked files that differ from HEAD, staged or not; untracked files (saved stubs) never count.
	if out, err := s.git(ctx, dir, "diff", "--name-only", "HEAD"); err == nil && out != "" {
		st.Dirty = strings.Split(out, "\n")
	}
	if out, err := s.git(ctx, dir, "log", "-1", "--format=%h%x1f%s%x1f%cr"); err == nil {
		if f := strings.Split(out, "\x1f"); len(f) == 3 {
			st.Commit = &GitCommit{Hash: f[0], Subject: f[1], When: f[2]}
		}
	}
	return st
}

// gitGate answers 409 and reports false when the checkout must not change right now.
func (s *Server) gitGate(w http.ResponseWriter, ctx context.Context, dir string, m *Manager) bool {
	if !m.Idle() {
		writeError(w, http.StatusConflict, errors.New("stop the running mock first; changing the project's files under it would confuse it"))
		return false
	}
	st := s.gitStatus(ctx, dir)
	if !st.Repo {
		writeError(w, http.StatusConflict, errors.New("this project folder is not a git checkout"))
		return false
	}
	if len(st.Dirty) > 0 {
		shown := st.Dirty
		if len(shown) > maxDirtyShown {
			shown = append(shown[:maxDirtyShown:maxDirtyShown], fmt.Sprintf("and %d more", len(st.Dirty)-maxDirtyShown))
		}
		writeError(w, http.StatusConflict, fmt.Errorf("these files have changes that would be lost or clash: %s. Ask a developer", strings.Join(shown, ", ")))
		return false
	}
	return true
}

func (s *Server) gitLock(w http.ResponseWriter) bool {
	if !s.gitMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("another git action is still running"))
		return false
	}
	return true
}

func (s *Server) gitInfo(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.gitStatus(ctx, p.Root))
}

func (s *Server) gitFetch(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok || !s.gitLock(w) {
		return
	}
	defer s.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	if _, err := s.git(ctx, p.Root, "fetch", "--prune"); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, s.gitStatus(ctx, p.Root))
}

func (s *Server) gitPull(w http.ResponseWriter, r *http.Request) {
	p, m, ok := s.cur(w)
	if !ok || !s.gitLock(w) {
		return
	}
	defer s.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	if !s.gitGate(w, ctx, p.Root, m) {
		return
	}
	out, err := s.git(ctx, p.Root, "pull", "--ff-only")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": firstLine(out), "status": s.gitStatus(ctx, p.Root)})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func (s *Server) gitBranches(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := s.git(ctx, p.Root, "for-each-ref", "--sort=-committerdate", "--format=%(refname)%1f%(committerdate:relative)", "refs/heads", "refs/remotes")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	current := s.gitStatus(ctx, p.Root).Branch
	local := map[string]bool{}
	var rows []GitBranch
	for _, line := range strings.Split(out, "\n") {
		ref, when, _ := strings.Cut(line, "\x1f")
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			local[name] = true
			rows = append(rows, GitBranch{Name: name, Current: name == current, When: when})
		}
	}
	for _, line := range strings.Split(out, "\n") {
		ref, when, _ := strings.Cut(line, "\x1f")
		rest, ok := strings.CutPrefix(ref, "refs/remotes/")
		if !ok {
			continue
		}
		_, name, found := strings.Cut(rest, "/") // drop the remote's name
		if !found || name == "HEAD" || local[name] {
			continue
		}
		local[name] = true
		rows = append(rows, GitBranch{Name: name, Remote: true, When: when})
	}
	if len(rows) > maxBranches {
		rows = rows[:maxBranches]
	}
	if rows == nil {
		rows = []GitBranch{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) gitSwitch(w http.ResponseWriter, r *http.Request) {
	p, m, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !branchName.MatchString(req.Branch) || strings.Contains(req.Branch, "..") || strings.HasSuffix(req.Branch, "/") || strings.HasSuffix(req.Branch, ".lock") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a branch name", req.Branch))
		return
	}
	if !s.gitLock(w) {
		return
	}
	defer s.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	if !s.gitGate(w, ctx, p.Root, m) {
		return
	}
	if _, err := s.git(ctx, p.Root, "check-ref-format", "--branch", req.Branch); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a branch name", req.Branch))
		return
	}
	if _, err := s.git(ctx, p.Root, "switch", req.Branch); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, s.gitStatus(ctx, p.Root))
}
