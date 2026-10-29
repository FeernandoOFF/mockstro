package project

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestCatalogMetadataLoadsOnSuiteAndEveryTarget(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", `catalog:
  profileGroups:
    member: Members
    staff: Team
    admin: Team
  groupOrder: [Team, Members]
  featureTags:
    feedback.enabled: feedback
    arbitrary-key: Account settings
platforms:
  android:
    appId: com.example.android
`)
	// Locally added targets still use the suite's catalog metadata.
	writeConfig(t, root, "mockstro.local.yaml", `platforms:
  ios:
    appId: com.example.ios
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	want := CatalogConfig{
		ProfileGroups: map[string]string{"member": "Members", "staff": "Team", "admin": "Team"},
		GroupOrder:    []string{"Team", "Members"},
		FeatureTags:   map[string]string{"feedback.enabled": "feedback", "arbitrary-key": "Account settings"},
	}
	if len(p.Warnings) != 0 || !reflect.DeepEqual(p.Catalog, want) {
		t.Fatalf("suite catalog = %+v, warnings = %v", p.Catalog, p.Warnings)
	}
	for name, target := range p.Targets {
		if !reflect.DeepEqual(target.Catalog, want) {
			t.Errorf("%s catalog = %+v, want %+v", name, target.Catalog, want)
		}
	}
	if len(p.Targets) != 2 {
		t.Fatalf("targets = %v", p.Targets)
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
	if a.Build.Task != "assembleLocal" || a.Build.Artifact != "app/build/e2e/*.apk" || a.Env["MAESTRO_AGENT_AVDS"] != "My_AVD" {
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
		"missing-platforms":   "env: {USER: user}\n",
		"unknown-platform":    "platforms: {web: {appId: x}}\n",
		"missing-app":         "platforms: {ios: {integration: lite}}\n",
		"invalid-mode":        "platforms: {android: {appId: x, integration: surprise}}\n",
		"reserved":            "env: {WIREMOCK_PORT: 1234}\nplatforms: {android: {appId: x}}\n",
		"nested-variable":     "env: {USER: [one, two]}\nplatforms: {android: {appId: x}}\n",
		"multiple-documents":  "platforms: {android: {appId: x}}\n---\nplatforms: {ios: {appId: x}}\n",
		"invalid-cli-version": "cliVersion: latest\nplatforms: {android: {appId: x}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, "mockstro.yaml", text)
			if _, err := Load(root); err == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	for _, text := range []string{
		"platforms: {ios: {project: ../ios}}",
		"platforms: {android: {appId: other}}",
		"platforms: {android: {integration: full}}",
		"platforms: {web: {appId: x}}",
		"platforms: {ios: {appId: '${APP_ID}'}}",
		"platforms: {ios: {appId: x, integration: surprise}}",
		"platforms: {ios: {appId: x, wiremockVersion: arbitrary}}",
	} {
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

func TestConfigWarnsOnUnknownKeys(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x, typo: true}}\n")
	writeConfig(t, root, "mockstro.local.yaml", "cases: {login: anything}\nplatforms: {ios: {appId: y, build: {typo: true}}}\n")
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 3 {
		t.Fatalf("warnings = %q", p.Warnings)
	}
	if p.Targets["android"].AppID != "x" || p.Targets["ios"].AppID != "y" {
		t.Fatalf("unknown keys broke decoding: %+v", p.Targets)
	}
}

func TestLocalConfigResolvesAliasesAcrossPlatforms(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x}, ios: {appId: y}}\n")
	writeConfig(t, root, "mockstro.local.yaml", "platforms:\n  android: &shared\n    devices: [Pixel]\n  ios: *shared\n")
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Targets["ios"].Devices; len(got) != 1 || got[0] != "Pixel" {
		t.Fatalf("ios devices = %q", got)
	}
}

