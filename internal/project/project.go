// Package project locates a repository's .maestro directory and loads its per-repo settings.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ReservedMockPort  = 18080
	EmulatorPortBase  = 18081
	OverrideTTLSecs   = 14400
	DefaultFBRCURL    = "https://firebaseremoteconfig.googleapis.com"
	DefaultAcceptKey  = "mockstro_e2e_accepted_run_id"
	DefaultOverrideKy = "mockstro_e2e_override"
)

type Project struct {
	RunRoot         string // child run storage within a combined session
	Root            string // suite repository root
	BuildRoot       string // platform source checkout (defaults to Root)
	Targets         map[string]*Project
	Variables       Variables
	TargetVariables Variables
	LocalVariables  Variables
	Devices         []string
	MaestroRoot     string // <root>/.maestro
	Env             map[string]string

	BuildSystem         string
	KotlinAndroidModule string
	KotlinIOSModule     string
	KotlinVariant       string
	IOSAppGlob          string
	WireMockVersion     string
	WebSocketPath       string

	Platform    string
	AppID       string
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

	// Integration is explicitly configured per platform: lite or full.
	Integration string
}

// Lite: the app is not modified and traffic reaches WireMock via the user's Map Remote.
func (p *Project) Lite() bool { return p.Integration == "lite" }

func (p *Project) IntegrationLabel() string {
	if p.Lite() {
		return "lite · proxy (drop-in)"
	}
	return "full · runtime override"
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
func (p *Project) RunsRoot() string {
	if p.RunRoot != "" {
		return p.RunRoot
	}
	return filepath.Join(p.RuntimeRoot(), "runs")
}
func (p *Project) RecordingsRoot() string {
	return filepath.Join(p.RuntimeRoot(), "wiremock", "recordings")
}
func (p *Project) LastRunPath() string {
	return filepath.Join(p.RuntimeRoot(), "results", "last-run.tsv")
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
	for _, marker := range []string{"flows", "wiremock", "mockstro.yaml"} {
		if _, err := os.Stat(filepath.Join(m, marker)); err == nil {
			return true
		}
	}
	return false
}

// SourceRoot locates build inputs without changing suite-owned flow and fixture paths.
func (p *Project) SourceRoot() string {
	if p.BuildRoot != "" {
		return p.BuildRoot
	}
	return p.Root
}

// Selected returns configured targets in deterministic Android/iOS order.
func (p *Project) Selected(filter string) ([]*Project, error) {
	if filter != "" && filter != "android" && filter != "ios" {
		return nil, fmt.Errorf("unknown platform: %s", filter)
	}
	if len(p.Targets) == 0 {
		if filter != "" && filter != p.Platform {
			return nil, fmt.Errorf("platform %s is not configured", filter)
		}
		return []*Project{p}, nil
	}
	var out []*Project
	for _, name := range []string{"android", "ios"} {
		if target := p.Targets[name]; target != nil && (filter == "" || filter == name) {
			out = append(out, target)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("platform %s is not configured", filter)
	}
	return out, nil
}
