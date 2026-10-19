// Package bootstrap installs project launchers and the skill bundled with the CLI.
package bootstrap

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/feernandooff/mockstro/skills"
)

//go:embed mockstrow
var Wrapper []byte

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Project adds a wrapper and explicit pin to an existing .maestro project.
// Existing files are never replaced, including symlinks.
func Project(root, version string) error {
	if !releaseTag.MatchString(version) {
		return fmt.Errorf("release must be a tag such as v1.2.3 (got %q)", version)
	}
	st, err := os.Stat(filepath.Join(root, ".maestro"))
	if err != nil || !st.IsDir() {
		return fmt.Errorf("bootstrap requires an existing .maestro directory in %s", root)
	}
	for _, name := range []string{"mockstrow", filepath.Join(".maestro", ".mockstro-version")} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			return fmt.Errorf("refusing to overwrite %s", filepath.Join(root, name))
		}
	}
	// Write the pin first; remove only our new pin if writing the wrapper fails.
	pin := filepath.Join(root, ".maestro", ".mockstro-version")
	if err := create(pin, []byte(version+"\n"), 0644); err != nil {
		return err
	}
	if err := create(filepath.Join(root, "mockstrow"), Wrapper, 0755); err != nil {
		os.Remove(pin)
		return err
	}
	return nil
}

func create(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(path)
	}
	return closeErr
}

// Skill installs one canonical project skill. Claude uses a relative compatibility
// symlink to the same files; existing installs are left for the user to manage.
func Skill(root string, claude bool) error {
	dest := filepath.Join(root, ".agents", "skills", "mockstro")
	alias := filepath.Join(root, ".claude", "skills", "mockstro")
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("refusing to overwrite %s", dest)
	}
	if claude {
		if _, err := os.Lstat(alias); !os.IsNotExist(err) {
			return fmt.Errorf("refusing to overwrite %s", alias)
		}
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".mockstro-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = fs.WalkDir(skills.Files, "mockstro", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("mockstro", path)
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := skills.Files.ReadFile(path)
		if err != nil {
			return err
		}
		return create(target, data, 0644)
	})
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if claude {
		if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
			return err
		}
		return os.Symlink("../../.agents/skills/mockstro", alias)
	}
	return nil
}
