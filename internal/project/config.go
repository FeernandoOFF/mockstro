package project

import (
	"bytes"
	"errors"
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
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func ValidateCLIVersion(version string) error {
	if !releaseTag.MatchString(version) {
		return fmt.Errorf("cliVersion must be a release tag such as v1.2.3 (got %q)", version)
	}
	return nil
}

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
	if _, err = decode(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if v == nil {
		return nil, fmt.Errorf("%s: expected a YAML map", path)
	}
	return v, nil
}

type BuildConfig struct {
	System        string `yaml:"system,omitempty"`
	Module        string `yaml:"module,omitempty"`
	Variant       string `yaml:"variant,omitempty"`
	Task          string `yaml:"task,omitempty"`
	Artifact      string `yaml:"artifact,omitempty"`
	Project       string `yaml:"project,omitempty"` // .xcodeproj or .xcworkspace relative to target checkout
	Scheme        string `yaml:"scheme,omitempty"`
	Configuration string `yaml:"configuration,omitempty"`
}
type TargetConfig struct {
	WireMockVersion string      `yaml:"wiremockVersion,omitempty"`
	WebSocketPath   string      `yaml:"websocketPath,omitempty"`
	Project         string      `yaml:"project,omitempty"`
	AppID           string      `yaml:"appId"`
	Integration     string      `yaml:"integration,omitempty"`
	Devices         []string    `yaml:"devices,omitempty"`
	Build           BuildConfig `yaml:"build,omitempty"`
	Env             Variables   `yaml:"env,omitempty"`
	ProxyBaseURL    string      `yaml:"proxyBaseUrl,omitempty"`
	FallbackURL     string      `yaml:"fallbackUrl,omitempty"`
	FBRCURL         string      `yaml:"fbrcUrl,omitempty"`
	OverrideKey     string      `yaml:"overrideKey,omitempty"`
	AcceptKey       string      `yaml:"acceptKey,omitempty"`
}
type Config struct {
	CLIVersion string                  `yaml:"cliVersion,omitempty"`
	Env        Variables               `yaml:"env,omitempty"`
	Platforms  map[string]TargetConfig `yaml:"platforms"`
}

type LocalTarget struct {
	Project *string      `yaml:"project,omitempty"`
	Devices *[]string    `yaml:"devices,omitempty"`
	Build   *BuildConfig `yaml:"build,omitempty"`
	Env     Variables    `yaml:"env,omitempty"`
}
type LocalConfig struct {
	Env Variables `yaml:"env,omitempty"`
	// Shared platforms accept machine overrides; new platforms accept a full definition.
	Platforms map[string]yaml.Node `yaml:"platforms"`
}

// decode returns unknown keys as warnings rather than errors, so configs written
// for a newer CLI still load.
func decode(b []byte, dest any) ([]string, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	err := decodeOne(b, dest, true)
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return nil, err
	}
	for _, msg := range typeErr.Errors {
		if !strings.Contains(msg, "not found in type") {
			return nil, err
		}
	}
	return typeErr.Errors, decodeOne(b, dest, false)
}

func decodeOne(b []byte, dest any, strict bool) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(strict)
	if err := d.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one YAML document")
	}
	return nil
}

