package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	return &project.Project{Root: t.TempDir(), Platform: "android", Build: project.BuildConfig{System: project.BuildKotlin}, AppID: "dev.example"}
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
	artifact, err := Build(context.Background(), p, fake, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, source, "changed")
	b, _ := os.ReadFile(artifact.Path)
	if string(b) != "original" || len(artifact.SHA256) != 64 {
		t.Fatalf("artifact not snapshotted: %s %s", b, artifact.SHA256)
	}
}
func TestKotlinBuildRejectsAmbiguousAndFailedOutputs(t *testing.T) {
	p := kotlinProject(t)
	p.Build.Artifact = "*.apk"
	write(t, filepath.Join(p.Root, "old.apk"), "stale")
	fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) { return "", errors.New("compiler failed") }}
	if _, err := Build(context.Background(), p, fake, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "compiler failed") {
		t.Fatal(err)
	}
	fake.Handler = func(c execx.Cmd) (string, error) { write(t, filepath.Join(p.Root, "new.apk"), "new"); return "", nil }
	if _, err := Build(context.Background(), p, fake, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatal(err)
	}
}
func TestIOSBuildSnapshotsResourcesAndSymlinks(t *testing.T) {
	p := kotlinProject(t)
	p.Platform = "ios"
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
	artifact, err := Build(context.Background(), p, fake, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := BundleSHA(artifact.Path); got != artifact.SHA256 {
		t.Fatal("unstable hash")
	}
	write(t, filepath.Join(source, "ActualExecutable"), "rebuild")
	if got, _ := BundleSHA(artifact.Path); got != artifact.SHA256 {
		t.Fatal("shared mutable bundle")
	}
	write(t, filepath.Join(artifact.Path, "Frameworks/Library.framework/Versions/A/Library"), "changed resource")
	if got, _ := BundleSHA(artifact.Path); got == artifact.SHA256 {
		t.Fatal("resource change not detected")
	}
	if got, _ := os.Readlink(filepath.Join(artifact.Path, "Frameworks/Library.framework/Library")); got != "Versions/A/Library" {
		t.Fatal("symlink not preserved")
	}
}
func TestBuildCancellation(t *testing.T) {
	p := kotlinProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, p, execx.Real{}, t.TempDir(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCommandBuildUsesArgvAndSourceRoot(t *testing.T) {
	p := &project.Project{Root: t.TempDir(), BuildRoot: t.TempDir(), Platform: "android", AppID: "dev.example", Build: project.BuildConfig{
		Command: []string{"make", "app", "literal value", "$(touch unwanted)", "*.apk", "&&"}, Artifact: "out/*.apk",
	}}
	ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		if c.Name != "make" || !slices.Equal(c.Args, p.Build.Command[1:]) || c.Dir != p.SourceRoot() {
			t.Fatalf("command was reinterpreted or ran outside the source checkout: %+v", c)
		}
		write(t, filepath.Join(c.Dir, "out/app.apk"), "built")
		// A directory with an APK suffix is not an installable file.
		if err := os.Mkdir(filepath.Join(c.Dir, "out/not-an-app.apk"), 0755); err != nil {
			t.Fatal(err)
		}
		return "", nil
	}}
	artifact, err := Build(context.Background(), p, ex, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Platform != p.Platform || artifact.AppID != p.AppID || artifact.System != project.BuildCommand || !artifact.Exists() {
		t.Fatalf("artifact metadata: %+v", artifact)
	}
	if err := artifact.Verify(); err != nil {
		t.Fatal(err)
	}
	write(t, artifact.Path, "modified snapshot")
	if err := artifact.Verify(); err == nil {
		t.Fatal("modified artifact passed verification")
	}
}

func TestCommandBuildDiscovery(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			p := &project.Project{Root: t.TempDir(), Platform: "android", Build: project.BuildConfig{Command: []string{"mise", "run", "build-android"}, Artifact: "out/*.apk"}}
			ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
				for n := 0; n < count; n++ {
					write(t, filepath.Join(c.Dir, "out", fmt.Sprintf("%d.apk", n)), "app")
				}
				return "", nil
			}}
			if _, err := Build(context.Background(), p, ex, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("found %d", count)) {
				t.Fatalf("discovery: %v", err)
			}
		})
	}
}

