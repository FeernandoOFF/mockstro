package maestro

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func artifactFile(t *testing.T, root, path, data string) string {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestIncludesScreenshotKindsInCommandOrder(t *testing.T) {
	root := t.TempDir()
	first := artifactFile(t, root, "takeScreenshot/z-first.png", "png")
	second := artifactFile(t, root, "screenshots/step-010.png", "png")
	diff := artifactFile(t, root, "diff.png", "png")
	artifactFile(t, root, "screenshots/not-listed.png", "png")
	artifactFile(t, root, "unknown.png", "png")
	artifactFile(t, root, "manifest.json", `{"entries":[
	 {"kind":"SCREENSHOT","relativePath":"screenshots/step-010.png"},
	 {"kind":"TAKE_SCREENSHOT","relativePath":"takeScreenshot","count":1},
	 {"kind":"SCREENSHOT_DIFF","relativePath":"diff.png"},
	 {"kind":"FUTURE_KIND","relativePath":"unknown.png"},
	 {"kind":"SCREENSHOT","relativePath":"missing.png"}]}`)
	artifactFile(t, root, "commands.json", `[{"metadata":{"artifacts":[{"path":"takeScreenshot/z-first.png"}]}},{"metadata":{"artifacts":[{"path":"screenshots/step-010.png"},{"path":"screenshots/not-listed.png"}]}}]`)
	got := artifacts(root)
	if got.Dir != root || !reflect.DeepEqual(got.Shots, []string{first, second, diff}) {
		t.Fatalf("artifacts %+v", got)
	}
}

func TestManifestCannotIncludeFilesOutsideBundle(t *testing.T) {
	parent := t.TempDir()
	outside := artifactFile(t, parent, "outside.png", "png")
	root := filepath.Join(parent, "bundle")
	inside := artifactFile(t, root, "inside.png", "png")
	if err := os.Symlink(outside, filepath.Join(root, "linked.png")); err != nil {
		t.Fatal(err)
	}
	artifactFile(t, root, "manifest.json", `{"entries":[
	 {"kind":"SCREENSHOT","relativePath":"../outside.png"},
	 {"kind":"SCREENSHOT","relativePath":"linked.png"},
	 {"kind":"SCREENSHOT","relativePath":"inside.png"}]}`)
	if got := artifacts(root); !reflect.DeepEqual(got.Shots, []string{inside}) {
		t.Fatalf("unsafe artifacts %+v", got)
	}
}

func TestRecordManifestAndLegacyBundles(t *testing.T) {
	for _, manifest := range []string{"", "invalid", `{"entries":[{"kind":"TAKE_SCREENSHOT","relativePath":"takeScreenshot","count":1}]}`} {
		t.Run(manifest, func(t *testing.T) {
			root := t.TempDir()
			bundle := filepath.Join(root, ".maestro", "tests", "timestamp", "flow")
			shot := artifactFile(t, bundle, "takeScreenshot/explicit.png", "png")
			if manifest != "" {
				artifactFile(t, bundle, "manifest.json", manifest)
			}
			if got := findArtifacts(root); got.Dir != bundle || !reflect.DeepEqual(got.Shots, []string{shot}) {
				t.Fatalf("record artifacts %+v", got)
			}
		})
	}
}
