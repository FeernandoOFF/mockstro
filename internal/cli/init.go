package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/project"
)

// scaffold is a minimal lite-integration .maestro: one flow, one profile, one stub.
func scaffold(appID string) map[string]string {
	return map[string]string{
		"config.yaml": "flows:\n  - flows/**\n",
		"flows/example/smoke.yaml": fmt.Sprintf(`appId: %s
tags: [smoke, android, ios]
properties:
  apiProfile: default
---
- launchApp
`, appID),
		"wiremock/stubs/example-hello.json": `{
  "name": "example-hello",
  "request": { "method": "GET", "urlPath": "/api/hello" },
  "response": {
    "status": 200,
    "headers": { "Content-Type": "application/json" },
    "bodyFileName": "example-hello.json"
  }
}
`,
		"wiremock/__files/example-hello.json": "{ \"message\": \"Hello from WireMock\" }\n",
		"wiremock/profiles/shared.json":       "{ \"mappings\": [] }\n",
		"wiremock/profiles/default.json":      "{ \"mappings\": [\"example-hello\"] }\n",
	}
}

func initCmd(g *globals, appID string) error {
	dir := g.projectDir
	if dir == "" {
		dir = "."
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	m := filepath.Join(root, ".maestro")
	if _, err := os.Stat(m); err == nil {
		return fmt.Errorf("%s already exists", m)
	}
	files := scaffold(appID)
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	slices.Sort(paths)
	for _, rel := range paths {
		path := filepath.Join(m, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(files[rel]), 0o644); err != nil {
			return err
		}
		fmt.Println("created .maestro/" + rel)
	}
	p, err := project.Load(root)
	if err != nil {
		return err
	}
	counts, err := fixtures.New(p).ValidateAll(true)
	if err != nil {
		return err
	}
	fmt.Println(counts)
	fmt.Println("Next: map your API to http://127.0.0.1:18080 (docs/integration.md), then `mockstro run`.")
	return nil
}
