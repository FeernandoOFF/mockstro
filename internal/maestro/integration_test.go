package maestro_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/maestro"
)

// Opt in with an explicit disposable Android emulator; no application is launched.
func TestRealMaestroMetadataAndArtifacts(t *testing.T) {
	device := os.Getenv("MOCKSTRO_TEST_MAESTRO_DEVICE")
	if device == "" {
		t.Skip("set MOCKSTRO_TEST_MAESTRO_DEVICE to a disposable Android emulator")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeFile(t, filepath.Join(source, "config.yaml"), "platform: {}\n")
	writeFile(t, filepath.Join(source, "shared", "relative.js"), "output.probe = 'resolved';\n")
	makeFlow := func(name string, legacy bool) maestro.Flow {
		header := "mockstro:\n  apiProfile: default\n  apiOverrides: [example]\nproperties:\n  owner: qa\n"
		if legacy {
			header = "properties:\n  owner: qa\n  apiProfile: default\n  apiOverrides: [example]\n"
		}
		path := filepath.Join(source, "flows", "f", name+".yaml")
		writeFile(t, path, "appId: com.android.settings\nname: Same name\n"+header+"---\n- runScript: ../../shared/relative.js\n- assertTrue: ${output.probe === 'resolved'}\n- takeScreenshot: "+name+"\n")
		return maestro.Flow{Source: &fixtures.Flow{Path: path, Rel: "f/" + name + ".yaml"}, LogPath: filepath.Join(root, "run", "logs", name+".log")}
	}
	x := &maestro.Executor{Exec: execx.Real{}, Device: device, Workspace: maestro.Workspace{
		Dir: filepath.Join(root, "run"), FlowsRoot: filepath.Join(root, "run", "workspace"),
		SharedRoot: filepath.Join(source, "shared"), ConfigPath: filepath.Join(source, "config.yaml"),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	flows := []maestro.Flow{makeFlow("namespaced", false), makeFlow("legacy", true)}
	checkArtifacts := func(a maestro.Artifacts, name string) {
		t.Helper()
		if len(a.Shots) != 1 || filepath.Base(a.Shots[0]) != name+".png" {
			t.Fatalf("artifacts for %s: %+v", name, a)
		}
		if _, err := os.Stat(filepath.Join(a.Dir, "manifest.json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, flow := range flows {
		out := x.Run(ctx, flow, "")
		if out.Exit != 0 || out.Err != nil {
			log, _ := os.ReadFile(flow.LogPath)
			t.Fatalf("isolated outcome %+v\n%s", out, log)
		}
		checkArtifacts(out.Artifacts, strings.TrimSuffix(filepath.Base(flow.Source.Path), ".yaml"))
	}
	published := make([]maestro.Artifacts, len(flows))
	completed := 0
	halted := x.Batch(ctx, flows, maestro.Hooks{
		Initialize: func(int) {}, Start: func(int) error { return nil },
		Complete: func(_ int, out maestro.Outcome) bool {
			completed++
			if out.Exit != 0 || out.Err != nil {
				t.Errorf("batch outcome %+v", out)
			}
			return out.Exit == 0
		},
		Publish: func(i int, a maestro.Artifacts) { published[i] = a },
	}, false)
	if halted || completed != len(flows) {
		t.Fatalf("batch halted=%v completed=%d", halted, completed)
	}
	checkArtifacts(published[0], "namespaced")
	checkArtifacts(published[1], "legacy")
	if published[0].Dir == published[1].Dir {
		t.Fatal("duplicate flow names shared an artifact bundle")
	}
	recording := filepath.Join(root, "recording.mp4")
	out := x.Run(ctx, flows[0], recording)
	if out.Exit != 0 || out.Err != nil {
		t.Fatalf("record outcome %+v", out)
	}
	checkArtifacts(out.Artifacts, "namespaced")
	if info, err := os.Stat(recording); err != nil || info.Size() == 0 {
		t.Fatalf("recording %v %v", info, err)
	}
}
