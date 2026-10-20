package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Init writes a complete small suite. Existing template files are never replaced.
func Init(root string, cfg Config) error {
	if len(cfg.Platforms) == 0 {
		return fmt.Errorf("provide --android-app-id and/or --ios-app-id")
	}
	for name, t := range cfg.Platforms {
		if name != "android" && name != "ios" {
			return fmt.Errorf("unknown platform %q", name)
		}
		if t.AppID == "" || strings.Contains(t.AppID, "${") {
			return fmt.Errorf("%s needs a literal appId", name)
		}
		if t.Integration == "" {
			t.Integration = "lite"
		}
		if t.Integration != "lite" && t.Integration != "full" {
			return fmt.Errorf("integration must be lite or full")
		}
		cfg.Platforms[name] = t
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"mockstro.yaml":                  data,
		"config.yaml":                    []byte("flows:\n  - flows/**\n"),
		"flows/smoke/launch.yaml":        []byte("appId: ${APP_ID}\nname: Launch app\ntags: [smoke]\nproperties:\n  apiProfile: default\n---\n- launchApp\n# Add assertions and native runFlow.when.platform conditions as needed.\n"),
		"shared/.gitkeep":                nil,
		"wiremock/stubs/health.json":     []byte("{\"name\":\"health\",\"request\":{\"method\":\"GET\",\"url\":\"/api/health\"},\"response\":{\"status\":200,\"jsonBody\":{\"status\":\"ok\"}}}\n"),
		"wiremock/__files/.gitkeep":      nil,
		"wiremock/profiles/shared.json":  []byte("{\"mappings\":[]}\n"),
		"wiremock/profiles/default.json": []byte("{\"mappings\":[\"health\"]}\n"),
	}
	maestro := filepath.Join(root, ".maestro")
	for rel := range files {
		path := filepath.Join(maestro, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("refusing to overwrite %s", path)
		}
	}
	for rel, data := range files {
		path := filepath.Join(maestro, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Keep machine paths and generated reports out of the owning suite's history.
	ignore := filepath.Join(maestro, ".gitignore")
	b, err := os.ReadFile(ignore)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(b)
	for _, rule := range []string{"/mockstro.local.yaml", "/.runtime/"} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			found = found || line == rule
		}
		if !found {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			text += rule + "\n"
		}
	}
	return os.WriteFile(ignore, []byte(text), 0644)
}
