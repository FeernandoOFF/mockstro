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
	cmd := initCommand(g, "dev")
	cmd.SetArgs([]string{"--app-id", "com.acme.app"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/.maestro/flows/smoke/launch.yaml")
	if err != nil || !strings.Contains(string(b), "appId: ${APP_ID}") {
		t.Fatalf("flow: %v %s", err, b)
	}
	cmd = initCommand(g, "dev")
	cmd.SetArgs([]string{"--app-id", "x"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("second init should refuse app IDs for an existing suite")
	}
	if _, err := os.Stat(dir + "/mockstrow"); !os.IsNotExist(err) {
		t.Fatal("dev build added a wrapper")
	}
	// A release build pins itself and adds the wrapper to an existing suite.
	for range 2 {
		cmd = initCommand(g, "v1.2.3")
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Load(dir)
	pin, _ := os.ReadFile(dir + "/.maestro/mockstro.yaml")
	if _, statErr := os.Stat(dir + "/mockstrow"); err != nil || statErr != nil || !strings.Contains(string(pin), "cliVersion: v1.2.3") || p.AppID != "com.acme.app" {
		t.Fatalf("pin/wrapper: %v %v %s", err, statErr, pin)
	}
}

func TestInitScaffoldRunsOnBothPlatforms(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			cmd := initCommand(&globals{projectDir: dir, platform: platform}, "dev")
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
