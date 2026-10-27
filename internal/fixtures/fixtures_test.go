package fixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newStore(t *testing.T) *Store {
	root := t.TempDir()
	p := &project.Project{Root: root, MaestroRoot: filepath.Join(root, ".maestro")}
	return New(p)
}

func TestExtendsInheritance(t *testing.T) {
	s := newStore(t)
	write(t, filepath.Join(s.P.BasesRoot(), "root.json"), `{"request":{"url":"/parent"},"priority":9,"response":{"status":200,"jsonBody":{"nested":{"keep":1,"drop":2,"nullable":3},"array":[1,2],"scalar":1}}}`)
	write(t, filepath.Join(s.P.StubsRoot(), "middle.json"), `{"extends":"bases/root","response":{"jsonBody":{"nested":{"drop":"__remove__","nullable":null},"array":[3],"scalar":"new"}}}`)
	write(t, filepath.Join(s.P.StubsRoot(), "leaf.json"), `{"name":"leaf","request":{"url":"/leaf"},"extends":"middle","response":{"jsonBody":{"nested":{"added":4}}}}`)

	got, err := s.ResolvedStub("leaf")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ojson.Marshal(got)
	want := `{"name":"leaf","request":{"url":"/leaf"},"response":{"status":200,"jsonBody":{"nested":{"keep":1,"nullable":null,"added":4},"array":[3],"scalar":"new"}}}`
	if string(b) != want {
		t.Fatalf("resolved:\n got %s\nwant %s", b, want)
	}

	write(t, filepath.Join(s.P.StubsRoot(), "cycle-a.json"), `{"extends":"cycle-b"}`)
	write(t, filepath.Join(s.P.StubsRoot(), "cycle-b.json"), `{"extends":"cycle-a"}`)
	if _, err := s.ResolvedStub("cycle-a"); err == nil || !strings.Contains(err.Error(), "circular extends chain") {
		t.Fatalf("cycle: %v", err)
	}
	write(t, filepath.Join(s.P.StubsRoot(), "dangling.json"), `{"extends":"missing"}`)
	if _, err := s.ResolvedStub("dangling"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("dangling: %v", err)
	}
	chain, _ := s.ExtendsChain("leaf")
	if strings.Join(chain, ">") != "leaf>middle>bases/root" {
		t.Fatalf("chain %v", chain)
	}
}

func TestSanitizedFlowKeepsStepsVerbatim(t *testing.T) {
	s := newStore(t)
	path := filepath.Join(s.P.FlowsRoot(), "a", "x.yaml")
	steps := "- runFlow: ../../shared/launch.yaml\n# comment kept\n- assertVisible: \"€125\"\n"
	write(t, path, "appId: app\ntags: [smoke]\nproperties:\n  apiProfile: smart\n  apiOverrides: []\n---\n"+steps)
	out, err := SanitizedFlow(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "properties") || !strings.HasSuffix(string(out), "---\n"+steps) || !strings.Contains(string(out), "appId: app") {
		t.Fatalf("sanitized:\n%s", out)
	}
}

