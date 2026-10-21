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
	writeSuite(t, project)
	if err := Project(project, "v0.1.0-rc.1"); err != nil {
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
	dev := filepath.Join(bin, "mockstro-dev")
	os.WriteFile(dev, []byte("#!/bin/sh\necho wrong-dev; exit 99\n"), 0755)
	payload := []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\nexit 7\n")
	asset := "mockstro_v0.1.0-rc.1_" + runtime.GOOS + "_" + runtime.GOARCH
	os.WriteFile(filepath.Join(assets, asset), payload, 0644)
	checksums := filepath.Join(assets, "checksums.txt")
	os.WriteFile(checksums, []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), asset)), 0644)
	run := func() (string, error) {
		cmd := exec.Command(filepath.Join(project, "mockstrow"), "one two", "three", "")
		cmd.Dir = root // Pin comes from wrapper location, not the working directory.
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ASSETS="+assets, "MOCKSTRO_CACHE_DIR="+cache, "MOCKSTRO_DEV_CLI="+dev)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	assertRun := func() {
		t.Helper()
		out, err := run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 || !strings.Contains(out, "<one two>\n<three>\n<>\n") || strings.Contains(out, "wrong-global") || strings.Contains(out, "wrong-dev") {
			t.Fatalf("argv/exit/pin failure: %q, %v", out, err)
		}
	}
	assertRun()
	os.WriteFile(filepath.Join(assets, "offline"), nil, 0644)
	assertRun() // Verified cached binary needs no network.
	cached := filepath.Join(cache, "cli", "v0.1.0-rc.1", runtime.GOOS+"_"+runtime.GOARCH, "mockstro")
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
	os.WriteFile(filepath.Join(project, ".maestro", "mockstro.yaml"), []byte("cliVersion: v0.1.0-rc.1\ncliVersion: ../../other\n"), 0644)
	out, err = run()
	if err == nil || !strings.Contains(out, "one release tag") {
		t.Fatalf("accepted multiline pin: %q, %v", out, err)
	}
}

func TestWrapperDevMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	root := t.TempDir()
	project := filepath.Join(root, "project with spaces")
	bin := filepath.Join(root, "bin with spaces")
	for _, dir := range []string{filepath.Join(project, ".maestro"), bin} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSuite(t, project)
	if err := Project(project, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(bin, "mockstro-dev")
	payload := "#!/bin/sh\nprintf 'cwd=<%s>\\n' \"$PWD\"\nprintf '<%s>\\n' \"$@\"\nexit 17\n"
	if err := os.WriteFile(dev, []byte(payload), 0755); err != nil {
		t.Fatal(err)
	}
	// Dev mode needs neither a usable pin nor access to release downloads/cache.
	pin := filepath.Join(project, ".maestro", "mockstro.yaml")
	const originalPin = "cliVersion: invalid release pin\n"
	if err := os.WriteFile(pin, []byte(originalPin), 0644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "unused cache")
	cwd, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{"", dev} {
		t.Run(map[bool]string{true: "PATH", false: "explicit-path"}[override == ""], func(t *testing.T) {
			cmd := exec.Command(filepath.Join(project, "mockstrow"), "--dev", "run", "one two", "", "--tags", "smoke")
			cmd.Dir = project
			cmd.Env = append(os.Environ(), "PATH="+bin, "MOCKSTRO_DEV_CLI="+override, "MOCKSTRO_CACHE_DIR="+cache)
			out, err := cmd.CombinedOutput()
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 17 {
				t.Fatalf("dev exit code: %v, output=%s", err, out)
			}
			if !strings.Contains(string(out), "<run>\n<one two>\n<>\n<--tags>\n<smoke>\n") || !strings.Contains(string(out), "cwd=<"+cwd+">") {
				t.Fatalf("dev arguments/working directory: %s", out)
			}
		})
	}
	// A requested dev executable must fail explicitly rather than run the release.
	missing := filepath.Join(bin, "missing-dev")
	cmd := exec.Command(filepath.Join(project, "mockstrow"), "--dev", "--version")
	cmd.Env = append(os.Environ(), "MOCKSTRO_DEV_CLI="+missing, "MOCKSTRO_CACHE_DIR="+cache)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "make install-dev") || strings.Contains(string(out), "pin must") {
		t.Fatalf("missing dev command: %v, output=%s", err, out)
	}
	if got, err := os.ReadFile(pin); err != nil || string(got) != originalPin {
		t.Fatalf("dev mode changed the pin: %q, %v", got, err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("dev mode accessed the release cache: %v", err)
	}
}

func TestWrapperYAMLPin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX launcher")
	}
	for _, tc := range []struct {
		name, config string
		valid        bool
	}{
		{"plain", "cliVersion: v1.2.3\n", true},
		{"single-quoted", "cliVersion: 'v1.2.3' # release\n", true},
		{"double-quoted-crlf", "cliVersion: \"v1.2.3\"\r\n", true},
		{"document-comments", "---\n# pin\ncliVersion: v1.2.3 # release\nplatforms:\n  android:\n    appId: example\n", true},
		{"nested-decoy", "platforms:\n  android:\n    cliVersion: v9.9.9\ncliVersion: v1.2.3\n", true},
		{"missing-config", "", false},
		{"missing-pin", "platforms: {}\n", false},
		{"nested-only", "platforms:\n  android:\n    cliVersion: v1.2.3\n", false},
		{"empty", "cliVersion: # missing\n", false},
		{"duplicate", "cliVersion: v1.2.3\ncliVersion: v1.2.3\n", false},
		{"multiline", "cliVersion: |\n  v1.2.3\n", false},
		{"traversal", "cliVersion: ../../other\n", false},
		{"command", "cliVersion: $(echo v1.2.3)\n", false},
		{"unclosed-quote", "cliVersion: 'v1.2.3\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.Mkdir(filepath.Join(root, ".maestro"), 0755)
			os.WriteFile(filepath.Join(root, "mockstrow"), Wrapper, 0755)
			if tc.config != "" {
				os.WriteFile(filepath.Join(root, ".maestro", "mockstro.yaml"), []byte(tc.config), 0644)
			}
			cache := filepath.Join(root, "cache")
			cached := filepath.Join(cache, "cli", "v1.2.3", runtime.GOOS+"_"+runtime.GOARCH)
			os.MkdirAll(cached, 0755)
			payload := []byte("#!/bin/sh\necho yaml-pin\n")
			os.WriteFile(filepath.Join(cached, "mockstro"), payload, 0755)
			os.WriteFile(filepath.Join(cached, "sha256"), []byte(fmt.Sprintf("%x\n", sha256.Sum256(payload))), 0644)
			bin := filepath.Join(root, "bin")
			os.Mkdir(bin, 0755)
			os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\necho unexpected-download >&2\nexit 1\n"), 0755)
			cmd := exec.Command(filepath.Join(root, "mockstrow"), "--version")
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MOCKSTRO_CACHE_DIR="+cache)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid || strings.Contains(string(out), "unexpected-download") || tc.valid && strings.TrimSpace(string(out)) != "yaml-pin" {
				t.Fatalf("valid=%v: %v, output=%s", tc.valid, err, out)
			}
		})
	}
}
