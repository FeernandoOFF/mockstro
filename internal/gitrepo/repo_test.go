package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func checkout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "tracked"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "tracked")
	git(t, dir, "commit", "-qm", "initial")
	return dir
}

func TestTrackedEditsRefuseMutationIncludingStagedChanges(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstaged", true: "staged"}[staged], func(t *testing.T) {
			dir := checkout(t)
			git(t, dir, "branch", "other")
			if err := os.WriteFile(filepath.Join(dir, "tracked"), []byte("edited\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if staged {
				git(t, dir, "add", "tracked")
			}
			repo := New(dir, execx.Real{})
			if _, _, err := repo.Pull(context.Background()); !errors.Is(err, ErrDirty) {
				t.Fatalf("pull: %v", err)
			}
			if _, err := repo.SwitchBranch(context.Background(), "other"); !errors.Is(err, ErrDirty) {
				t.Fatalf("switch: %v", err)
			}
			if got := git(t, dir, "branch", "--show-current"); got != "main" {
				t.Fatalf("branch = %q", got)
			}
		})
	}
}

func TestUntrackedFilesAndDetachedStatus(t *testing.T) {
	dir := checkout(t)
	if err := os.WriteFile(filepath.Join(dir, "saved-stub.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "switch", "--detach", "-q")
	st, err := New(dir, execx.Real{}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Repo || !st.Detached || st.Branch == "" || len(st.Dirty) != 0 || st.Commit == nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestFailuresHaveTypedClassificationAndRetainCause(t *testing.T) {
	for _, tc := range []struct {
		stderr  string
		kind    error
		message string
	}{
		{"git@bitbucket.org: Permission denied (publickey).\nfatal: Could not read from remote repository.", ErrAuthentication, "could not sign in"},
		{"fatal: Not possible to fast-forward, aborting.", ErrDiverged, "both changed"},
		{"There is no tracking information for the current branch.", ErrNoUpstream, "not linked to a remote branch"},
		{"fatal: not a git repository (or any of the parent directories): .git", ErrNotRepository, "not a git repository"},
		{"error: something unexpected", ErrCommand, "something unexpected"},
	} {
		t.Run(tc.kind.Error(), func(t *testing.T) {
			cause := errors.New("exit status 128")
			fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", &execx.Error{Err: cause, Stderr: tc.stderr} }}
			_, err := New("/checkout", fake).Fetch(context.Background())
			var typed *Error
			if !errors.Is(err, tc.kind) || !errors.Is(err, cause) || !errors.As(err, &typed) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDirtyCheckFailureNeverAdmitsPull(t *testing.T) {
	cause := errors.New("cannot read index")
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		switch c.Args[4] {
		case "rev-parse":
			return "main", nil
		case "diff":
			return "", cause
		default:
			return "", nil
		}
	}}
	_, _, err := New("/checkout", fake).Pull(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v", err)
	}
	for _, call := range fake.Calls {
		if strings.Contains(call, " pull ") {
			t.Fatalf("pull ran after dirty check failed: %s", call)
		}
	}
}

func TestBranchValidationBeforeCommandsAndGitFormatValidation(t *testing.T) {
	fake := &execx.Fake{}
	repo := New("/checkout", fake)
	for _, name := range []string{"", "--detach", "-f", "a..b", "x y", "main/", "bad.lock", "$(touch x)", "a;b", "--orphan=x"} {
		if _, err := repo.SwitchBranch(context.Background(), name); !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("unsafe names reached git: %v", fake.Calls)
	}
	if _, err := New(checkout(t), execx.Real{}).SwitchBranch(context.Background(), "a//b"); !errors.Is(err, ErrInvalidBranch) {
		t.Fatalf("git format validation: %v", err)
	}
}

func TestCommandArgumentsAndNoninteractiveEnvironment(t *testing.T) {
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.Name != "git" || !reflect.DeepEqual(c.Args[:4], []string{"-c", "core.quotepath=false", "-C", "/checkout with spaces"}) {
			t.Fatalf("command = %+v", c)
		}
		want := []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "LC_ALL=C"}
		if !reflect.DeepEqual(c.Env, want) {
			t.Fatalf("env = %v", c.Env)
		}
		if c.Args[4] == "rev-parse" {
			return "", &execx.Error{Err: errors.New("exit status 128"), Stderr: "fatal: not a git repository"}
		}
		return "", nil
	}}
	st, err := New("/checkout with spaces", fake).Status(context.Background())
	if err != nil || st.Repo || st.Dirty == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
}
