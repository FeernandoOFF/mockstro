package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/build"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestBuildersUseTargetCheckoutInsteadOfSuite(t *testing.T) {
	for _, name := range []string{"android", "ios"} {
		t.Run(name, func(t *testing.T) {
			suite, source, artifacts := t.TempDir(), t.TempDir(), t.TempDir()
			p := &project.Project{Root: suite, BuildRoot: source, Platform: name}
			if name == "ios" {
				p.Build = project.BuildConfig{Project: "App.xcworkspace", Scheme: "App"}
			} else {
				p.Build = project.BuildConfig{Artifact: "build/*.apk"}
			}
			called := false
			ex := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
				called = true
				if c.Dir != source {
					t.Errorf("build working directory is %q, want %q", c.Dir, source)
				}
				path := filepath.Join(source, "build/app.apk")
				if name == "ios" {
					if !strings.Contains(strings.Join(c.Args, " "), filepath.Join(source, "App.xcworkspace")) {
						t.Error("Xcode project resolved from the wrong checkout")
					}
					path = filepath.Join(source, "build/maestro-derived-data/Build/Products/Debug-iphonesimulator/App.app/App")
				}
				os.MkdirAll(filepath.Dir(path), 0755)
				os.WriteFile(path, []byte("built app"), 0755)
				if name == "ios" {
					plist := `<plist><dict><key>CFBundleExecutable</key><string>App</string><key>CFBundleSupportedPlatforms</key><array><string>iPhoneSimulator</string></array></dict></plist>`
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), "Info.plist"), []byte(plist), 0644); err != nil {
						t.Fatal(err)
					}
				}
				return "", nil
			}}
			artifact, err := build.Build(context.Background(), p, ex, artifacts, nil)
			if err != nil || !called || artifact.Path == "" || artifact.SHA256 == "" {
				t.Fatal(artifact, err)
			}
			if _, err := os.Stat(filepath.Join(suite, "build")); !os.IsNotExist(err) {
				t.Fatal("build wrote into the owning suite")
			}
		})
	}
}
