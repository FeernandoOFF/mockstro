package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrationFollowsProjectEnv(t *testing.T) {
	root := t.TempDir()
	m := filepath.Join(root, ".maestro")
	if err := os.MkdirAll(filepath.Join(m, "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PACKAGE", "")
	t.Setenv("E2E_PROXY_BASE_URL", "")
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Lite() || p.AppID != "" || p.ProxyBaseURL != "" || p.GradleTask != "" || p.FBRCURL != DefaultFBRCURL {
		t.Fatalf("lite should carry no project defaults: %+v", p)
	}
	if err := os.WriteFile(filepath.Join(m, "project.env"), []byte("APP_PACKAGE=com.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err = Load(root); err != nil {
		t.Fatal(err)
	}
	if p.Lite() || p.AppID != "com.example" || p.GradleTask == "" {
		t.Fatalf("full expected: %+v", p)
	}
}
