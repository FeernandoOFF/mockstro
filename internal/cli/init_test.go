package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestInitScaffoldValidates(t *testing.T) {
	dir := t.TempDir()
	g := &globals{projectDir: dir}
	cmd := initCommand(g)
	cmd.SetArgs([]string{"--app-id", "com.acme.app"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/.maestro/flows/smoke/launch.yaml")
	if err != nil || !strings.Contains(string(b), "appId: ${APP_ID}") {
		t.Fatalf("flow: %v %s", err, b)
	}
	cmd = initCommand(g)
	cmd.SetArgs([]string{"--app-id", "x"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("second init should refuse an existing .maestro")
	}
}

func TestInitScaffoldRunsOnBothPlatforms(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			cmd := initCommand(&globals{projectDir: dir, platform: platform})
			cmd.SetArgs([]string{"--app-id", "com.acme.app"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			p, err := project.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			o := engine.DefaultOptions()
			o.Platform = platform
			e := engine.New(p, o, nil, nil)
			flows, err := e.DiscoverFlows()
			if err != nil || len(flows) != 1 {
				t.Fatalf("scaffold discovery: flows=%d err=%v", len(flows), err)
			}
			if flows[0].Header.AppID != "${APP_ID}" || e.P.AppID != "com.acme.app" {
				t.Fatalf("app ID: template=%q configured=%q", flows[0].Header.AppID, e.P.AppID)
			}
		})
	}
}
