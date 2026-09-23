// Package project locates a repository's .maestro directory and loads its per-repo settings.
// The on-disk layout is the one test.sh uses, so both runners can serve the same checkout.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ReservedMockPort  = 18080
	EmulatorPortBase  = 18081
	OverrideTTLSecs   = 14400
	DefaultProxyBase  = "https://apptest.prepaypower.eu:2019"
	DefaultFBRCURL    = "https://firebaseremoteconfig.googleapis.com"
	DefaultOverrideKy = "ppp_e2e_override"
	DefaultAcceptKey  = "prepaypower_e2e_accepted_run_id"
)

type Project struct {
	Root        string // repository (worktree) root
	MaestroRoot string // <root>/.maestro
	Env         map[string]string

	Platform    string
	AppID       string
	Variant     string
	GradleTask  string
	APKGlob     string
	IOSProject  string
	IOSScheme   string
	IOSConfig   string
	IOSSimNames []string

	ProxyBaseURL string
	FBRCURL      string
	OverrideKey  string
	AcceptKey    string
	FallbackURL  string

	// GitCommonDir is shared by every worktree; test.sh keeps its locks under <it>/maestro.
	GitCommonDir string
}

func (p *Project) FlowsRoot() string     { return filepath.Join(p.MaestroRoot, "flows") }
func (p *Project) ScenariosRoot() string { return filepath.Join(p.MaestroRoot, "scenarios") }
func (p *Project) SharedRoot() string    { return filepath.Join(p.MaestroRoot, "shared") }
func (p *Project) ConfigPath() string    { return filepath.Join(p.MaestroRoot, "config.yaml") }
func (p *Project) WiremockRoot() string  { return filepath.Join(p.MaestroRoot, "wiremock") }
func (p *Project) StubsRoot() string     { return filepath.Join(p.WiremockRoot(), "stubs") }
func (p *Project) BodiesRoot() string    { return filepath.Join(p.WiremockRoot(), "__files") }
func (p *Project) ProfilesRoot() string  { return filepath.Join(p.WiremockRoot(), "profiles") }
func (p *Project) BasesRoot() string     { return filepath.Join(p.WiremockRoot(), "bases") }
func (p *Project) RuntimeRoot() string   { return filepath.Join(p.MaestroRoot, ".runtime") }
func (p *Project) RunsRoot() string      { return filepath.Join(p.RuntimeRoot(), "runs") }
func (p *Project) RecordingsRoot() string {
	return filepath.Join(p.RuntimeRoot(), "wiremock", "recordings")
}
func (p *Project) LastRunPath() string {
	return filepath.Join(p.RuntimeRoot(), "results", "last-run.tsv")
}
func (p *Project) AgentAVDsFile() string { return filepath.Join(p.MaestroRoot, "agent-avds.txt") }

// LegacyLocksRoot is where test.sh keeps device/port locks for this repository.
func (p *Project) LegacySharedRoot() string {
	if p.GitCommonDir != "" {
		return filepath.Join(p.GitCommonDir, "maestro")
	}
	return filepath.Join(p.Root, "build", "maestro-shared")
}

func (p *Project) Rel(path string) string {
	if r, err := filepath.Rel(p.Root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

var ErrNotFound = errors.New("no .maestro directory found (run inside a repository that has one, or pass --project)")

// Find walks up from dir to the nearest directory containing .maestro/.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, ".maestro")); err == nil && st.IsDir() {
			if isMaestroRoot(filepath.Join(d, ".maestro")) {
				return d, nil
			}
		}
		if filepath.Dir(d) == d {
			return "", ErrNotFound
		}
	}
}

func isMaestroRoot(m string) bool {
	for _, marker := range []string{"flows", "wiremock", "project.env", "test.sh"} {
		if _, err := os.Stat(filepath.Join(m, marker)); err == nil {
			return true
		}
	}
	return false
}

// Load reads project.env on top of the process environment (project.env wins, as with
// test.sh's `set -a; . project.env`).
func Load(root string) (*Project, error) {
	p := &Project{Root: root, MaestroRoot: filepath.Join(root, ".maestro"), Env: map[string]string{}}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			p.Env[k] = v
		}
	}
	envFile := filepath.Join(p.MaestroRoot, "project.env")
	if vals, err := ParseEnvFile(envFile); err == nil {
		for k, v := range vals {
			p.Env[k] = v
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", envFile, err)
	}

	get := func(k, def string) string {
		if v := p.Env[k]; v != "" {
			return v
		}
		return def
	}
	p.Platform = get("MAESTRO_PLATFORM", "android")
	p.AppID = get("APP_PACKAGE", "ie.prepaypower.envtest")
	p.Variant = get("MAESTRO_APP_VARIANT", "debugEnvTest")
	switch p.Variant {
	case "debugEnvTest":
		p.GradleTask = get("ANDROID_GRADLE_TASK", "assembleDebugEnvTest")
		p.APKGlob = get("ANDROID_APK_GLOB", "app/build/outputs/apk/debugEnvTest/*.apk")
	case "envtest":
		p.GradleTask = "assembleEnvtest"
		p.APKGlob = "app/build/outputs/apk/envtest/*-envtest.apk"
	default:
		return nil, fmt.Errorf("unknown MAESTRO_APP_VARIANT: %s (expected debugEnvTest or envtest)", p.Variant)
	}
	p.IOSProject = get("IOS_PROJECT", "")
	p.IOSScheme = get("IOS_SCHEME", "")
	p.IOSConfig = get("IOS_CONFIGURATION", "Debug")
	for _, n := range strings.Split(get("IOS_SIMULATOR_NAMES", ""), ",") {
		if n = strings.TrimSpace(n); n != "" {
			p.IOSSimNames = append(p.IOSSimNames, n)
		}
	}
	p.ProxyBaseURL = get("E2E_PROXY_BASE_URL", DefaultProxyBase)
	p.FBRCURL = get("E2E_FBRC_URL", DefaultFBRCURL)
	p.OverrideKey = get("E2E_OVERRIDE_SETTING_KEY", DefaultOverrideKy)
	p.AcceptKey = get("E2E_ACCEPT_SETTING_KEY", DefaultAcceptKey)
	p.FallbackURL = get("E2E_FALLBACK_PROXY_URL", "")

	if out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				p.GitCommonDir = d
			}
		}
	}
	return p, nil
}

// ParseEnvFile reads simple KEY=VALUE lines (comments, blank lines, optional quotes, `export`).
func ParseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		vals[k] = v
	}
	return vals, sc.Err()
}
