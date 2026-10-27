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
	writeSuite(t, project)
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
	run := func(version string, exit string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{installer, version, "-C", project}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ASSETS="+assets,
			"MOCKSTRO_CACHE_DIR="+filepath.Join(root, "cache"), "CLI_EXIT="+exit)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("v0.1.0-rc.1", "0")
	want := "<-C>\n<" + project + ">\n<init>\n<--cli-version>\n<v0.1.0-rc.1>\n"
	if err != nil || !strings.Contains(out, want) || strings.Contains(out, "wrong-global") {
		t.Fatalf("init delegation failed: %q, %v", out, err)
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
	for _, path := range []string{filepath.Join(project, "mockstrow")} {
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
	os.WriteFile(filepath.Join(assets, "mockstrow"), Wrapper, 0644)
	out, err = run("v0.1.0-rc.1", "0", "--android-app-id", "com.example.app")
	if err != nil || !strings.Contains(out, want+"<--android-app-id>\n<com.example.app>\n") {
		t.Fatalf("lost init flags: %q, %v", out, err)
	}
}

func TestGlobalInstaller(t *testing.T) {
	t.Setenv("JAVA_SETUP_FAIL", "")
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	fakes := filepath.Join(root, "fakes")
	binDir := filepath.Join(root, "global bin")
	// BIN_DIR can live inside a project; the global Java check must not find it.
	for _, path := range []string{assets, fakes, filepath.Join(root, ".maestro")} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(fakes, "curl"), []byte(`#!/bin/sh
set -eu
while [ "$1" != --output ]; do shift; done
cp "$ASSETS/${3##*/}" "$2"
`), 0755)
	payload := []byte(`#!/bin/sh
printf 'cli <%s>\n' "$@"
dir=$PWD
while :; do
    if [ -d "$dir/.maestro" ]; then echo unexpected-project-check >&2; exit 24; fi
    [ "$dir" != / ] || break
    dir=$(dirname "$dir")
done
if [ "${JAVA_SETUP_FAIL:-}" = yes ]; then exit 23; fi
`)
	launcher := []byte("printf 'launcher <%s>\\n' \"$@\"\n")
	asset := "mockstro_v0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH
	write := func(launcherSum []byte) {
		os.WriteFile(filepath.Join(assets, asset), payload, 0644)
		os.WriteFile(filepath.Join(assets, "install-ui-launcher.sh"), launcher, 0644)
		os.WriteFile(filepath.Join(assets, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n%x  install-ui-launcher.sh\n",
			sha256.Sum256(payload), asset, sha256.Sum256(launcherSum))), 0644)
	}
	installer, err := filepath.Abs("../../scripts/install-global.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{installer}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+fakes+":"+os.Getenv("PATH"), "ASSETS="+assets, "BIN_DIR="+binDir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cli := filepath.Join(binDir, "mockstro")

	write(launcher)
	out, err := run("v0.1.0")
	if err != nil {
		t.Fatalf("install failed: %q, %v", out, err)
	}
	if got, _ := os.ReadFile(cli); string(got) != string(payload) {
		t.Fatal("cli not installed")
	}
	if wantLauncher := runtime.GOOS == "darwin"; strings.Contains(out, "launcher <"+cli+">") != wantLauncher {
		t.Fatalf("launcher run = %v, want %v: %q", !wantLauncher, wantLauncher, out)
	}
	if !strings.Contains(out, "cli <check>\ncli <--install>") {
		t.Fatalf("Java setup was not invoked: %q", out)
	}
	if !strings.Contains(out, "Add "+binDir+" to your PATH") {
		t.Fatalf("no PATH hint: %q", out)
	}

	out, err = run("v0.1.0", "--no-launcher")
	if err != nil || strings.Contains(out, "launcher <") {
		t.Fatalf("--no-launcher ran launcher: %q, %v", out, err)
	}

	if runtime.GOOS == "darwin" {
		os.Remove(cli)
		write([]byte("other"))
		out, err = run("v0.1.0")
		if err == nil || !strings.Contains(out, "checksum mismatch for install-ui-launcher.sh") {
			t.Fatalf("accepted corrupt launcher: %q, %v", out, err)
		}
		if _, err := os.Stat(cli); !os.IsNotExist(err) {
			t.Fatal("installed cli despite corrupt launcher")
		}
	}

	t.Setenv("JAVA_SETUP_FAIL", "yes")
	out, err = run("v0.1.0", "--no-launcher")
	if err == nil || strings.Contains(out, "Installed mockstro") {
		t.Fatalf("ignored Java setup failure: %q, %v", out, err)
	}
	t.Setenv("JAVA_SETUP_FAIL", "")
	for _, version := range []string{"latest", "v0.1.0/other"} {
		if out, err := run(version); err == nil {
			t.Fatalf("accepted %q: %s", version, out)
		}
	}
	if entries, _ := os.ReadDir(binDir); len(entries) > 1 {
		t.Fatalf("left temp files: %v", entries)
	}
}
