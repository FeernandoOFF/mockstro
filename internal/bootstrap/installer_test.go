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

func TestInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	root := t.TempDir()
	project := filepath.Join(root, "app with spaces")
	assets := filepath.Join(root, "assets")
	bin := filepath.Join(root, "bin")
	for _, path := range []string{filepath.Join(project, ".maestro"), assets, bin} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(project, ".maestro", "config.yaml")
	os.WriteFile(config, []byte("existing config"), 0644)
	// Fake the transport and released CLI. The installer must verify and execute
	// the actual wrapper, which then verifies this CLI and delegates bootstrap.
	os.WriteFile(filepath.Join(bin, "curl"), []byte(`#!/bin/sh
set -eu
printf 'request\n' >> "$ASSETS/requests"
while [ "$1" != --output ]; do shift; done
cp "$ASSETS/${3##*/}" "$2"
`), 0755)
	os.WriteFile(filepath.Join(bin, "mockstro"), []byte("#!/bin/sh\necho wrong-global; exit 99\n"), 0755)
	payload := []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\nexit \"${CLI_EXIT:-0}\"\n")
	asset := "mockstro_v0.1.0-rc.1_" + runtime.GOOS + "_" + runtime.GOARCH
	os.WriteFile(filepath.Join(assets, asset), payload, 0644)
	os.WriteFile(filepath.Join(assets, "mockstrow"), Wrapper, 0644)
	checksums := []byte(fmt.Sprintf("%x  mockstrow\n%x  %s\n", sha256.Sum256(Wrapper), sha256.Sum256(payload), asset))
	os.WriteFile(filepath.Join(assets, "checksums.txt"), checksums, 0644)
	installer, err := filepath.Abs("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(version string, exit string) (string, error) {
		cmd := exec.Command("sh", installer, version, project)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ASSETS="+assets,
			"MOCKSTRO_CACHE_DIR="+filepath.Join(root, "cache"), "CLI_EXIT="+exit)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("v0.1.0-rc.1", "0")
	want := "<-C>\n<" + project + ">\n<bootstrap>\n<--cli-version>\n<v0.1.0-rc.1>\n"
	if err != nil || !strings.Contains(out, want) || strings.Contains(out, "wrong-global") {
		t.Fatalf("bootstrap delegation failed: %q, %v", out, err)
	}
	out, err = run("v0.1.0-rc.1", "17")
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 17 {
		t.Fatalf("lost CLI failure status: %q, %v", out, err)
	}
	os.WriteFile(filepath.Join(assets, "mockstrow"), []byte("#!/bin/sh\necho unverified-wrapper\n"), 0644)
	out, err = run("v0.1.0-rc.1", "0")
	if err == nil || !strings.Contains(out, "checksum mismatch") || strings.Contains(out, "unverified-wrapper") {
		t.Fatalf("accepted corrupt wrapper: %q, %v", out, err)
	}
	for _, path := range []string{filepath.Join(project, "mockstrow"), filepath.Join(project, ".maestro", ".mockstro-version")} {
		// Even a dangling symlink must block installation before network access.
		os.Remove(filepath.Join(assets, "requests"))
		os.Symlink("missing", path)
		out, err = run("v0.1.0-rc.1", "0")
		if err == nil || !strings.Contains(out, "refusing to overwrite") {
			t.Fatalf("accepted existing path: %q, %v", out, err)
		}
		if _, err := os.Stat(filepath.Join(assets, "requests")); !os.IsNotExist(err) {
			t.Fatal("downloaded before detecting existing files")
		}
		os.Remove(path)
	}
	for _, version := range []string{"latest", "v0.1.0-rc.1\n../../other", "v0.1.0-rc.1/other"} {
		if out, err := run(version, "0"); err == nil {
			t.Fatalf("accepted %q: %s", version, out)
		}
	}
	if got, _ := os.ReadFile(config); string(got) != "existing config" {
		t.Fatal("changed existing config")
	}
	os.RemoveAll(filepath.Join(project, ".maestro"))
	if out, err := run("v0.1.0-rc.1", "0"); err == nil || !strings.Contains(out, "existing .maestro") {
		t.Fatalf("accepted project without .maestro: %q, %v", out, err)
	}
}
