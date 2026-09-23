package fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/trun/internal/ojson"
	"github.com/feernandooff/trun/internal/project"
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
