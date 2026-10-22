package project

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildConfigDefaults(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		root := t.TempDir()
		writeConfig(t, root, "mockstro.yaml", "platforms: {"+platform+": {appId: example}}\n")
		p, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if platform == "android" && (p.Build.System != BuildGradle || p.Build.Task != "assembleDebug" || p.Build.Artifact != "app/build/outputs/apk/debug/*.apk") {
			t.Fatalf("gradle defaults: %+v", p.Build)
		}
		if platform == "ios" && (p.Build.System != BuildXcode || p.Build.Configuration != "Debug") {
			t.Fatalf("xcode defaults: %+v", p.Build)
		}
	}
}

func TestConfigCommandBuildAndLocalOverrides(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", `platforms:
  android:
    appId: example.android
    project: ../android
    build: {task: assembleApp, artifact: out/*.apk}
  ios:
    appId: example.ios
    build:
      command: [mise, run, build-ios]
      artifact: out/*.app
`)
	writeConfig(t, root, "mockstro.local.yaml", `platforms:
  android:
    build:
      command: [make, app, "ARG=with spaces"]
  ios:
    build:
      command: [./custom-build, "$(literal)"]
`)
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	android, ios := p.Targets["android"], p.Targets["ios"]
	if android.Build.System != BuildCommand || !slices.Equal(android.Build.Command, []string{"make", "app", "ARG=with spaces"}) || android.Build.Artifact != "out/*.apk" || android.SourceRoot() != filepath.Clean(filepath.Join(root, "../android")) {
		t.Fatalf("local command did not override native builder: %+v", android)
	}
	if ios.Build.System != BuildCommand || !slices.Equal(ios.Build.Command, []string{"./custom-build", "$(literal)"}) || ios.Build.Artifact != "out/*.app" {
		t.Fatalf("command override lost shared artifact: %+v", ios.Build)
	}
	writeConfig(t, root, "mockstro.local.yaml", `platforms:
  ios:
    build: {system: xcode, project: App.xcodeproj, scheme: App}
`)
	p, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets["ios"].Build.System != BuildXcode || p.Targets["ios"].Build.Command != nil || p.Targets["ios"].Build.Artifact != "out/*.app" {
		t.Fatalf("explicit system override retained old command: %+v", p.Targets["ios"].Build)
	}
}

func TestConfigRejectsInvalidBuilds(t *testing.T) {
	for name, build := range map[string]string{
		"unknown system":         "{system: unknown}",
		"renamed native system":  "{system: native}",
		"xcode on android":       "{system: xcode}",
		"scalar command":         "{command: make app, artifact: '*.apk'}",
		"empty command":          "{command: [], artifact: '*.apk'}",
		"missing command":        "{system: command, artifact: '*.apk'}",
		"blank executable":       "{command: ['  ', app], artifact: '*.apk'}",
		"missing artifact":       "{command: [make, app]}",
		"command with native":    "{system: gradle, command: [make, app], artifact: '*.apk'}",
		"bad artifact glob":      "{command: [make, app], artifact: '['}",
		"nul command argument":   `{command: [make, "a\0b"], artifact: '*.apk'}`,
		"invalid kotlin variant": "{system: kotlin-toolchain, variant: custom}",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: example, build: "+build+"}}\n")
			if _, err := Load(root); err == nil {
				t.Fatalf("accepted invalid build %s", build)
			}
		})
	}
	if _, err := (BuildConfig{System: BuildGradle}).Resolve("ios"); err == nil || !strings.Contains(err.Error(), "requires android") {
		t.Fatal("accepted gradle for ios", err)
	}
}

func TestLocalEmptyCommandDoesNotSilentlyRetainSharedCommand(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mockstro.yaml", "platforms: {android: {appId: example, build: {command: [make, app], artifact: '*.apk'}}}\n")
	writeConfig(t, root, "mockstro.local.yaml", "platforms: {android: {build: {command: []}}}\n")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "non-empty argv") {
		t.Fatal("empty override reused a shared command", err)
	}
}
