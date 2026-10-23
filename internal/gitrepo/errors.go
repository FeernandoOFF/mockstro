package gitrepo

import "errors"

var (
	ErrCommand        = errors.New("git command failed")
	ErrNotRepository  = errors.New("not a git checkout")
	ErrDirty          = errors.New("tracked files have local changes")
	ErrInvalidBranch  = errors.New("invalid branch name")
	ErrInvalidURL     = errors.New("invalid repository address")
	ErrNotFound       = errors.New("repository not found")
	ErrNetwork        = errors.New("remote unreachable")
	ErrNoGit          = errors.New("git is not installed")
	ErrHostKey        = errors.New("ssh host is not trusted")
	ErrExists         = errors.New("destination already exists")
	ErrAuthentication = errors.New("git authentication failed")
	ErrDiverged       = errors.New("history cannot fast-forward")
	ErrNoUpstream     = errors.New("branch has no upstream")
)

// Error classifies a failure independently of its user-facing explanation while
// retaining the original command error for diagnostics and cancellation checks.
type Error struct {
	Kind    error
	Message string
	Cause   error
}

func (e *Error) Error() string        { return e.Message }
func (e *Error) Unwrap() error        { return e.Cause }
func (e *Error) Is(target error) bool { return target == e.Kind }
