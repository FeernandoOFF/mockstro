// Package paths holds the machine-wide locations mockstro shares across every project.
package paths

import (
	"os"
	"path/filepath"
)

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// StateRoot holds the global locks and run registry, so runs from any repository see each other.
func StateRoot() string {
	if v := os.Getenv("MOCKSTRO_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "mockstro")
	}
	return filepath.Join(home(), ".local", "state", "mockstro")
}

func LocksRoot() string    { return filepath.Join(StateRoot(), "locks") }
func RegistryRoot() string { return filepath.Join(StateRoot(), "runs") }

func CacheRoot() string {
	if v := os.Getenv("MOCKSTRO_CACHE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "mockstro")
	}
	return filepath.Join(home(), ".cache", "mockstro")
}

func ConfigRoot() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "mockstro")
	}
	return filepath.Join(home(), ".config", "mockstro")
}

// DesignatedFile lists AVD / simulator names any project may drive, one per line.
func DesignatedFile() string { return filepath.Join(ConfigRoot(), "designated.txt") }