func TestNativeFailureDoesNotRetryOrSnapshotStaleArtifact(t *testing.T) {
	p := &project.Project{Root: t.TempDir(), Platform: "android"}
	write(t, filepath.Join(p.Root, "app/build/outputs/apk/debug/stale.apk"), "stale")
	ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) { return "", errors.New("gradle failed") }}
	artifact, err := Build(context.Background(), p, ex, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "gradle failed") || len(ex.Calls) != 1 || artifact.Path != "" {
		t.Fatalf("failed native build retried or accepted stale output: %+v, %v, %v", artifact, err, ex.Calls)
	}
}

func TestRepeatedBuildsKeepIndependentSnapshots(t *testing.T) {
	p := &project.Project{Root: t.TempDir(), Platform: "android", Build: project.BuildConfig{Command: []string{"make", "app"}, Artifact: "app.apk"}}
	buildNumber := 0
	ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
		buildNumber++
		write(t, filepath.Join(c.Dir, "app.apk"), fmt.Sprint(buildNumber))
		return "", nil
	}}
	dir := t.TempDir()
	first, err := Build(context.Background(), p, ex, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), p, ex, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path || first.SHA256 == second.SHA256 {
		t.Fatal("rebuild reused a snapshot", first, second)
	}
	if err := first.Verify(); err != nil {
		t.Fatal("rebuild mutated the first snapshot", err)
	}
}

func TestCommandCancellationStopsRunningBuild(t *testing.T) {
	root := t.TempDir()
	// The shell is explicitly the script's interpreter, not an implicit build shell.
	write(t, filepath.Join(root, "build-tool"), "#!/bin/sh\nprintf started > started\nexec sleep 30\n")
	p := &project.Project{Root: root, Platform: "android", Build: project.BuildConfig{Command: []string{"./build-tool"}, Artifact: "*.apk"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		_, err := Build(ctx, p, execx.Real{}, dir, nil)
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("build exited before cancellation: %v", err)
		case <-deadline:
			t.Fatal("build did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled build did not stop")
	}
	if matches, err := filepath.Glob(filepath.Join(dir, "artifacts", "*")); err != nil || len(matches) != 0 {
		t.Fatalf("canceled command left snapshots: %v, %v", matches, err)
	}
}

func TestIOSCommandBuildValidatesSimulatorBundle(t *testing.T) {
	for _, tc := range []struct {
		name, appID, platform, executable, wantError string
	}{
		{"valid", "dev.example", "iPhoneSimulator", "App", ""},
		{"wrong app", "dev.other", "iPhoneSimulator", "App", "bundle identifier"},
		{"device bundle", "dev.example", "iPhoneOS", "App", "not an iOS Simulator bundle"},
		{"missing executable", "dev.example", "iPhoneSimulator", "Missing", "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &project.Project{Root: t.TempDir(), Platform: "ios", AppID: "dev.example", Build: project.BuildConfig{Command: []string{"mise", "run", "build-ios"}, Artifact: "out/*.app"}}
			ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
				if c.Name != "mise" || !slices.Equal(c.Args, []string{"run", "build-ios"}) {
					t.Fatalf("unexpected build command: %+v", c)
				}
				app := filepath.Join(c.Dir, "out/Example.app")
				write(t, filepath.Join(app, "App"), "executable")
				write(t, filepath.Join(app, "Info.plist"), fmt.Sprintf(`<plist><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleSupportedPlatforms</key><array><string>%s</string></array><key>CFBundleExecutable</key><string>%s</string></dict></plist>`, tc.appID, tc.platform, tc.executable))
				return "", nil
			}}
			dir := t.TempDir()
			artifact, err := Build(context.Background(), p, ex, dir, nil)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := artifact.Verify(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("wanted %q, got %v", tc.wantError, err)
				}
				if matches, err := filepath.Glob(filepath.Join(dir, "artifacts", "*")); err != nil || len(matches) != 0 {
					t.Fatalf("invalid bundle left a snapshot: %v, %v", matches, err)
				}
			}
		})
	}
}

func TestBundleSnapshotRejectsExternalSymlinks(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprint(absolute), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "App.app")
			write(t, filepath.Join(root, "external"), "mutable")
			if err := os.Mkdir(source, 0755); err != nil {
				t.Fatal(err)
			}
			link := "../external"
			if absolute {
				link = filepath.Join(root, "external")
			}
			if err := os.Symlink(link, filepath.Join(source, "resource")); err != nil {
				t.Fatal(err)
			}
			if err := copyBundle(source, filepath.Join(t.TempDir(), "App.app")); err == nil || !strings.Contains(err.Error(), "remain inside") {
				t.Fatal("snapshot retained external dependency", err)
			}
		})
	}
}
