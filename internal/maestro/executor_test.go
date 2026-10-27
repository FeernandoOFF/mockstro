package maestro_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/maestro"
)

func executor(t *testing.T) (*maestro.Executor, maestro.Flow, *execx.Fake) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.yaml")
	if err := os.WriteFile(path, []byte("appId: ${APP_ID}\n---\n- launchApp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte("platform: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Paths: map[string]string{"maestro": "maestro"}}
	x := &maestro.Executor{Exec: fake, Device: "device-1", Workspace: maestro.Workspace{Dir: dir, FlowsRoot: filepath.Join(dir, "workspace"), SharedRoot: filepath.Join(dir, "shared"), ConfigPath: config}}
	return x, maestro.Flow{Source: &fixtures.Flow{Path: path, Rel: "feature/test.yaml"}, Variables: map[string]string{"Z": "last", "APP_ID": "app"}, LogPath: filepath.Join(dir, "logs", "flow.log")}, fake
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRunBuildsCommandStreamsOutputAndCollectsNewArtifacts(t *testing.T) {
	for _, recording := range []string{"", "recording.mp4"} {
		t.Run(map[bool]string{true: "record", false: "test"}[recording != ""], func(t *testing.T) {
			x, f, fake := executor(t)
			old := filepath.Join(x.Workspace.Dir, "results", "old", "flow")
			writeFile(t, filepath.Join(old, "screenshots", "old.png"), "")
			var fresh string
			var output []string
			x.Output = func(flow, line string) {
				if flow != f.Source.Rel {
					t.Errorf("flow %q", flow)
				}
				output = append(output, line)
			}
			fake.Handler = func(c execx.Cmd) (string, error) {
				args := []string{"--device", "device-1", "test"}
				if recording != "" {
					args = []string{"--device", "device-1", "record", "--local"}
				}
				debug := c.Args[len(args)+3]
				if !strings.HasPrefix(debug, filepath.Join(x.Workspace.Dir, "results", "flow-")) {
					t.Fatalf("output directory %s", debug)
				}
				fresh = filepath.Join(debug, "flow")
				args = append(args, "--config", x.Workspace.ConfigPath, "--debug-output", debug)
				if recording == "" {
					args = append(args, "--flatten-debug-output")
				} else {
					fresh = filepath.Join(debug, ".maestro", "tests", "ts", "flow")
				}
				args = append(args, "-e", "APP_ID=app", "-e", "Z=last", f.Source.Path)
				if recording != "" {
					args = append(args, recording)
				}
				if c.Name != "maestro" || !reflect.DeepEqual(c.Args, args) {
					t.Errorf("command %s, want args %v", c, args)
				}
				data, err := os.ReadFile(args[len(args)-1])
				if recording != "" {
					data, err = os.ReadFile(args[len(args)-2])
				}
				if err != nil || !strings.Contains(string(data), "launchApp") {
					t.Errorf("sanitized flow %q err %v", data, err)
				}
				io.WriteString(c.Stdout, "hello\r\nprogress\rfinished\n")
				io.WriteString(c.Stderr, "trailing")
				writeFile(t, filepath.Join(fresh, "manifest.json"), `{"entries":[{"kind":"SCREENSHOT","relativePath":"screenshots","count":2}]}`)
				writeFile(t, filepath.Join(fresh, "screenshots", "b.png"), "")
				writeFile(t, filepath.Join(fresh, "screenshots", "a.png"), "")
				return "", nil
			}
			out := x.Run(context.Background(), f, recording)
			if out.Exit != 0 || out.Err != nil || out.Setup || out.Cancelled || out.Artifacts.Dir != fresh || !reflect.DeepEqual(out.Artifacts.Shots, []string{filepath.Join(fresh, "screenshots", "a.png"), filepath.Join(fresh, "screenshots", "b.png")}) {
				t.Fatalf("outcome %+v", out)
			}
			if !reflect.DeepEqual(output, []string{"hello", "finished", "trailing"}) {
				t.Fatalf("output %q", output)
			}
			log, err := os.ReadFile(f.LogPath)
			if err != nil || string(log) != "hello\r\nprogress\rfinished\ntrailing" {
				t.Fatalf("log %q err %v", log, err)
			}
		})
	}
}

func TestRunClassifiesFailuresAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		cancel    bool
		exit      int
		cancelled bool
	}{
		{name: "unexpected error", err: errors.New("runner error"), exit: 1},
		{name: "exit status", err: exitError(7), exit: 7},
		{name: "interrupt", err: exitError(130), exit: 130, cancelled: true},
		{name: "terminated", err: exitError(143), exit: 143, cancelled: true},
		{name: "cancelled context", cancel: true, exit: 130, cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, f, fake := executor(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake.Handler = func(execx.Cmd) (string, error) {
				if tc.cancel {
					cancel()
				}
				return "", tc.err
			}
			out := x.Run(ctx, f, "")
			if out.Exit != tc.exit || out.Cancelled != tc.cancelled || out.Setup || !errors.Is(out.Err, tc.err) {
				t.Fatalf("outcome %+v", out)
			}
		})
	}
}
func exitError(code int) error {
	return exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}

func TestRunSetupFailureDoesNotLaunchMaestro(t *testing.T) {
	for _, failure := range []string{"source", "command", "log"} {
		t.Run(failure, func(t *testing.T) {
			x, f, fake := executor(t)
			switch failure {
			case "source":
				f.Source.Path += ".missing"
			case "command":
				fake.Paths = nil
			case "log":
				f.LogPath = x.Workspace.ConfigPath + "/flow.log"
			}
			out := x.Run(context.Background(), f, "")
			if out.Exit != 1 || out.Err == nil || !out.Setup || out.Cancelled || len(fake.Calls) != 0 {
				t.Fatalf("outcome %+v calls %v", out, fake.Calls)
			}
		})
	}
}

func TestCommandFallsBackToConfiguredInstallation(t *testing.T) {
	t.Setenv("MAESTRO_DIR", t.TempDir())
	path := filepath.Join(os.Getenv("MAESTRO_DIR"), "bin", "maestro")
	fake := &execx.Fake{Paths: map[string]string{path: path}}
	if got, err := maestro.Command(fake); err != nil || got != path {
		t.Fatalf("Command = %q, %v", got, err)
	}
}

func TestFlowPathUsesOriginalNamespaceAndCopiesOnlyLegacyMetadata(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			x, f, _ := executor(t)
			metadata := "mockstro:\n  apiProfile: default\nproperties:\n  owner: qa\n"
			if legacy {
				metadata = "properties:\n  owner: qa\n  apiProfile: default\n"
			}
			source := "appId: app\n" + metadata + "---\n- runScript: ./relative.js\n"
			writeFile(t, f.Source.Path, source)
			path, err := x.Workspace.FlowPath(f.Source)
			if err != nil {
				t.Fatal(err)
			}
			if (path == f.Source.Path) == legacy {
				t.Fatalf("flow path %q, legacy=%v", path, legacy)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "owner: qa") {
				t.Fatalf("reporting properties lost: %s %v", data, err)
			}
			original, _ := os.ReadFile(f.Source.Path)
			if string(original) != source {
				t.Fatal("source changed")
			}
		})
	}
}
