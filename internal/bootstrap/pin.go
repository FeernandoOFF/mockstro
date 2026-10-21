package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/feernandooff/mockstro/internal/project"
	"gopkg.in/yaml.v3"
)

func pinnedConfig(root, version string) (string, []byte, error) {
	if _, err := project.Load(root); err != nil {
		return "", nil, fmt.Errorf("bootstrap needs a valid .maestro/mockstro.yaml; use init for a new suite: %w", err)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, ".maestro", "mockstro.yaml"))
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", nil, err
	}
	config := doc.Content[0]
	found := false
	for i := 0; i < len(config.Content); i += 2 {
		if config.Content[i].Value == "cliVersion" {
			if config.Content[i+1].Value != version {
				return "", nil, fmt.Errorf("refusing to replace existing cliVersion in %s", path)
			}
			if config.Content[i+1].Anchor != "" {
				return "", nil, fmt.Errorf("cliVersion in %s must be a plain value, not an anchor", path)
			}
			// Normalize the pin to a standalone scalar the shell wrapper can read.
			config.Content[i+1].Style = 0
			config.Content[i].Style = 0
			found = true
			break
		}
	}
	if !found {
		config.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "cliVersion"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: version},
		}, config.Content...)
	}
	config.Style = 0
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", nil, err
	}
	if err := enc.Close(); err != nil {
		return "", nil, err
	}
	return path, out.Bytes(), nil
}

func replaceConfig(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mockstro-pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		f.Close()
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
	return os.Rename(f.Name(), path)
}
