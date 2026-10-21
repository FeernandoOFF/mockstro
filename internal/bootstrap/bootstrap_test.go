package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/project"
)

const testSuite = "# keep suite comment\nplatforms:\n  android:\n    appId: example.android # keep app comment\n    integration: full\n    devices: [Agent_AVD]\n"

func writeSuite(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".maestro", "mockstro.yaml"), []byte(testSuite), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectPreservesConfigAndExistingFiles(t *testing.T) {
	root := t.TempDir()
	if err := Project(root, "v1.2.3"); err == nil {
		t.Fatal("accepted missing .maestro")
	}
	if err := os.Mkdir(filepath.Join(root, ".maestro"), 0755); err != nil {
		t.Fatal(err)
	}
	writeSuite(t, root)
	config := filepath.Join(root, ".maestro", "config.yaml")
	os.WriteFile(config, []byte("existing config"), 0644)
	for _, bad := range []string{"dev", "latest", "v1.2.3/../../other", "v1.2.3\nv2.0.0"} {
		if err := Project(root, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := Project(root, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := Project(root, "v2.0.0"); err == nil {
		t.Fatal("overwrote installation")
	}
	got, err := os.ReadFile(config)
	if err != nil || string(got) != "existing config" {
		t.Fatalf("%s: %q, %v", config, got, err)
	}
	pin := filepath.Join(root, ".maestro", "mockstro.yaml")
	data, err := os.ReadFile(pin)
	if err != nil || !strings.Contains(string(data), "cliVersion: v1.2.3") || !strings.Contains(string(data), "# keep suite comment") || !strings.Contains(string(data), "# keep app comment") {
		t.Fatal("pin or existing comments lost", string(data), err)
	}
	p, err := project.Load(root)
	if err != nil || p.AppID != "example.android" || p.Lite() || p.Devices[0] != "Agent_AVD" {
		t.Fatal("changed existing settings", p, err)
	}
	info, _ := os.Stat(pin)
	if info.Mode().Perm() != 0600 {
		t.Fatal("changed config permissions")
	}
	if _, err := os.Stat(filepath.Join(root, ".maestro", ".mockstro-version")); !os.IsNotExist(err) {
		t.Fatal("created a separate pin")
	}
	st, _ := os.Stat(filepath.Join(root, "mockstrow"))
	if st.Mode()&0111 == 0 {
		t.Fatal("wrapper not executable")
	}
}

func TestProjectRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".maestro"), 0755)
	writeSuite(t, root)
	os.Symlink("missing", filepath.Join(root, "mockstrow"))
	if err := Project(root, "v1.2.3"); err == nil {
		t.Fatal("accepted existing symlink")
	}
	if got, _ := os.ReadFile(filepath.Join(root, ".maestro", "mockstro.yaml")); string(got) != testSuite {
		t.Fatal("changed config after conflict")
	}
}

func TestSkillInstall(t *testing.T) {
	root := t.TempDir()
	if err := Skill(root, true); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(root, ".agents", "skills", "mockstro", "SKILL.md")
	alias := filepath.Join(root, ".claude", "skills", "mockstro", "SKILL.md")
	original, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(alias)
	if err != nil || string(got) != string(original) {
		t.Fatal("compatibility link does not resolve to canonical skill", err)
	}
	os.WriteFile(canonical, []byte("user edit"), 0644)
	if err := Skill(root, true); err == nil {
		t.Fatal("overwrote skill")
	}
	got, _ = os.ReadFile(canonical)
	if string(got) != "user edit" {
		t.Fatal("lost user edits")
	}
}

func TestReleaseTags(t *testing.T) {
	for _, version := range []string{"v1.2.3", "v0.1.0-rc.1", "v0.1.0-beta", "v0.1.0-alpha.2"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".maestro"), 0755); err != nil {
				t.Fatal(err)
			}
			writeSuite(t, root)
			if err := Project(root, version); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, version := range []string{"v0.1.0-", "v0.1.0-rc..1", "v0.1.0-rc.1/other", "v0.1.0-rc.1\nv1.0.0"} {
		if project.ValidateCLIVersion(version) == nil {
			t.Fatalf("accepted malformed release tag %q", version)
		}
	}
}

func TestProjectPreservesConflictingOrInvalidConfig(t *testing.T) {
	for _, config := range []string{
		"cliVersion: v2.0.0\n" + testSuite,
		"platforms: {android: {appId: x, integration: surprise}}\n",
	} {
		root := t.TempDir()
		os.Mkdir(filepath.Join(root, ".maestro"), 0755)
		path := filepath.Join(root, ".maestro", "mockstro.yaml")
		os.WriteFile(path, []byte(config), 0644)
		if err := Project(root, "v1.2.3"); err == nil {
			t.Fatal("accepted conflicting/invalid config")
		}
		got, _ := os.ReadFile(path)
		if string(got) != config {
			t.Fatal("changed rejected config")
		}
		if _, err := os.Lstat(filepath.Join(root, "mockstrow")); !os.IsNotExist(err) {
			t.Fatal("left wrapper after config failure")
		}
	}
}

func TestProjectNormalizesExistingPinAndPreservesConfigSymlink(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".maestro"), 0755)
	target := filepath.Join(root, "suite.yaml")
	os.WriteFile(target, []byte("{cliVersion: 'v1.2.3', platforms: {android: {appId: example.android}}}\n"), 0600)
	path := filepath.Join(root, ".maestro", "mockstro.yaml")
	os.Symlink(target, path)
	if err := Project(root, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(path)
	got, _ := os.ReadFile(target)
	if info.Mode()&os.ModeSymlink == 0 || !strings.HasPrefix(string(got), "cliVersion: v1.2.3\n") {
		t.Fatal("replaced symlink or failed to normalize pin", string(got))
	}
}
