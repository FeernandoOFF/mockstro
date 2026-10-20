package project

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Variables contains Maestro input strings, never process environment settings.
type Variables map[string]string

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateVariable(key string) error {
	if !variableName.MatchString(key) {
		return fmt.Errorf("invalid variable name %q", key)
	}
	if key == "APP_ID" || key == "WIREMOCK_PORT" {
		return fmt.Errorf("%s is controlled by mockstro; configure appId on the platform instead", key)
	}
	return nil
}

func (v *Variables) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("variables must be a YAML map of scalar values")
	}
	out := Variables{}
	for i := 0; i < len(n.Content); i += 2 {
		k, value := n.Content[i], n.Content[i+1]
		if err := ValidateVariable(k.Value); err != nil {
			return err
		}
		if _, exists := out[k.Value]; exists {
			return fmt.Errorf("duplicate variable %q", k.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
			return fmt.Errorf("variable %s must be a non-null scalar", k.Value)
		}
		out[k.Value] = value.Value
	}
	*v = out
	return nil
}

// ReadVariables reads a single YAML map. Relative CLI paths are relative to the caller.
func ReadVariables(path string) (Variables, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v Variables
	if err = decode(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if v == nil {
		return nil, fmt.Errorf("%s: expected a YAML map", path)
	}
	return v, nil
}

type BuildConfig struct {
	Task          string `yaml:"task,omitempty"`
	Artifact      string `yaml:"artifact,omitempty"`
	Project       string `yaml:"project,omitempty"` // .xcodeproj or .xcworkspace relative to target checkout
	Scheme        string `yaml:"scheme,omitempty"`
	Configuration string `yaml:"configuration,omitempty"`
}
type TargetConfig struct {
	Project      string      `yaml:"project,omitempty"`
	AppID        string      `yaml:"appId"`
	Integration  string      `yaml:"integration,omitempty"`
	Devices      []string    `yaml:"devices,omitempty"`
	Build        BuildConfig `yaml:"build,omitempty"`
	Env          Variables   `yaml:"env,omitempty"`
	ProxyBaseURL string      `yaml:"proxyBaseUrl,omitempty"`
	FallbackURL  string      `yaml:"fallbackUrl,omitempty"`
	FBRCURL      string      `yaml:"fbrcUrl,omitempty"`
	OverrideKey  string      `yaml:"overrideKey,omitempty"`
	AcceptKey    string      `yaml:"acceptKey,omitempty"`
}
type Config struct {
	Env       Variables               `yaml:"env,omitempty"`
	Platforms map[string]TargetConfig `yaml:"platforms"`
}

type LocalTarget struct {
	Project *string      `yaml:"project,omitempty"`
	Devices *[]string    `yaml:"devices,omitempty"`
	Build   *BuildConfig `yaml:"build,omitempty"`
	Env     Variables    `yaml:"env,omitempty"`
}
type LocalConfig struct {
	Env       Variables              `yaml:"env,omitempty"`
	Platforms map[string]LocalTarget `yaml:"platforms"`
}

func decode(b []byte, dest any) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one YAML document")
	}
	return nil
}

// Load requires the new typed config; legacy project.env is intentionally not read.
func Load(root string) (*Project, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	maestro := filepath.Join(root, ".maestro")
	path := filepath.Join(maestro, "mockstro.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := decode(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cfg.Platforms) == 0 {
		return nil, fmt.Errorf("%s: configure at least one platform", path)
	}
	var local LocalConfig
	path = filepath.Join(maestro, "mockstro.local.yaml")
	if b, err = os.ReadFile(path); err == nil {
		if err = decode(b, &local); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for name := range local.Platforms {
		if _, ok := cfg.Platforms[name]; !ok {
			return nil, fmt.Errorf("local override references unconfigured platform %q", name)
		}
	}
	targets := map[string]*Project{}
	for name, t := range cfg.Platforms {
		if name != "android" && name != "ios" {
			return nil, fmt.Errorf("unknown platform %q", name)
		}
		if t.AppID == "" || strings.Contains(t.AppID, "${") {
			return nil, fmt.Errorf("platform %s needs a literal appId", name)
		}
		if t.Integration == "" {
			t.Integration = "lite"
		}
		if t.Integration != "lite" && t.Integration != "full" {
			return nil, fmt.Errorf("platform %s: integration must be lite or full", name)
		}
		l := local.Platforms[name]
		if l.Project != nil {
			t.Project = *l.Project
		}
		if l.Devices != nil {
			t.Devices = *l.Devices
		}
		if l.Build != nil {
			// Build overrides patch individual fields, retaining shared defaults.
			if l.Build.Task != "" {
				t.Build.Task = l.Build.Task
			}
			if l.Build.Artifact != "" {
				t.Build.Artifact = l.Build.Artifact
			}
			if l.Build.Project != "" {
				t.Build.Project = l.Build.Project
			}
			if l.Build.Scheme != "" {
				t.Build.Scheme = l.Build.Scheme
			}
			if l.Build.Configuration != "" {
				t.Build.Configuration = l.Build.Configuration
			}
		}
		buildRoot := root
		if t.Project != "" {
			buildRoot = t.Project
			if !filepath.IsAbs(buildRoot) {
				buildRoot = filepath.Join(root, buildRoot)
			}
		}
		loc := maps.Clone(local.Env)
		if loc == nil {
			loc = Variables{}
		}
		maps.Copy(loc, l.Env)
		p := &Project{Root: root, MaestroRoot: maestro, BuildRoot: filepath.Clean(buildRoot), Platform: name, AppID: t.AppID, Integration: t.Integration,
			Variables: maps.Clone(cfg.Env), TargetVariables: maps.Clone(t.Env), LocalVariables: loc, Devices: append([]string(nil), t.Devices...),
			GradleTask: t.Build.Task, APKGlob: t.Build.Artifact, IOSProject: t.Build.Project, IOSScheme: t.Build.Scheme, IOSConfig: t.Build.Configuration,
			ProxyBaseURL: t.ProxyBaseURL, FallbackURL: t.FallbackURL, FBRCURL: t.FBRCURL, OverrideKey: t.OverrideKey, AcceptKey: t.AcceptKey, Env: map[string]string{}}
		// SDK discovery still uses the machine's actual environment, not test variables.
		for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
			p.Env[key] = os.Getenv(key)
		}
		p.Env["MAESTRO_AGENT_AVDS"] = strings.Join(t.Devices, ",")
		p.IOSSimNames = append([]string(nil), t.Devices...)
		if p.GradleTask == "" {
			p.GradleTask = "assembleDebug"
		}
		if p.APKGlob == "" {
			p.APKGlob = "app/build/outputs/apk/debug/*.apk"
		}
		if p.IOSConfig == "" {
			p.IOSConfig = "Debug"
		}
		if p.FBRCURL == "" {
			p.FBRCURL = DefaultFBRCURL
		}
		if p.OverrideKey == "" {
			p.OverrideKey = DefaultOverrideKy
		}
		if p.AcceptKey == "" {
			p.AcceptKey = DefaultAcceptKey
		}
		targets[name] = p
	}
	first := targets["android"]
	if first == nil {
		first = targets["ios"]
	}
	suite := *first
	suite.Targets = targets
	return &suite, nil
}
