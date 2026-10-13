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

func TestKotlinAndWebSocketConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".maestro"), 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".maestro/project.env")
	if err := os.WriteFile(config, []byte("BUILD_SYSTEM=kotlin-toolchain\nMAESTRO_APP_VARIANT=custom\nWIREMOCK_VERSION=4.0.0-beta.40\nE2E_WEBSOCKET_PATH=/ws\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.GradleTask != "" || p.KotlinVariant != "debug" || p.KotlinIOSModule != "ios" || p.WebSocketPath != "/ws" {
		t.Fatalf("config: %+v", p)
	}
	for _, bad := range []string{"BUILD_SYSTEM=unknown", "BUILD_SYSTEM=kotlin-toolchain\nKOTLIN_VARIANT=custom", "WIREMOCK_VERSION=arbitrary", "E2E_WEBSOCKET_PATH=/ws", "WIREMOCK_VERSION=4.0.0-beta.40\nE2E_WEBSOCKET_PATH=ws://host/ws"} {
		if err := os.WriteFile(config, []byte(bad), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
