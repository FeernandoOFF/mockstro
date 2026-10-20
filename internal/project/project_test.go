package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, ".maestro", name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigTargetsAndLocalOverrides(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", `env:
  USER: shared
platforms:
  android:
    appId: com.example.android
    project: android-app
    integration: full
    devices: [Agent_AVD]
    build:
      task: assembleE2e
      artifact: app/build/e2e/*.apk
    env:
      USER: android
  ios:
    appId: com.example.ios
    project: ios-app
    integration: lite
    devices: [Agent_iPhone]
    build:
      project: App.xcworkspace
      scheme: App
`)
	writeConfig(t, root, "mockstro.local.yaml", `env:
  USER: local
platforms:
  android:
    project: ../my-android
    devices: [My_AVD]
    build:
      task: assembleLocal
    env:
      COUNT: 3
`)
	t.Setenv("APP_PACKAGE", "must.not.override.config")
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := p.Selected("")
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
	a, i := targets[0], targets[1]
	if a.Platform != "android" || i.Platform != "ios" || a.AppID != "com.example.android" || a.Lite() || !i.Lite() {
		t.Fatalf("bad targets: %+v %+v", a, i)
	}
	if a.SourceRoot() != filepath.Clean(filepath.Join(root, "../my-android")) || a.Root != root || a.FlowsRoot() != filepath.Join(root, ".maestro/flows") {
		t.Fatal("source and suite roots were conflated", a)
	}
	if a.GradleTask != "assembleLocal" || a.APKGlob != "app/build/e2e/*.apk" || a.Env["MAESTRO_AGENT_AVDS"] != "My_AVD" {
		t.Fatal("local build or device override lost", a)
	}
	if a.Variables["USER"] != "shared" || a.TargetVariables["USER"] != "android" || a.LocalVariables["USER"] != "local" || a.LocalVariables["COUNT"] != "3" {
		t.Fatal("lost variable layers", a)
	}
	if selected, err := p.Selected("ios"); err != nil || len(selected) != 1 || selected[0].AppID != "com.example.ios" {
		t.Fatal(selected, err)
	}
	if _, err := p.Selected("web"); err == nil {
		t.Fatal("accepted unsupported target")
	}
}

func TestConfigRejectsInvalidSettings(t *testing.T) {
	for name, text := range map[string]string{
		"missing-platforms":  "env: {USER: user}\n",
		"unknown-field":      "platforms: {android: {appId: x, typo: true}}\n",
		"unknown-platform":   "platforms: {web: {appId: x}}\n",
		"missing-app":        "platforms: {ios: {integration: lite}}\n",
		"invalid-mode":       "platforms: {android: {appId: x, integration: surprise}}\n",
		"reserved":           "env: {WIREMOCK_PORT: 1234}\nplatforms: {android: {appId: x}}\n",
		"nested-variable":    "env: {USER: [one, two]}\nplatforms: {android: {appId: x}}\n",
		"multiple-documents": "platforms: {android: {appId: x}}\n---\nplatforms: {ios: {appId: x}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, "mockstro.yaml", text)
			if _, err := Load(root); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	for _, text := range []string{"platforms: {ios: {project: ../ios}}", "platforms: {android: {appId: other}}", "cases: {login: anything}"} {
		root := t.TempDir()
		writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x}}\n")
		writeConfig(t, root, "mockstro.local.yaml", text)
		if _, err := Load(root); err == nil {
			t.Fatalf("accepted out-of-scope local override %q", text)
		}
	}
	root := t.TempDir()
	writeConfig(t, root, "project.env", "APP_PACKAGE=old\n")
	if _, err := Load(root); err == nil {
		t.Fatal("read legacy project.env")
	}
}

func TestVariableFiles(t *testing.T) {
	for _, text := range []string{"APP_ID: x", "WIREMOCK_PORT: 12", "KEY: [one]", "KEY: null", "bad key: x", "KEY: one\nKEY: two", "KEY: one\n---\nKEY: two"} {
		path := filepath.Join(t.TempDir(), "vars.yaml")
		os.WriteFile(path, []byte(text), 0644)
		if _, err := ReadVariables(path); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	path := filepath.Join(t.TempDir(), "vars.yaml")
	os.WriteFile(path, []byte("COUNT: 12\nENABLED: true\nEMPTY: ''\nUSER: 'space = value'\n"), 0644)
	got, err := ReadVariables(path)
	if err != nil || got["COUNT"] != "12" || got["ENABLED"] != "true" || got["EMPTY"] != "" || got["USER"] != "space = value" {
		t.Fatal(got, err)
	}
}

func TestInitSharedAppIDTemplateAndPreservation(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Platforms: map[string]TargetConfig{"android": {AppID: "com.example.android"}, "ios": {AppID: "com.example.ios"}}}
	if err := Init(root, cfg); err != nil {
		t.Fatal(err)
	}
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 2 {
		t.Fatal(p.Targets)
	}
	flow, _ := os.ReadFile(filepath.Join(p.FlowsRoot(), "smoke/launch.yaml"))
	if !strings.Contains(string(flow), "appId: ${APP_ID}") {
		t.Fatal(string(flow))
	}
	ignore, _ := os.ReadFile(filepath.Join(p.MaestroRoot, ".gitignore"))
	if !strings.Contains(string(ignore), "/mockstro.local.yaml") || !strings.Contains(string(ignore), "/.runtime/") {
		t.Fatal(string(ignore))
	}
	configPath := filepath.Join(p.MaestroRoot, "config.yaml")
	os.WriteFile(configPath, []byte("user edits"), 0644)
	if err := Init(root, cfg); err == nil {
		t.Fatal("overwrote existing suite")
	}
	got, _ := os.ReadFile(configPath)
	if string(got) != "user edits" {
		t.Fatal("lost edits")
	}
}

func TestKotlinAndWebSocketConfiguration(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", `platforms:
  android:
    appId: dev.example.android
    integration: full
    wiremockVersion: 4.0.0-beta.40
    websocketPath: /ws
    build:
      system: kotlin-toolchain
  ios:
    appId: dev.example.ios
    build:
      system: kotlin-toolchain
      module: mobileIos
      variant: release
      artifact: custom/*.app
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.GradleTask != "" || p.APKGlob != "" || p.KotlinVariant != "debug" || p.KotlinAndroidModule != "android" || p.WebSocketPath != "/ws" {
		t.Fatalf("config: %+v", p)
	}
	ios := p.Targets["ios"]
	if ios.KotlinIOSModule != "mobileIos" || ios.KotlinVariant != "release" || ios.IOSAppGlob != "custom/*.app" || ios.WireMockVersion != "3.13.2" {
		t.Fatalf("iOS config: %+v", ios)
	}
	for _, bad := range []string{"build: {system: unknown}", "build: {system: kotlin-toolchain, variant: custom}", "wiremockVersion: arbitrary", "websocketPath: /ws", "wiremockVersion: 4.0.0-beta.40, websocketPath: 'ws://host/ws'"} {
		writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x, "+bad+"}}\n")
		if _, err := Load(root); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
