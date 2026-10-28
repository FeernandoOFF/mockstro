// Package gitrepo provides non-interactive Git operations for a single checkout.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
)

const (
	maxBranches   = 60
	maxDirtyShown = 10
)

// Repository is a checkout and the command runner used to access it. Callers must
// serialize mutations with other users of the checkout, including mock startup.
type Repository struct {
	root string
	ex   execx.Runner
}

func New(root string, ex execx.Runner) *Repository {
	return &Repository{root: root, ex: ex}
}

// Status describes the local checkout without contacting its remotes.
type Status struct {
	Repo     bool     `json:"repo"`
	Branch   string   `json:"branch,omitempty"`
	Detached bool     `json:"detached,omitempty"`
	Upstream string   `json:"upstream,omitempty"`
	Ahead    int      `json:"ahead"`
	Behind   int      `json:"behind"`
	Dirty    []string `json:"dirty"`
	Commit   *Commit  `json:"commit,omitempty"`
}

type Commit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	When    string `json:"when"`
}

// Branch describes a local branch or an available remote tracking branch.
type Branch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Remote  bool   `json:"remote"` // only on the remote so far; switching creates a local copy
	When    string `json:"when"`
}

var (
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]*$`)

	signIn    = regexp.MustCompile(`(?i)could not read from remote|permission denied|authentication failed|could not read username|could not read password|terminal prompts disabled|returned error: 40[13]|invalid username or (password|token)`)
	notFound  = regexp.MustCompile(`(?i)repository not found|does not appear to be a git repository|returned error: 404|repository .* does not exist`)
	offline   = regexp.MustCompile(`(?i)could not resolve host|failed to connect|could not connect|connection timed out|operation timed out|network is unreachable|connection refused`)
	hostKey   = regexp.MustCompile(`(?i)host key verification failed|remote host identification has changed`)
	noGit     = regexp.MustCompile(`(?i)xcrun: error|xcode-select|no developer tools`)
	noFF      = regexp.MustCompile(`(?i)not possible to fast-forward|diverging branches|divergent branches|need to specify how to reconcile`)
	noTracked = regexp.MustCompile(`(?i)no tracking information|no upstream|not currently on a branch`)
)

func friendlyError(stderr string) string {
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

// sshCommand trusts a host the first time it is seen, as answering "yes" would, but still
// refuses a host whose key has changed.
const sshCommand = "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new"

// command never invokes a shell or allows interactive authentication.
func (r *Repository) command(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-c", "core.quotepath=false", "-C", r.root}, args...)
	out, err := r.ex.Output(ctx, execx.Cmd{Name: "git", Args: full,
		Env: []string{"GIT_TERMINAL_PROMPT=0", sshCommand, "LC_ALL=C"}})
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var ee *execx.Error
	if errors.As(err, &ee) && ee.Stderr != "" {
		kind := ErrCommand
		switch {
		// ssh says "could not read from remote" for an untrusted host or a missing repository too.
		case hostKey.MatchString(ee.Stderr):
			kind = ErrHostKey
		case notFound.MatchString(ee.Stderr):
			kind = ErrNotFound
		case signIn.MatchString(ee.Stderr):
			kind = ErrAuthentication
		case noFF.MatchString(ee.Stderr):
			kind = ErrDiverged
		case noTracked.MatchString(ee.Stderr):
			kind = ErrNoUpstream
		case strings.Contains(strings.ToLower(ee.Stderr), "not a git repository"):
			kind = ErrNotRepository
		case offline.MatchString(ee.Stderr):
			kind = ErrNetwork
		case noGit.MatchString(ee.Stderr):
			kind = ErrNoGit
		}
		return out, &Error{Kind: kind, Message: friendlyError(ee.Stderr), Cause: err}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return out, &Error{Kind: ErrNoGit, Message: "Git is not installed", Cause: err}
	}
	return out, &Error{Kind: ErrCommand, Message: err.Error(), Cause: err}
}

func (r *Repository) Status(ctx context.Context) (Status, error) {
	st := Status{Dirty: []string{}}
	if _, err := r.command(ctx, "rev-parse", "--show-toplevel"); err != nil {
		if errors.Is(err, ErrNotRepository) {
			return st, nil
		}
		return st, err
	}
	st.Repo = true
	if b, err := r.command(ctx, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && b != "" {
		st.Branch = b
	} else {
		st.Detached = true
		st.Branch, _ = r.command(ctx, "rev-parse", "--short", "HEAD")
	}
	if up, err := r.command(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && up != "" {
		st.Upstream = up
		if counts, err := r.command(ctx, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			if f := strings.Fields(counts); len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(f[0])
				st.Behind, _ = strconv.Atoi(f[1])
			}
		}
	}
	// Tracked files that differ from HEAD, staged or not; untracked files (saved stubs) never count.
	if out, err := r.command(ctx, "diff", "--name-only", "-z", "HEAD"); err != nil {
		return st, err
	} else if out != "" {
		st.Dirty = strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	}
	if out, err := r.command(ctx, "log", "-1", "--format=%h%x1f%s%x1f%cr"); err == nil {
		if f := strings.Split(out, "\x1f"); len(f) == 3 {
			st.Commit = &Commit{Hash: f[0], Subject: f[1], When: f[2]}
		}
	}
	return st, nil
}

// clean refuses tracked edits, staged or unstaged. Untracked saved stubs do not
// prevent a pull or switch. A failed dirty check is never treated as clean.
func (r *Repository) clean(ctx context.Context) error {
	st, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Repo {
		return &Error{Kind: ErrNotRepository, Message: "this project folder is not a git checkout"}
	}
	if len(st.Dirty) > 0 {
		shown := st.Dirty
		if len(shown) > maxDirtyShown {
			shown = append(shown[:maxDirtyShown:maxDirtyShown], fmt.Sprintf("and %d more", len(st.Dirty)-maxDirtyShown))
		}
		return &Error{Kind: ErrDirty, Message: fmt.Sprintf("these files have changes that would be lost or clash: %s. Ask a developer", strings.Join(shown, ", "))}
	}
	return nil
}

func (r *Repository) Fetch(ctx context.Context) (Status, error) {
	if _, err := r.command(ctx, "fetch", "--prune"); err != nil {
		return Status{}, err
	}
	return r.Status(ctx)
}

// Pull only fast-forwards; it never merges, stashes, resets or discards edits.
func (r *Repository) Pull(ctx context.Context) (string, Status, error) {
	if err := r.clean(ctx); err != nil {
		return "", Status{}, err
	}
	out, err := r.command(ctx, "pull", "--ff-only")
	if err != nil {
		return "", Status{}, err
	}
	st, err := r.Status(ctx)
	return firstLine(out), st, err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func (r *Repository) Branches(ctx context.Context) ([]Branch, error) {
	out, err := r.command(ctx, "for-each-ref", "--sort=-committerdate", "--format=%(refname)%1f%(committerdate:relative)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	st, err := r.Status(ctx)
	if err != nil {
		return nil, err
	}
	current := st.Branch
	local := map[string]bool{}
	var rows []Branch
	for _, line := range strings.Split(out, "\n") {
		ref, when, _ := strings.Cut(line, "\x1f")
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			local[name] = true
			rows = append(rows, Branch{Name: name, Current: name == current, When: when})
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
		rows = append(rows, Branch{Name: name, Remote: true, When: when})
	}
	if len(rows) > maxBranches {
		rows = rows[:maxBranches]
	}
	if rows == nil {
		rows = []Branch{}
	}
	return rows, nil
}

// BranchesWithPath names the branches, as Branches names them, whose tip contains path.
func (r *Repository) BranchesWithPath(ctx context.Context, path string) (map[string]bool, error) {
	out, err := r.command(ctx, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	// A local branch wins over its remote copy, just as it does in Branches.
	local := map[string]bool{}
	for _, ref := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			local[name] = true
		}
	}
	found := map[string]bool{}
	for _, ref := range strings.Split(out, "\n") {
		name, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			rest, remote := strings.CutPrefix(ref, "refs/remotes/")
			if !remote {
				continue
			}
			if _, name, ok = strings.Cut(rest, "/"); !ok || name == "HEAD" || local[name] {
				continue
			}
		}
		if found[name] {
			continue
		}
		if tree, err := r.command(ctx, "ls-tree", "--name-only", ref, "--", path); err == nil && tree != "" {
			found[name] = true
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return found, nil
}

func (r *Repository) SwitchBranch(ctx context.Context, name string) (Status, error) {
	invalid := func() error {
		return &Error{Kind: ErrInvalidBranch, Message: fmt.Sprintf("%q is not a branch name", name)}
	}
	if !branchName.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return Status{}, invalid()
	}
	if _, err := r.command(ctx, "check-ref-format", "--branch", name); err != nil {
		if ctx.Err() != nil {
			return Status{}, ctx.Err()
		}
		if execx.ExitCode(err) > 0 {
			return Status{}, invalid()
		}
		return Status{}, err
	}
	if err := r.clean(ctx); err != nil {
		return Status{}, err
	}
	if _, err := r.command(ctx, "switch", name); err != nil {
		return Status{}, err
	}
	return r.Status(ctx)
}
