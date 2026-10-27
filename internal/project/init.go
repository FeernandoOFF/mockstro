package project

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Init writes a small suite around any existing .maestro files, which are kept.
// It returns the template files it skipped.
func Init(root string, cfg Config) ([]string, error) {
	if cfg.CLIVersion != "" {
		if err := ValidateCLIVersion(cfg.CLIVersion); err != nil {
			return nil, err
		}
	}
	if len(cfg.Platforms) == 0 {
		return nil, fmt.Errorf("provide --android-app-id and/or --ios-app-id")
	}
	for name, t := range cfg.Platforms {
		if name != "android" && name != "ios" {
			return nil, fmt.Errorf("unknown platform %q", name)
		}
		if t.AppID == "" || strings.Contains(t.AppID, "${") {
			return nil, fmt.Errorf("%s needs a literal appId", name)
		}
		if t.Integration == "" {
			t.Integration = "lite"
		}
		if t.Integration != "lite" && t.Integration != "full" {
			return nil, fmt.Errorf("integration must be lite or full")
		}
		cfg.Platforms[name] = t
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		"mockstro.yaml":                  data,
		"config.yaml":                    []byte("flows:\n  - flows/**\n"),
		"flows/smoke/launch.yaml":        []byte("appId: ${APP_ID}\nname: Launch app\ntags: [smoke]\nmockstro:\n  apiProfile: default\n---\n- launchApp\n# Add assertions and native runFlow.when.platform conditions as needed.\n"),
		"shared/.gitkeep":                nil,
		"wiremock/stubs/health.json":     []byte("{\"name\":\"health\",\"request\":{\"method\":\"GET\",\"url\":\"/api/health\"},\"response\":{\"status\":200,\"jsonBody\":{\"status\":\"ok\"}}}\n"),
		"wiremock/__files/.gitkeep":      nil,
		"wiremock/profiles/shared.json":  []byte("{\"mappings\":[]}\n"),
		"wiremock/profiles/default.json": []byte("{\"mappings\":[\"health\"]}\n"),
	}
	maestro := filepath.Join(root, ".maestro")
	config := filepath.Join(maestro, "mockstro.yaml")
	if _, err := os.Lstat(config); !os.IsNotExist(err) {
		return nil, fmt.Errorf("%s already exists", config)
	}
	var skipped []string
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(maestro, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			skipped = append(skipped, rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(files[rel])
		closeErr := f.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	// Keep machine paths and generated reports out of the owning suite's history.
	ignore := filepath.Join(maestro, ".gitignore")
	b, err := os.ReadFile(ignore)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
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
	return skipped, os.WriteFile(ignore, []byte(text), 0644)
}
