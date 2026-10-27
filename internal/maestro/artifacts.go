package maestro

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// findArtifacts searches only this invocation's output, including record's nested
// debug tree. A manifest identifies the bundle independently of Maestro's layout.
func findArtifacts(root string) Artifacts {
	var bundles []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == "manifest.json" {
			bundles = append(bundles, filepath.Dir(path))
		}
		return nil
	})
	if len(bundles) > 0 {
		return artifacts(bundles[0])
	}
	// Older Maestro versions have no manifest. Keep their screenshot bundles readable.
	var legacy string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && (entry.Name() == "screenshots" || entry.Name() == "takeScreenshot") && legacy == "" {
			legacy = filepath.Dir(path)
		}
		return nil
	})
	if legacy != "" {
		return artifacts(legacy)
	}
	return Artifacts{}
}

func artifacts(dir string) Artifacts {
	out := Artifacts{Dir: dir}
	var manifest struct {
		Entries []struct {
			Kind         string `json:"kind"`
			RelativePath string `json:"relativePath"`
			Count        *int   `json:"count"`
		} `json:"entries"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err == nil {
		err = json.Unmarshal(data, &manifest)
	}
	paths := map[string]bool{}
	add := func(relative string) {
		path := artifactPath(dir, relative)
		if path != "" && strings.EqualFold(filepath.Ext(path), ".png") {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				paths[path] = true
			}
		}
	}
	if err == nil {
		for _, entry := range manifest.Entries {
			switch entry.Kind {
			case "SCREENSHOT", "TAKE_SCREENSHOT", "SCREENSHOT_DIFF":
			default:
				continue // Manifest kinds are extensible; ignore additions we don't display.
			}
			if entry.Count == nil {
				add(entry.RelativePath)
				continue
			}
			path := artifactPath(dir, entry.RelativePath)
			if path == "" {
				continue
			}
			_ = filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					rel, _ := filepath.Rel(dir, path)
					add(rel)
				}
				return nil
			})
		}
	} else {
		for _, folder := range []string{"screenshots", "takeScreenshot"} {
			shots, _ := filepath.Glob(filepath.Join(dir, folder, "*.png"))
			for _, shot := range shots {
				rel, _ := filepath.Rel(dir, shot)
				add(rel)
			}
		}
	}
	// Commands carry the execution order, including explicit screenshot commands
	// interleaved with failure screenshots. Only include paths allowed by the manifest.
	var commands []struct {
		Metadata struct {
			Artifacts []struct{ Path string } `json:"artifacts"`
		} `json:"metadata"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "commands.json")); err == nil && json.Unmarshal(data, &commands) == nil {
		for _, command := range commands {
			for _, artifact := range command.Metadata.Artifacts {
				path := artifactPath(dir, artifact.Path)
				if paths[path] {
					out.Shots = append(out.Shots, path)
					delete(paths, path)
				}
			}
		}
	}
	var remaining []string
	for path := range paths {
		remaining = append(remaining, path)
	}
	slices.Sort(remaining)
	out.Shots = append(out.Shots, remaining...)
	return out
}

// artifactPath rejects absolute/traversing paths and symlinks escaping the bundle.
func artifactPath(dir, relative string) string {
	if relative == "" || filepath.IsAbs(relative) || !filepath.IsLocal(relative) {
		return ""
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, relative)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	return path
}

// Flow directories exist before onFlowStart; their manifests are finalized after
// onFlowComplete. Batch associates the directory at the hook, then reads its manifest.
func flowDirs(root string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			out[filepath.Join(root, entry.Name())] = true
		}
	}
	return out
}
