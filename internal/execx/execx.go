// Package execx wraps external commands behind an interface so device, lock and build logic
// can be exercised in tests without adb, xcrun, gradle or a JVM.
package execx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Cmd describes one external invocation.
type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string // appended to os.Environ()
	Stdin  io.Reader
	Stdout io.Writer // when set, output is streamed and not captured
	Stderr io.Writer
}

func (c Cmd) String() string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// Runner executes commands. Output returns trimmed stdout.
type Runner interface {
	Output(ctx context.Context, c Cmd) (string, error)
	Run(ctx context.Context, c Cmd) error
	LookPath(name string) (string, error)
}

// Real runs commands on the host. Every child gets its own process group so a cancel kills
// the whole tree (maestro and gradle both fork JVMs).
type Real struct{}

func build(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

func (Real) Output(ctx context.Context, c Cmd) (string, error) {
	cmd := build(ctx, c)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	} else {
		cmd.Stderr = &errb
	}
	err := cmd.Run()
	if err != nil && errb.Len() > 0 {
		err = &Error{Cmd: c.String(), Err: err, Stderr: strings.TrimSpace(errb.String())}
	}
	return strings.TrimSpace(strings.ReplaceAll(out.String(), "\r", "")), err
}

func (Real) Run(ctx context.Context, c Cmd) error {
	cmd := build(ctx, c)
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = io.Discard
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	return cmd.Run()
}

func (Real) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Start launches a long-lived child (WireMock, an emulator) and returns it unwaited.
func Start(c Cmd, detach bool) (*exec.Cmd, error) {
	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	return cmd, cmd.Start()
}

type Error struct {
	Cmd    string
	Err    error
	Stderr string
}

func (e *Error) Error() string { return e.Cmd + ": " + e.Err.Error() + ": " + e.Stderr }
func (e *Error) Unwrap() error { return e.Err }

// ExitCode extracts a process exit status; -1 when unknown.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return -1
}
