package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	maestroexec "github.com/feernandooff/mockstro/internal/maestro"
)

func TestMaestroSetupReusesExistingInstall(t *testing.T) {
	for _, onPath := range []bool{true, false} {
		t.Run(map[bool]string{true: "PATH", false: "official-install-dir"}[onPath], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("MAESTRO_DIR", dir)
			e, fake := testEngine(t, "human", func(c execx.Cmd) (string, error) {
				if c.String() != "java -version" {
					t.Fatalf("existing Maestro was modified: %s", c)
				}
				// Real JVMs normally print the version on stderr.
				io.WriteString(c.Stderr, "openjdk 21.0.5 2024-10-15\n")
				return "", nil
			})
			want := filepath.Join(dir, "bin", "maestro")
			fake.Paths[want] = want
			if onPath {
				fake.Paths["maestro"] = "/bin/maestro"
				want = "maestro"
			}
			if err := e.EnsureMaestro(context.Background(), yes); err != nil {
				t.Fatal(err)
			}
			if got, err := maestroexec.Command(e.Exec); err != nil || got != want {
				t.Fatalf("resolved %q, %v; want %q", got, err, want)
			}
			for _, name := range []string{"java", "adb"} {
				fake.Paths[name] = "/bin/" + name
			}
			if err := e.CheckEnvironment("run"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaestroDefaultInstallIsFoundWithoutPATH(t *testing.T) {
	t.Setenv("MAESTRO_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	e, fake := testEngine(t, "human", nil)
	path := filepath.Join(home, ".maestro", "bin", "maestro")
	fake.Paths[path] = path
	if got, err := maestroexec.Command(e.Exec); err != nil || got != path {
		t.Fatalf("default install: %q, %v", got, err)
	}
}

func TestMaestroSetupCancellationDoesNotInstall(t *testing.T) {
	e, fake := testEngine(t, "human", func(c execx.Cmd) (string, error) { return "", context.Canceled })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.EnsureMaestro(ctx, yes); !errors.Is(err, context.Canceled) || len(fake.Calls) != 1 {
		t.Fatalf("cancellation: %v, calls=%v", err, fake.Calls)
	}
}

func TestMaestroSetupInstallsWithHomebrewOnce(t *testing.T) {
	installs := 0
	var fake *execx.Fake
	e, runner := testEngine(t, "human", func(c execx.Cmd) (string, error) {
		switch c.String() {
		case "java -version":
			return `openjdk version "17.0.13"`, nil
		case "brew install mobile-dev-inc/tap/maestro":
			installs++
			fake.Paths["maestro"] = "/bin/maestro"
			return "installed", nil
		case "maestro --version":
			return "2.0.0", nil
		default:
			t.Fatalf("unexpected %s", c)
			return "", nil
		}
	})
	fake = runner
	fake.Paths["brew"] = "/bin/brew"
	for range 2 {
		if err := e.EnsureMaestro(context.Background(), yes); err != nil {
			t.Fatal(err)
		}
	}
	if installs != 1 {
		t.Fatalf("installed %d times", installs)
	}
}

func TestMaestroSetupUsesOfficialInstallerWithoutHomebrew(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAESTRO_DIR", dir)
	maestro := filepath.Join(dir, "bin", "maestro")
	const script = "#!/bin/bash\necho official installer\n"
	var fake *execx.Fake
	e, runner := testEngine(t, "human", func(c execx.Cmd) (string, error) {
		switch c.Name {
		case "java":
			return `openjdk version "17.0.13"`, nil
		case "curl":
			if !strings.HasSuffix(c.String(), "https://get.maestro.mobile.dev") || !strings.Contains(c.String(), "--fail") {
				t.Fatalf("installer download: %s", c)
			}
			return script, nil
		case "bash":
			body, _ := io.ReadAll(c.Stdin)
			if string(body) != strings.TrimSpace(script) || c.String() != "bash -s" {
				t.Fatalf("installer execution: %s, stdin=%q", c, body)
			}
			fake.Paths[maestro] = maestro
			return "installed", nil
		case maestro:
			if c.String() != maestro+" --version" {
				t.Fatal(c)
			}
			return "2.0.0", nil
		default:
			t.Fatalf("unexpected %s", c)
			return "", nil
		}
	})
	fake = runner
	for _, tool := range []string{"bash", "curl", "unzip"} {
		fake.Paths[tool] = "/bin/" + tool
	}
	if err := e.EnsureMaestro(context.Background(), yes); err != nil {
		t.Fatal(err)
	}
	if got, err := maestroexec.Command(e.Exec); err != nil || got != maestro {
		t.Fatalf("installed command: %s, %v", got, err)
	}
}

func TestMaestroSetupReportsInstallFailures(t *testing.T) {
	for _, failure := range []string{"brew", "curl", "bash", "missing-executable", "startup"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("MAESTRO_DIR", t.TempDir())
			problem := errors.New("installation failed")
			var fake *execx.Fake
			e, runner := testEngine(t, "human", func(c execx.Cmd) (string, error) {
				if c.Name == "java" {
					return `openjdk version "17.0.13"`, nil
				}
				if c.Name == failure {
					return "", problem
				}
				if c.Name == "curl" {
					return "#!/bin/bash\necho installer", nil
				}
				if c.Name == "brew" || c.Name == "bash" {
					if failure != "missing-executable" {
						fake.Paths["maestro"] = "/bin/maestro"
					}
					return "", nil
				}
				if c.Name == "maestro" && failure == "startup" {
					return "", problem
				}
				t.Fatalf("unexpected %s", c)
				return "", nil
			})
			fake = runner
			for _, tool := range []string{"bash", "curl", "unzip"} {
				fake.Paths[tool] = "/bin/" + tool
			}
			if failure == "brew" {
				fake.Paths["brew"] = "/bin/brew"
			}
			err := e.EnsureMaestro(context.Background(), yes)
			if err == nil || failure != "missing-executable" && !errors.Is(err, problem) {
				t.Fatalf("failure %s: %v", failure, err)
			}
		})
	}
}

func TestDeclinedMaestroInstallDoesNothing(t *testing.T) {
	e, fake := testEngine(t, "human", func(c execx.Cmd) (string, error) { return "openjdk version \"17.0.1\"", nil })
	fake.Paths["brew"] = "/bin/brew"
	err := e.EnsureMaestro(context.Background(), func() bool { return false })
	if err == nil || !strings.Contains(err.Error(), "check --install") || len(fake.Calls) != 1 {
		t.Fatalf("declined install: %v, calls=%v", err, fake.Calls)
	}
}

func TestMissingJavaAndMaestroGiveSetupGuidance(t *testing.T) {
	e, fake := testEngine(t, "human", nil)
	err := e.CheckEnvironment("check")
	if err == nil || !strings.Contains(err.Error(), "Homebrew") {
		t.Fatalf("missing Java: %v", err)
	}
	fake.Paths["java"], fake.Paths["adb"] = "/bin/java", "/bin/adb"
	err = e.CheckEnvironment("run")
	if err == nil || !strings.Contains(err.Error(), "mockstrow check") {
		t.Fatalf("missing Maestro: %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatal("environment check attempted installation")
	}
}

func yes() bool { return true }
