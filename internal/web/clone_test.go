package web

import (
	"errors"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/gitrepo"
)

func gitFailure(kind error, stderr string) error {
	return &gitrepo.Error{Kind: kind, Message: "m", Cause: &execx.Error{Err: errors.New("exit status 128"), Stderr: stderr}}
}

func TestCloneFailureNamesTheHostsSignInGuides(t *testing.T) {
	_, f := describeCloneFailure("https://me:secret@bitbucket.org/team/app.git", "", gitFailure(gitrepo.ErrAuthentication, "fatal: could not read Password"))
	if f.Kind != "auth" || f.Host != "bitbucket.org" || !strings.Contains(f.Guides[0].URL, "atlassian.com") || f.Detail == "" {
		t.Fatalf("bitbucket = %+v", f)
	}
	if f.Command != "git ls-remote https://me@bitbucket.org/team/app.git" {
		t.Fatalf("command kept the password or lost the user: %q", f.Command)
	}
	_, f = describeCloneFailure("git@github.com:team/app.git", "", gitFailure(gitrepo.ErrNotFound, "ERROR: Repository not found."))
	if f.Kind != "notFound" || f.Host != "github.com" || !strings.Contains(f.Guides[0].URL, "docs.github.com") {
		t.Fatalf("github = %+v", f)
	}
	_, f = describeCloneFailure("https://git.example.com/app.git", "", gitFailure(gitrepo.ErrAuthentication, ""))
	if len(f.Guides) != 1 || !strings.Contains(f.Guides[0].URL, "git-scm.com") {
		t.Fatalf("other host = %+v", f)
	}
	if got := signInCommand("https://host/it's app"); got != `git ls-remote 'https://host/it'\''s app'` {
		t.Fatalf("quoting = %q", got)
	}
}

func TestHostKeyFailureForAProviderSubdomain(t *testing.T) {
	_, f := describeCloneFailure("git@ssh.bitbucket.org:team/app.git", "", gitFailure(gitrepo.ErrHostKey, "Host key verification failed."))
	if f.Kind != "hostKey" || f.Host != "ssh.bitbucket.org" || f.Provider != "bitbucket.org" || len(f.Guides) != 1 {
		t.Fatalf("host key = %+v", f)
	}
	if _, f := describeCloneFailure("https://ssh.github.com/a/b", "", gitFailure(gitrepo.ErrAuthentication, "")); f.Provider != "github.com" || len(f.Guides) != 3 {
		t.Fatalf("github subdomain = %+v", f)
	}
}

func TestCloneFailureKinds(t *testing.T) {
	for want, err := range map[string]error{
		"network": gitFailure(gitrepo.ErrNetwork, "Could not resolve host: bitbucket.org"),
		"noGit":   &gitrepo.Error{Kind: gitrepo.ErrNoGit},
		"running": errWorkspaceRunning,
		"invalid": &gitrepo.Error{Kind: gitrepo.ErrInvalidURL},
	} {
		if _, f := describeCloneFailure("https://bitbucket.org/a/b", "", err); f.Kind != want {
			t.Errorf("%v: kind %q, want %q", err, f.Kind, want)
		}
	}
	if _, f := describeCloneFailure("https://bitbucket.org/a/b", "/p/b", errors.New("bad config")); f.Kind != "openFailed" || f.Path != "/p/b" {
		t.Errorf("open failure = %+v", f)
	}
}
