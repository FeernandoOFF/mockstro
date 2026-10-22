package execx_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
)

func TestStartPreservesCommandIOEnvAndDirectory(t *testing.T) {
	for _, detach := range []bool{false, true} {
		dir := t.TempDir()
		var out, stderr bytes.Buffer
		p, err := (execx.Real{}).Start(execx.Cmd{Name: "/bin/sh", Args: []string{"-c", `read value; printf '%s:%s' "$TEST_EXECX_VALUE" "$value"; pwd; printf stderr >&2`}, Dir: dir, Env: []string{"TEST_EXECX_VALUE=env"}, Stdin: strings.NewReader("input\n"), Stdout: &out, Stderr: &stderr}, detach)
		if err != nil {
			t.Fatal(err)
		}
		if p.PID() <= 0 {
			t.Errorf("PID %d", p.PID())
		}
		if err := p.Wait(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "env:input") || !strings.Contains(out.String(), dir) || stderr.String() != "stderr" {
			t.Fatalf("stdout %q stderr %q", out.String(), stderr.String())
		}
	}
}

func TestProcessSignalKillAndExitCode(t *testing.T) {
	for _, kill := range []bool{false, true} {
		p, err := (execx.Real{}).Start(execx.Cmd{Name: "/bin/sleep", Args: []string{"30"}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if kill {
			err = p.Kill()
		} else {
			err = p.Signal(os.Interrupt)
		}
		if err != nil {
			t.Fatal(err)
		}
		err = p.Wait()
		want := 128 + int(syscall.SIGINT)
		if kill {
			want = 128 + int(syscall.SIGKILL)
		}
		if got := execx.ExitCode(err); got != want {
			t.Fatalf("ExitCode(%v) = %d, want %d", err, got, want)
		}
	}
	if _, err := (execx.Real{}).Start(execx.Cmd{Name: "/does/not/exist"}, false); err == nil {
		t.Fatal("missing executable started")
	}
}

func TestOutputCapturesWrappedExitAndStderr(t *testing.T) {
	out, err := (execx.Real{}).Output(context.Background(), execx.Cmd{Name: "/bin/sh", Args: []string{"-c", `printf ' out\r\n'; printf ' problem\n' >&2; exit 7`}})
	var commandError *execx.Error
	if out != "out" || execx.ExitCode(err) != 7 || !errors.As(err, &commandError) || commandError.Stderr != "problem" {
		t.Fatalf("out %q err %#v", out, err)
	}
}