func TestLocalConfigAddsOptionalPlatform(t *testing.T) {
	for _, added := range []string{"ios", "android"} {
		t.Run(added, func(t *testing.T) {
			shared := "android"
			if added == shared {
				shared = "ios"
			}
			root := t.TempDir()
			writeConfig(t, root, "mockstro.yaml", "env: {USER: shared}\nplatforms:\n  "+shared+":\n    appId: example.shared\n")
			local := `env: {USER: local, KEEP: root}
platforms:
  ` + shared + `:
    devices: [Shared_device_override]
  ` + added + `:
    appId: example.local
    project: ../other-app
    integration: full
    devices: [Local_device]
    proxyBaseUrl: https://api.example.test
    fallbackUrl: https://fallback.example.test
    fbrcUrl: https://config.example.test
    overrideKey: local_override
    acceptKey: local_accept
    wiremockVersion: 4.0.0-beta.40
    websocketPath: /ws
    build:
      project: App.xcodeproj
      scheme: App
      task: assembleLocal
      artifact: local/build/*
    env:
      USER: platform-local
`
			writeConfig(t, root, "mockstro.local.yaml", local)
			p, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			targets, err := p.Selected("")
			if err != nil || len(targets) != 2 {
				t.Fatal("local platform did not join default runs", targets, err)
			}
			a := p.Targets[added]
			if a.AppID != "example.local" || a.Lite() || a.SourceRoot() != filepath.Clean(filepath.Join(root, "../other-app")) || a.FlowsRoot() != p.FlowsRoot() || len(a.Devices) != 1 || a.Devices[0] != "Local_device" {
				t.Fatal("local platform configuration lost", a)
			}
			if a.Build.Project != "App.xcodeproj" || a.Build.Scheme != "App" || a.Build.Task != "assembleLocal" || a.Build.Artifact != "local/build/*" || a.ProxyBaseURL != "https://api.example.test" || a.FallbackURL != "https://fallback.example.test" || a.FBRCURL != "https://config.example.test" || a.OverrideKey != "local_override" || a.AcceptKey != "local_accept" || a.WebSocketPath != "/ws" {
				t.Fatal("full local platform settings were not loaded", a)
			}
			if a.Variables["USER"] != "shared" || a.LocalVariables["USER"] != "platform-local" || a.LocalVariables["KEEP"] != "root" || p.Targets[shared].LocalVariables["USER"] != "local" || p.Targets[shared].Devices[0] != "Shared_device_override" {
				t.Fatal("local variable or existing platform overrides changed", p.Targets)
			}
			if err := os.Remove(filepath.Join(root, ".maestro/mockstro.local.yaml")); err != nil {
				t.Fatal(err)
			}
			p, err = Load(root)
			if err != nil || len(p.Targets) != 1 || p.Targets[added] != nil || p.Targets[shared].AppID != "example.shared" {
				t.Fatal("local platform was not optional", p, err)
			}
		})
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
	if _, err := Init(root, cfg); err != nil {
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
	if _, err := Init(root, cfg); err == nil {
		t.Fatal("re-initialized an existing suite")
	}
	// An existing Maestro project keeps its files; only missing ones are added.
	root = t.TempDir()
	configPath := filepath.Join(root, ".maestro", "config.yaml")
	os.MkdirAll(filepath.Dir(configPath), 0755)
	os.WriteFile(configPath, []byte("user edits"), 0644)
	skipped, err := Init(root, cfg)
	if err != nil || len(skipped) != 1 || skipped[0] != "config.yaml" {
		t.Fatal(skipped, err)
	}
	got, _ := os.ReadFile(configPath)
	if string(got) != "user edits" {
		t.Fatal("lost edits")
	}
	if _, err := Load(root); err != nil {
		t.Fatal(err)
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
	if p.Build.Task != "" || p.Build.Artifact != "" || p.Build.Variant != "debug" || p.Build.Module != "android" || p.WebSocketPath != "/ws" {
		t.Fatalf("config: %+v", p)
	}
	ios := p.Targets["ios"]
	if ios.Build.Module != "mobileIos" || ios.Build.Variant != "release" || ios.Build.Artifact != "custom/*.app" {
		t.Fatalf("iOS config: %+v", ios)
	}
	for _, bad := range []string{"build: {system: unknown}", "build: {system: kotlin-toolchain, variant: custom}", "wiremockVersion: arbitrary", "wiremockVersion: 4.0.0-beta.40, websocketPath: 'ws://host/ws'"} {
		writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: x, "+bad+"}}\n")
		if _, err := Load(root); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestLoadLiteServesASuiteWithoutConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "wiremock/stubs/health.json", "{}")
	writeConfig(t, root, "mockstro.local.yaml", "platforms:\n  ios:\n    proxyBaseUrl: https://api.example.test\n")
	if !HasSuite(root) || HasConfig(root) {
		t.Fatalf("HasSuite=%v HasConfig=%v", HasSuite(root), HasConfig(root))
	}
	if _, err := Load(root); err == nil {
		t.Fatal("Load must still require mockstro.yaml")
	}
	p, err := LoadLite(root, "ios")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Implicit || !p.Lite() || p.Platform != "ios" || p.AppID != "" || p.FBRCURL != DefaultFBRCURL || p.ProxyBaseURL != "https://api.example.test" {
		t.Fatalf("project = %+v", p)
	}
}

func TestGuessPlatform(t *testing.T) {
	root := t.TempDir()
	if got := GuessPlatform(root); got != "ios" {
		t.Fatalf("empty = %s", got)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.gradle.kts"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if got := GuessPlatform(root); got != "android" {
		t.Fatalf("gradle = %s", got)
	}
}
