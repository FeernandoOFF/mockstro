package bootstrap

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project with spaces")
	bin := filepath.Join(root, "bin")
	assets := filepath.Join(root, "assets")
	cache := filepath.Join(root, "cache")
	for _, dir := range []string{filepath.Join(project, ".maestro"), bin, assets} {
		os.MkdirAll(dir, 0755)
	}
	if err := Project(project, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	// Fake only the network transport. Exercise the real checksum tools and shell.
	curl := `#!/bin/sh
set -eu
[ ! -e "$ASSETS/offline" ] || exit 22
while [ "$1" != --output ]; do shift; done
out=$2
url=$3
cp "$ASSETS/${url##*/}" "$out"
`
	os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0755)
	os.WriteFile(filepath.Join(bin, "mockstro"), []byte("#!/bin/sh\necho wrong-global; exit 99\n"), 0755)
	payload := []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\nexit 7\n")
	asset := "mockstro_v1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH
	os.WriteFile(filepath.Join(assets, asset), payload, 0644)
	checksums := filepath.Join(assets, "checksums.txt")
	os.WriteFile(checksums, []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), asset)), 0644)
	run := func() (string, error) {
		cmd := exec.Command(filepath.Join(project, "mockstrow"), "one two", "three", "")
		cmd.Dir = root // Pin comes from wrapper location, not the working directory.
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ASSETS="+assets, "MOCKSTRO_CACHE_DIR="+cache)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	assertRun := func() {
		t.Helper()
		out, err := run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 || !strings.Contains(out, "<one two>\n<three>\n<>\n") || strings.Contains(out, "wrong-global") {
			t.Fatalf("argv/exit/pin failure: %q, %v", out, err)
		}
	}
	assertRun()
	os.WriteFile(filepath.Join(assets, "offline"), nil, 0644)
	assertRun() // Verified cached binary needs no network.
	cached := filepath.Join(cache, "cli", "v1.2.3", runtime.GOOS+"_"+runtime.GOARCH, "mockstro")
	os.WriteFile(cached, []byte("#!/bin/sh\necho corrupted\n"), 0755)
	out, err := run()
	if err == nil || strings.Contains(out, "corrupted") || !strings.Contains(out, "download failed") {
		t.Fatalf("used corrupt cache: %q, %v", out, err)
	}
	os.Remove(filepath.Join(assets, "offline"))
	os.WriteFile(checksums, []byte(strings.Repeat("0", 64)+"  "+asset+"\n"), 0644)
	out, err = run()
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("accepted invalid download: %q, %v", out, err)
	}
	// A malformed multiline pin must fail before any cache/network activity.
	os.WriteFile(filepath.Join(project, ".maestro", ".mockstro-version"), []byte("v1.2.3\n../../other\n"), 0644)
	out, err = run()
	if err == nil || !strings.Contains(out, "one release tag") {
		t.Fatalf("accepted multiline pin: %q, %v", out, err)
	}
}
