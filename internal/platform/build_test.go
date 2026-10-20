package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestBuildersUseTargetCheckoutInsteadOfSuite(t *testing.T) {
	for _, name := range []string{"android", "ios"} {
		t.Run(name, func(t *testing.T) {
			suite, source, artifacts := t.TempDir(), t.TempDir(), t.TempDir()
			p := &project.Project{Root: suite, BuildRoot: source, GradleTask: "assembleDebug", APKGlob: "build/*.apk", IOSProject: "App.xcworkspace", IOSScheme: "App", IOSConfig: "Debug"}
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
				return "", nil
			}}
			path, sha, err := New(p, name, ex).Build(context.Background(), artifacts, nil)
			if err != nil || !called || path == "" || sha == "" {
				t.Fatal(path, sha, err)
			}
			if _, err := os.Stat(filepath.Join(suite, "build")); !os.IsNotExist(err) {
				t.Fatal("build wrote into the owning suite")
			}
		})
	}
}
