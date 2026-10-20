package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}
func kotlinProject(t *testing.T) *project.Project {
	return &project.Project{Root: t.TempDir(), BuildSystem: "kotlin-toolchain", KotlinAndroidModule: "android", KotlinIOSModule: "ios", KotlinVariant: "debug", AppID: "dev.example"}
}
func TestKotlinBuildSnapshotsAndroid(t *testing.T) {
	p := kotlinProject(t)
	p.BuildRoot = t.TempDir()
	source := filepath.Join(p.SourceRoot(), "build/tasks/_android_buildAndroidDebug/app.apk")
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.String() != "./kotlin build -m android -p android -v debug" || c.Dir != p.SourceRoot() {
			t.Fatalf("wrong build: %+v", c)
		}
		write(t, source, "original")
		return "", nil
	}}
	path, sha, err := Build(context.Background(), p, "android", fake, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, source, "changed")
	b, _ := os.ReadFile(path)
	if string(b) != "original" || len(sha) != 64 {
		t.Fatalf("artifact not snapshotted: %s %s", b, sha)
	}
}
func TestKotlinBuildRejectsAmbiguousAndFailedOutputs(t *testing.T) {
	p := kotlinProject(t)
	p.APKGlob = "*.apk"
	write(t, filepath.Join(p.Root, "old.apk"), "stale")
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) { return "", errors.New("compiler failed") }}
	if _, _, err := Build(context.Background(), p, "android", fake, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "compiler failed") {
		t.Fatal(err)
	}
	fake.Handler = func(c execx.Cmd) (string, error) { write(t, filepath.Join(p.Root, "new.apk"), "new"); return "", nil }
	if _, _, err := Build(context.Background(), p, "android", fake, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatal(err)
	}
}
func TestIOSBuildSnapshotsResourcesAndSymlinks(t *testing.T) {
	p := kotlinProject(t)
	source := filepath.Join(p.Root, "build/tasks/_ios_buildIosAppIosSimulatorArm64Debug/bin/Debug-iphonesimulator/Example.app")
	plist := `<plist><dict><key>CFBundleExecutable</key><string>ActualExecutable</string><key>CFBundleIdentifier</key><string>dev.example</string><key>CFBundleSupportedPlatforms</key><array><string>iPhoneSimulator</string></array></dict></plist>`
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.String() != "./kotlin build -m ios -p iosSimulatorArm64 -v debug" {
			t.Fatal(c.String())
		}
		write(t, filepath.Join(source, "Info.plist"), plist)
		write(t, filepath.Join(source, "ActualExecutable"), "exe")
		write(t, filepath.Join(source, "Frameworks/Library.framework/Versions/A/Library"), "framework")
		if err := os.Symlink("Versions/A/Library", filepath.Join(source, "Frameworks/Library.framework/Library")); err != nil {
			t.Fatal(err)
		}
		return "", nil
	}}
	path, sha, err := Build(context.Background(), p, "ios", fake, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := BundleSHA(path); got != sha {
		t.Fatal("unstable hash")
	}
	write(t, filepath.Join(source, "ActualExecutable"), "rebuild")
	if got, _ := BundleSHA(path); got != sha {
		t.Fatal("shared mutable bundle")
	}
	write(t, filepath.Join(path, "Frameworks/Library.framework/Versions/A/Library"), "changed resource")
	if got, _ := BundleSHA(path); got == sha {
		t.Fatal("resource change not detected")
	}
	if got, _ := os.Readlink(filepath.Join(path, "Frameworks/Library.framework/Library")); got != "Versions/A/Library" {
		t.Fatal("symlink not preserved")
	}
}
func TestBuildCancellation(t *testing.T) {
	p := kotlinProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := Build(ctx, p, "android", execx.Real{}, t.TempDir(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