func TestVerificationRules(t *testing.T) {
	s := newStore(t)
	cases := map[string]bool{
		"- stub: a\n      atLeast: 1":                   true,
		"- stub: a\n      exactly: 0":                   true,
		"- stub: a\n      atLeast: 1\n      atMost: 3":  true,
		"- stub: a\n      exactly: 1\n      atLeast: 1": false,
		"- stub: a\n      atLeast: 3\n      atMost: 1":  false,
		"- stub: a\n      atLeast: 1.5":                 false,
		"- stub: a\n      bogus: 1":                     false,
		"- stub: a":                                     false,
	}
	for body, ok := range cases {
		path := filepath.Join(s.P.FlowsRoot(), "v.yaml")
		write(t, path, "appId: app\nproperties:\n  apiProfile: smart\n  apiVerifications:\n    "+body+"\n---\n- back\n")
		h, err := ParseHeader(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.validateVerifications("v"); (err == nil) != ok {
			t.Errorf("%q: valid=%v, err=%v", body, ok, err)
		}
	}
}

func TestFBRCValuesMustBeStrings(t *testing.T) {
	s := newStore(t)
	path := filepath.Join(s.P.FlowsRoot(), "f.yaml")
	write(t, path, "appId: app\nproperties:\n  apiProfile: smart\n  fbrcOverrides:\n    flag: true\n---\n- back\n")
	h, _ := ParseHeader(path)
	if err := h.validateProperties("f"); err == nil {
		t.Fatal("unquoted boolean accepted")
	}
}

func TestLayerDuplicatesAndMergeByRoute(t *testing.T) {
	s := newStore(t)
	write(t, s.StubPath("a"), `{"name":"a","request":{"method":"GET","url":"/x"},"response":{}}`)
	write(t, s.StubPath("b"), `{"name":"b","request":{"method":"get","url":"/x"},"response":{}}`)
	write(t, s.StubPath("c"), `{"name":"c","request":{"method":"GET","url":"/y"},"response":{}}`)
	if err := s.ValidateLayerDuplicates("l", []string{"a", "b"}); err == nil {
		t.Fatal("duplicate route accepted")
	}
	merged, _ := s.MergeOverridesByRoute([]string{"a", "c", "b"})
	if strings.Join(merged, ",") != "c,b" {
		t.Fatalf("merged %v", merged)
	}
}

func TestBatchFlowHooksAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.yaml")
	write(t, path, `appId: ${APP_ID}
properties:
  apiProfile: default
env:
  MINE: flow
  APP_ID: wrong
onFlowStart:
  - runFlow: own-start.yaml
onFlowComplete:
  - runFlow: own-complete.yaml
---
- launchApp
`)
	out, err := BatchFlow(path, map[string]string{"MINE": "cli", "APP_ID": "com.app", "NEW": "1"}, []string{"APP_ID"},
		BatchHooks{Script: "/h.js", URL: "http://x", Flow: "f/f.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	header, body, _ := splitDocuments(out)
	var h struct {
		Properties     any               `yaml:"properties"`
		Env            map[string]string `yaml:"env"`
		OnFlowStart    []map[string]any  `yaml:"onFlowStart"`
		OnFlowComplete []map[string]any  `yaml:"onFlowComplete"`
	}
	if err := yaml.Unmarshal(header, &h); err != nil {
		t.Fatal(err)
	}
	if h.Properties != nil {
		t.Fatal("mockstro properties must be stripped")
	}
	if h.Env["MINE"] != "flow" || h.Env["APP_ID"] != "com.app" || h.Env["NEW"] != "1" {
		t.Fatalf("env %v", h.Env)
	}
	if _, ok := h.OnFlowStart[0]["runScript"]; !ok || h.OnFlowStart[1]["runFlow"] != "own-start.yaml" {
		t.Fatalf("mockstro setup must run before the flow's hooks: %v", h.OnFlowStart)
	}
	if h.OnFlowComplete[0]["runFlow"] != "own-complete.yaml" || len(h.OnFlowComplete) != 2 {
		t.Fatalf("mockstro checks must run after the flow's hooks: %v", h.OnFlowComplete)
	}
	var steps []any
	if err := yaml.Unmarshal(body, &steps); err != nil || len(steps) != 2 {
		t.Fatalf("steps %v %v", steps, err)
	}
	if !strings.Contains(fmt.Sprint(steps[1]), "MOCKSTRO_ACTION:passed") {
		t.Fatalf("last step must report passed: %v", steps[1])
	}
}

func TestBatchFlowAppendsToAnyStepListStyle(t *testing.T) {
	for _, body := range []string{"  - launchApp\n  - back\n", "[launchApp, back]\n", ""} {
		path := filepath.Join(t.TempDir(), "f.yaml")
		write(t, path, "appId: x\n---\n"+body)
		out, err := BatchFlow(path, nil, nil, BatchHooks{Script: "/h.js", URL: "http://x", Flow: "f"})
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		header, steps, _ := splitDocuments(out)
		var list []any
		if err := yaml.Unmarshal(steps, &list); err != nil {
			t.Fatalf("%q: steps no longer valid YAML: %v\n%s", body, err, steps)
		}
		if want := strings.Count(body, "launchApp") * 2; len(list) != want+1 {
			t.Fatalf("%q: got %d steps", body, len(list))
		}
		if !strings.Contains(string(header), "name: f") {
			t.Fatalf("%q: renamed copy must keep Maestro's default name:\n%s", body, header)
		}
	}
}

func TestMetadataNamespaceAndLegacyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		legacy         bool
	}{
		{"namespaced", "mockstro:\n  apiProfile: smart\n  apiOverrides: [a]\n  fbrcOverrides: {flag: \"true\"}\n  apiVerifications: [{stub: a, exactly: 1}]\n", false},
		{"legacy", "properties:\n  apiProfile: smart\n  apiOverrides: [a]\n  fbrcOverrides: {flag: \"true\"}\n  apiVerifications: [{stub: a, exactly: 1}]\n", true},
		{"flattened", "properties.apiProfile: smart\nproperties.apiOverrides: [a]\nproperties.fbrcOverrides: {flag: \"true\"}\nproperties.apiVerifications: [{stub: a, exactly: 1}]\n", true},
		{"mixed", "properties: {apiProfile: old, apiOverrides: [old]}\nmockstro:\n  apiProfile: smart\n  apiOverrides: [a]\n  fbrcOverrides: {flag: \"true\"}\n  apiVerifications: [{stub: a, exactly: 1}]\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flow.yaml")
			write(t, path, "appId: app\n"+tc.metadata+"---\n- back\n")
			h, err := ParseHeader(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.validateProperties(path); err != nil {
				t.Fatal(err)
			}
			if err := h.validateVerifications(path); err != nil {
				t.Fatal(err)
			}
			if h.Profile != "smart" || len(h.Overrides) != 1 || h.Overrides[0] != "a" || h.FBRC["flag"] != "true" || len(h.Verifications) != 1 || h.LegacyMetadata() != tc.legacy {
				t.Fatalf("header %+v", h)
			}
		})
	}
	for _, metadata := range []string{"mockstro: []", "mockstro: null", "mockstro: value"} {
		path := filepath.Join(t.TempDir(), "invalid.yaml")
		write(t, path, "appId: app\n"+metadata+"\n---\n- back\n")
		if _, err := ParseHeader(path); err == nil {
			t.Fatalf("accepted %s", metadata)
		}
	}
}

func TestSanitizingPreservesReportingAndNamespacedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.yaml")
	steps := "- runScript: ../relative.js\n# keep this\n"
	write(t, path, "appId: app\nproperties:\n  apiProfile: old\n  owner: qa\nproperties.apiOverrides: [old]\nmockstro:\n  apiProfile: new\n  apiOverrides: [new]\n---\n"+steps)
	out, err := SanitizedFlow(path)
	if err != nil {
		t.Fatal(err)
	}
	header, body, _ := splitDocuments(out)
	var got struct {
		Properties map[string]string `yaml:"properties"`
		Mockstro   map[string]any    `yaml:"mockstro"`
	}
	if err := yaml.Unmarshal(header, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Properties) != 1 || got.Properties["owner"] != "qa" || got.Mockstro["apiProfile"] != "new" || string(body) != steps || strings.Contains(string(header), "properties.apiOverrides") {
		t.Fatalf("sanitized flow %s", out)
	}
}
