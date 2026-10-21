package project

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetAgentDevice edits only the effective platform devices list, retaining YAML comments.
// Local-only platforms and local devices overrides own their lists.
func SetAgentDevice(root, platform, name string, allow bool) (*Project, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, "", fmt.Errorf("device name is empty")
	}
	p, err := Load(root)
	if err != nil {
		return nil, "", err
	}
	if p.Targets[platform] == nil {
		return nil, "", fmt.Errorf("platform %q is not configured", platform)
	}
	path := filepath.Join(p.MaestroRoot, "mockstro.yaml")
	local := filepath.Join(p.MaestroRoot, "mockstro.local.yaml")
	if b, err := os.ReadFile(local); err == nil {
		var doc yaml.Node
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, "", err
		}
		if len(doc.Content) > 0 {
			target := yamlValue(yamlValue(doc.Content[0], "platforms"), platform)
			if yamlValue(target, "devices") != nil || yamlValue(target, "appId") != nil {
				path = local
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	// Write through a config symlink rather than replacing the link itself.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, "", err
	}
	platforms := yamlValue(doc.Content[0], "platforms")
	target := yamlValue(platforms, platform)
	if platforms == nil || platforms.Kind != yaml.MappingNode || target == nil || target.Kind != yaml.MappingNode || target.Anchor != "" {
		return nil, "", fmt.Errorf("edit %s manually: platform %s must be a YAML mapping", path, platform)
	}
	devices := yamlValue(target, "devices")
	if devices != nil && devices.Anchor != "" {
		return nil, "", fmt.Errorf("edit %s manually: the devices list uses a shared YAML anchor", path)
	}
	if devices == nil {
		devices = &yaml.Node{}
		target.Content = append(target.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "devices"}, devices)
	}
	names := make([]string, 0, len(p.Targets[platform].Devices)+1)
	for _, existing := range p.Targets[platform].Devices {
		if existing != name {
			names = append(names, existing)
		}
	}
	if allow {
		names = append(names, name)
	}
	var list yaml.Node
	if err := list.Encode(names); err != nil {
		return nil, "", err
	}
	// Retain comments attached to individual names as well as the list itself.
	source := devices
	if source.Kind == yaml.AliasNode {
		source = source.Alias
	}
	if source != nil && source.Kind == yaml.SequenceNode {
		for i, item := range list.Content {
			for _, old := range source.Content {
				if old.Kind == yaml.ScalarNode && old.Value == item.Value {
					list.Content[i] = old
					break
				}
			}
		}
	}
	list.Anchor = devices.Anchor
	list.HeadComment, list.LineComment, list.FootComment = devices.HeadComment, devices.LineComment, devices.FootComment
	list.Style = devices.Style
	*devices = list
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, "", err
	}
	if err := enc.Close(); err != nil {
		return nil, "", err
	}
	if path == local {
		var check LocalConfig
		_, err = decode(out.Bytes(), &check)
	} else {
		var check Config
		_, err = decode(out.Bytes(), &check)
	}
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, "", err
	}
	f, err := os.CreateTemp(filepath.Dir(resolved), ".mockstro-devices-*")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		f.Close()
		return nil, "", err
	}
	_, writeErr := f.Write(out.Bytes())
	closeErr := f.Close()
	if writeErr != nil {
		return nil, "", writeErr
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if err := os.Rename(f.Name(), resolved); err != nil {
		return nil, "", err
	}
	updated, err := Load(root)
	return updated, path, err
}

func yamlValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping != nil && mapping.Kind == yaml.AliasNode {
		mapping = mapping.Alias
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
