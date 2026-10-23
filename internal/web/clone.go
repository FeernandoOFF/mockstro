package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/gitrepo"
	"github.com/feernandooff/mockstro/internal/project"
)

// Guide is a help page the page links to when a clone fails.
type Guide struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// cloneFailure tells the page what went wrong and how to fix it; Error keeps the shape of other errors.
type cloneFailure struct {
	Error    string  `json:"error"`
	Kind     string  `json:"kind"`
	Host     string  `json:"host,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Path     string  `json:"path,omitempty"`
	Command  string  `json:"command,omitempty"`
	Guides   []Guide `json:"guides,omitempty"`
	Detail   string  `json:"detail,omitempty"`
}

var signInGuides = map[string][]Guide{
	"github.com": {
		{"Sign in to GitHub from Git", "https://docs.github.com/en/get-started/git-basics/caching-your-github-credentials-in-git"},
		{"Create a GitHub personal access token", "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens"},
		{"Use SSH keys with GitHub", "https://docs.github.com/en/authentication/connecting-to-github-with-ssh"},
	},
	"bitbucket.org": {
		{"Create a Bitbucket API token", "https://support.atlassian.com/bitbucket-cloud/docs/create-an-api-token/"},
		{"Use a Bitbucket API token with Git", "https://support.atlassian.com/bitbucket-cloud/docs/using-api-tokens/"},
		{"Use SSH keys with Bitbucket", "https://support.atlassian.com/bitbucket-cloud/docs/set-up-personal-ssh-keys-on-macos/"},
	},
}

var hostKeyGuides = map[string]Guide{
	"github.com":    {"GitHub's SSH fingerprints", "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints"},
	"bitbucket.org": {"Bitbucket's SSH fingerprints", "https://support.atlassian.com/bitbucket-cloud/docs/configure-ssh-and-two-step-verification/"},
}

// provider maps hosts such as ssh.bitbucket.org or ssh.github.com to the site whose guides apply.
func provider(host string) string {
	for site := range signInGuides {
		if host == site || strings.HasSuffix(host, "."+site) {
			return site
		}
	}
	return ""
}

var genericGuides = []Guide{{"How Git remembers sign-ins", "https://git-scm.com/docs/gitcredentials"}}

// remoteHost is the host of an https, ssh or scp-style address, or "" for a local path.
func remoteHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	if at := strings.Index(raw, "@"); at >= 0 {
		if host, _, ok := strings.Cut(raw[at+1:], ":"); ok {
			return strings.ToLower(host)
		}
	}
	return ""
}

// signInCommand checks access without cloning; over https Git asks for the sign-in and the
// Mac's keychain keeps it. Any password typed into the address is dropped.
func signInCommand(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		raw = u.String()
	}
	return "git ls-remote " + shellQuote(raw)
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_-~") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func describeCloneFailure(rawURL, dest string, err error) (int, cloneFailure) {
	f := cloneFailure{Error: err.Error(), Kind: "other", Host: remoteHost(rawURL)}
	f.Provider = provider(f.Host)
	var ee *execx.Error
	if errors.As(err, &ee) {
		f.Detail = ee.Stderr
	}
	guides := signInGuides[f.Provider]
	if guides == nil {
		guides = genericGuides
	}
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, gitrepo.ErrInvalidURL):
		f.Kind, status = "invalid", http.StatusBadRequest
	case errors.Is(err, gitrepo.ErrExists):
		f.Kind, f.Path, status = "exists", dest, http.StatusConflict
	case errors.Is(err, errWorkspaceRunning):
		f.Kind, status = "running", http.StatusConflict
	case errors.Is(err, project.ErrNotFound):
		f.Kind, f.Path, status = "notProject", dest, http.StatusUnprocessableEntity
	case errors.Is(err, gitrepo.ErrHostKey):
		f.Kind = "hostKey"
		if g, ok := hostKeyGuides[f.Provider]; ok {
			f.Guides = []Guide{g}
		}
	case errors.Is(err, gitrepo.ErrAuthentication):
		f.Kind, f.Guides, f.Command = "auth", guides, signInCommand(rawURL)
	case errors.Is(err, gitrepo.ErrNotFound):
		f.Kind, f.Guides, f.Command = "notFound", guides, signInCommand(rawURL)
	case errors.Is(err, gitrepo.ErrNetwork):
		f.Kind = "network"
	case errors.Is(err, gitrepo.ErrNoGit):
		f.Kind, f.Command = "noGit", "xcode-select --install"
	case errors.Is(err, context.DeadlineExceeded):
		f.Kind, f.Error = "timeout", "the clone took longer than 15 minutes and was stopped"
	case dest != "":
		f.Kind, f.Path, status = "openFailed", dest, http.StatusUnprocessableEntity
	}
	return status, f
}

// cloneProject clones a repository into mockstro's projects folder and opens it.
func (s *Server) cloneProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	address := gitrepo.Address(req.URL)
	dest, err := s.ws.Clone(ctx, address)
	if err != nil {
		status, f := describeCloneFailure(address, dest, err)
		writeJSON(w, status, f)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": dest})
}

// setupBranches lists the branches of a checkout that are set up for mockstro.
func (s *Server) setupBranches(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, errors.New("path must be absolute"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	branches, err := s.ws.SetupBranches(ctx, path)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches})
}

// openBranch switches a checkout that is not open to a branch, then opens it.
func (s *Server) openBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, errors.New("path must be absolute"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	if err := s.ws.OpenBranch(ctx, req.Path, req.Branch); err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
