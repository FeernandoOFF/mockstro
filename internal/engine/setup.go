package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/javaruntime"
	"github.com/feernandooff/mockstro/internal/maestro"
)

const javaInstallHint = javaruntime.Hint

// EnsureMaestro installs Maestro only when confirm agrees; existing installs are left alone.
func (e *Engine) EnsureMaestro(ctx context.Context, confirm func() bool) error {
	if err := javaruntime.Ensure(ctx, e.Exec, e.Logf); err != nil {
		return err
	}
	if command, err := maestro.Command(e.Exec); err == nil {
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
	command, err := maestro.Command(e.Exec)
	if err != nil {
		return fmt.Errorf("Maestro installer finished but the executable was not found; check PATH or MAESTRO_DIR and rerun check")
	}
	if _, err := e.Exec.Output(ctx, execx.Cmd{Name: command, Args: []string{"--version"}, Stderr: out}); err != nil {
		return fmt.Errorf("Maestro was installed but could not start: %w", err)
	}
	e.Logf("Maestro is ready at %s.", command)
	return nil
}