// resolveAliases copies n with aliases inlined, so it can be re-encoded on its own.
func resolveAliases(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return resolveAliases(n.Alias)
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = resolveAliases(child)
	}
	return &c
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
	var warnings []string
	warn := func(path string, msgs []string) {
		for _, msg := range msgs {
			warnings = append(warnings, fmt.Sprintf("%s: %s (ignored)", path, msg))
		}
	}
	var cfg Config
	msgs, err := decode(b, &cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	warn(path, msgs)
	if cfg.CLIVersion != "" {
		if err := ValidateCLIVersion(cfg.CLIVersion); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if len(cfg.Platforms) == 0 {
		return nil, fmt.Errorf("%s: configure at least one platform", path)
	}
	var local LocalConfig
	path = filepath.Join(maestro, "mockstro.local.yaml")
	if b, err = os.ReadFile(path); err == nil {
		msgs, err := decode(b, &local)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		warn(path, msgs)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	overrides := map[string]LocalTarget{}
	for name, node := range local.Platforms {
		data, err := yaml.Marshal(resolveAliases(&node))
		if err != nil {
			return nil, fmt.Errorf("%s: platform %s: %w", path, name, err)
		}
		var l LocalTarget
		var msgs []string
		if _, shared := cfg.Platforms[name]; shared {
			msgs, err = decode(data, &l)
			if err == nil && len(msgs) > 0 {
				err = fmt.Errorf("only project, devices, build and env can be overridden locally: %s", strings.Join(msgs, "; "))
			}
		} else {
			var t TargetConfig
			msgs, err = decode(data, &t)
			// Platform variables from this file retain local precedence.
			l.Env, t.Env = t.Env, nil
			cfg.Platforms[name] = t
		}
		if err != nil {
			return nil, fmt.Errorf("%s: platform %s: %w", path, name, err)
		}
		warn(path+": platform "+name, msgs)
		overrides[name] = l
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
		l := overrides[name]
		if l.Project != nil {
			t.Project = *l.Project
		}
		if l.Devices != nil {
			t.Devices = *l.Devices
		}
		if l.Build != nil {
			// Build overrides patch individual fields, retaining shared defaults.
			if l.Build.System != "" {
				t.Build.System = l.Build.System
			}
			if l.Build.Module != "" {
				t.Build.Module = l.Build.Module
			}
			if l.Build.Variant != "" {
				t.Build.Variant = l.Build.Variant
			}
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
			BuildSystem: t.Build.System, KotlinVariant: t.Build.Variant, WireMockVersion: t.WireMockVersion, WebSocketPath: t.WebSocketPath,
			Variables: maps.Clone(cfg.Env), TargetVariables: maps.Clone(t.Env), LocalVariables: loc, Devices: append([]string(nil), t.Devices...),
			GradleTask: t.Build.Task, APKGlob: t.Build.Artifact, IOSProject: t.Build.Project, IOSScheme: t.Build.Scheme, IOSConfig: t.Build.Configuration,
			ProxyBaseURL: t.ProxyBaseURL, FallbackURL: t.FallbackURL, FBRCURL: t.FBRCURL, OverrideKey: t.OverrideKey, AcceptKey: t.AcceptKey, Env: map[string]string{}}
		// SDK discovery still uses the machine's actual environment, not test variables.
		for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
			p.Env[key] = os.Getenv(key)
		}
		p.Env["MAESTRO_AGENT_AVDS"] = strings.Join(t.Devices, ",")
		p.IOSSimNames = append([]string(nil), t.Devices...)
		if p.BuildSystem == "" {
			p.BuildSystem = "native"
		}
		if p.BuildSystem != "native" && p.BuildSystem != "kotlin-toolchain" {
			return nil, fmt.Errorf("platform %s: build.system must be native or kotlin-toolchain", name)
		}
		if p.BuildSystem == "kotlin-toolchain" {
			module := t.Build.Module
			if module == "" {
				module = name
			}
			p.KotlinAndroidModule, p.KotlinIOSModule = module, module
			if p.KotlinVariant == "" {
				p.KotlinVariant = "debug"
			}
			if p.KotlinVariant != "debug" && p.KotlinVariant != "release" {
				return nil, fmt.Errorf("platform %s: build.variant must be debug or release", name)
			}
		}
		if p.WireMockVersion == "" {
			p.WireMockVersion = "3.13.2"
		}
		if p.WireMockVersion != "3.13.2" && p.WireMockVersion != "4.0.0-beta.40" {
			return nil, fmt.Errorf("unsupported wiremockVersion: %s", p.WireMockVersion)
		}
		if p.WebSocketPath != "" {
			if !strings.HasPrefix(p.WebSocketPath, "/") || strings.ContainsAny(p.WebSocketPath, "?#\\ \t\r\n") {
				return nil, fmt.Errorf("platform %s: websocketPath must be an absolute URL path", name)
			}
			if p.WireMockVersion != "4.0.0-beta.40" {
				return nil, fmt.Errorf("websocketPath requires wiremockVersion=4.0.0-beta.40")
			}
		}
		if name == "ios" {
			p.IOSAppGlob = t.Build.Artifact
		}
		if p.BuildSystem == "native" && p.GradleTask == "" {
			p.GradleTask = "assembleDebug"
		}
		if p.BuildSystem == "native" && p.APKGlob == "" {
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
	suite.Warnings = warnings
	return &suite, nil
}
