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
	if err := initCmd(g, "com.acme.app"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/.maestro/flows/example/smoke.yaml")
	if err != nil || !strings.Contains(string(b), "appId: com.acme.app") {
		t.Fatalf("flow: %v %s", err, b)
	}
	if err := initCmd(g, "x"); err == nil {
		t.Fatal("second init should refuse an existing .maestro")
	}
}

func TestInitScaffoldRunsOnBothPlatforms(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			if err := initCmd(&globals{projectDir: dir, platform: platform}, "com.acme.app"); err != nil {
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
			if flows[0].Header.AppID != "com.acme.app" {
				t.Fatalf("app ID: %q", flows[0].Header.AppID)
			}
		})
	}
}
