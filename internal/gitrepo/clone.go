package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
)

var (
	remoteURL = regexp.MustCompile(`^(https://|ssh://)[^\s]+$|^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Address accepts a pasted "git clone [options] <address> [folder]" command as well as a bare
// address, and returns just the address.
func Address(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) < 2 || fields[0] != "git" || fields[1] != "clone" {
		return strings.TrimSpace(raw)
	}
	for _, f := range fields[2:] {
		if !strings.HasPrefix(f, "-") {
			return f
		}
	}
	return ""
}

// CloneName is the folder name a clone of url gets: its last path segment without ".git".
func CloneName(url string) (string, error) {
	url = Address(url)
	if !remoteURL.MatchString(url) && !filepath.IsAbs(url) {
		return "", &Error{Kind: ErrInvalidURL, Message: "paste the repository's https:// or git@ address"}
	}
	trimmed := strings.TrimRight(url, "/")
	name := strings.TrimSuffix(trimmed[strings.LastIndexAny(trimmed, "/:")+1:], ".git")
	if !repoName.MatchString(name) {
		return "", &Error{Kind: ErrInvalidURL, Message: fmt.Sprintf("cannot name a folder after %q", url)}
	}
	return name, nil
}

// Clone checks out url into parent/<name> and returns the new folder. It never overwrites.
func Clone(ctx context.Context, ex execx.Runner, url, parent string) (string, error) {
	name, err := CloneName(url)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(parent, name)
	if _, err := os.Lstat(dest); err == nil {
		return dest, &Error{Kind: ErrExists, Message: fmt.Sprintf("%s already exists; open it from the folder chooser", dest)}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	if _, err := New(parent, ex).command(ctx, "clone", "--", Address(url), name); err != nil {
		return "", err
	}
	return dest, nil
}
