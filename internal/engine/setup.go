package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
)

const javaInstallHint = "install Java 17+ with `brew install --cask temurin@17` on macOS (Linux/Homebrew: `brew install openjdk@17`), then check JAVA_HOME and rerun check"

var javaVersion = regexp.MustCompile(`(?:openjdk|java)\s+(?:version\s+)?"?(\d+)(?:\.(\d+))?`)

// maestroCommand also finds the official install before its bin directory is on PATH.
func (e *Engine) maestroCommand() (string, error) {
	if _, err := e.Exec.LookPath("maestro"); err == nil {
		return "maestro", nil
	}
	dir := os.Getenv("MAESTRO_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "maestro", err
		}
		dir = filepath.Join(home, ".maestro")
	}
	path, err := e.Exec.LookPath(filepath.Join(dir, "bin", "maestro"))
	if err != nil {
		return "maestro", err
	}
	return path, nil
}

// EnsureMaestro installs Maestro only when confirm agrees; existing installs are left alone.
func (e *Engine) EnsureMaestro(ctx context.Context, confirm func() bool) error {
	// macOS supplies a java launcher even when no JDK is installed.
	var version bytes.Buffer
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := e.Exec.Run(checkCtx, execx.Cmd{Name: "java", Args: []string{"-version"}, Stdout: &version, Stderr: &version})
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("Java is unavailable: %w; %s", err, javaInstallHint)
	}
	match := javaVersion.FindStringSubmatch(version.String())
	major := 0
	if len(match) > 1 {
		major, _ = strconv.Atoi(match[1])
	}
	if major < 17 {
		return fmt.Errorf("Java 17+ is required (java -version: %s); %s", strings.TrimSpace(version.String()), javaInstallHint)
	}
	if command, err := e.maestroCommand(); err == nil {
		e.Logf("Maestro is ready at %s (existing installation).", command)
		return nil
	}
	if !confirm() {
		return fmt.Errorf("Maestro is not installed; run `./mockstrow check --install` to install it")
	}
	out := e.outputWriter(0, "")
	if _, err := e.Exec.LookPath("brew"); err == nil {
		e.Logf("Installing Maestro with Homebrew...")
		if err := e.Exec.Run(ctx, execx.Cmd{Name: "brew", Args: []string{"install", "mobile-dev-inc/tap/maestro"}, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("install Maestro: %w", err)
		}
	} else {
		for _, tool := range []string{"bash", "curl", "unzip"} {
			if _, err := e.Exec.LookPath(tool); err != nil {
				return fmt.Errorf("installing Maestro requires %s; install it and rerun check", tool)
			}
		}
		e.Logf("Installing Maestro with the official installer (https://get.maestro.mobile.dev)...")
		script, err := e.Exec.Output(ctx, execx.Cmd{Name: "curl", Args: []string{
			"--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https",
			"--connect-timeout", "15", "--max-time", "120", "https://get.maestro.mobile.dev",
		}})
		if err != nil {
			return fmt.Errorf("download Maestro installer: %w", err)
		}
		if strings.TrimSpace(script) == "" {
			return fmt.Errorf("download Maestro installer: empty response")
		}
		if err := e.Exec.Run(ctx, execx.Cmd{Name: "bash", Args: []string{"-s"}, Stdin: strings.NewReader(script), Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("install Maestro: %w", err)
		}
	}
	command, err := e.maestroCommand()
	if err != nil {
		return fmt.Errorf("Maestro installer finished but the executable was not found; check PATH or MAESTRO_DIR and rerun check")
	}
	if _, err := e.Exec.Output(ctx, execx.Cmd{Name: command, Args: []string{"--version"}, Stderr: out}); err != nil {
		return fmt.Errorf("Maestro was installed but could not start: %w", err)
	}
	e.Logf("Maestro is ready at %s.", command)
	return nil
}
