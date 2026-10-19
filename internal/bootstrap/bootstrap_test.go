package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPreservesConfigAndExistingFiles(t *testing.T) {
	root := t.TempDir()
	if err := Project(root, "v1.2.3"); err == nil {
		t.Fatal("accepted missing .maestro")
	}
	if err := os.Mkdir(filepath.Join(root, ".maestro"), 0755); err != nil {
		t.Fatal(err)
	}
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
	for path, want := range map[string]string{config: "existing config", filepath.Join(root, ".maestro", ".mockstro-version"): "v1.2.3\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", path, got, err)
		}
	}
	st, _ := os.Stat(filepath.Join(root, "mockstrow"))
	if st.Mode()&0111 == 0 {
		t.Fatal("wrapper not executable")
	}
}

func TestProjectRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".maestro"), 0755)
	os.Symlink("missing", filepath.Join(root, "mockstrow"))
	if err := Project(root, "v1.2.3"); err == nil {
		t.Fatal("accepted existing symlink")
	}
	if _, err := os.Stat(filepath.Join(root, ".maestro", ".mockstro-version")); !os.IsNotExist(err) {
		t.Fatal("left a pin after conflict")
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
