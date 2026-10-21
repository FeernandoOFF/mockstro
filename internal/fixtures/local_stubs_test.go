package fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
)

func localStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	return New(&project.Project{Root: root, MaestroRoot: filepath.Join(root, ".maestro")})
}

func TestSaveLocalStubWritesAReadableStubTheValidatorAccepts(t *testing.T) {
	s := localStore(t)
	saved, err := s.SaveLocalStub(LocalStub{Title: "Splash: maintenance 503!", Method: "GET", Path: "/api/splash/iOS/4.5", Status: 503,
		Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"maintenance":true,"big":12345678901234567890}`, DelayMs: 300})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "local/splash-maintenance-503" {
		t.Fatalf("name = %q", saved.Name)
	}

	raw, err := os.ReadFile(s.StubPath(saved.Name))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"jsonBody"`, `"urlPath": "/api/splash/iOS/4.5"`, `"fixedDelayMilliseconds": 300`, `12345678901234567890`, `"source": "ui"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stub file missing %s:\n%s", want, raw)
		}
	}

	resolved, err := s.ResolvedStub(saved.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateStub(resolved, saved.Name, s.StubPath(saved.Name)); err != nil {
		t.Fatalf("saved stub fails validation: %v", err)
	}
	if _, err := ojson.ParseObjectFile(s.StubPath(saved.Name)); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLocalStubNeverOverwrites(t *testing.T) {
	s := localStore(t)
	in := LocalStub{Title: "Same name", Method: "GET", Path: "/a", Status: 200, Body: `{"v":1}`}
	first, err := s.SaveLocalStub(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Body = `{"v":2}`
	second, err := s.SaveLocalStub(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "local/same-name" || second.Name != "local/same-name-2" {
		t.Fatalf("names = %q, %q", first.Name, second.Name)
	}
	got, err := s.LocalStub(first.Name)
	if err != nil || !strings.Contains(got.Body, `"v": 1`) {
		t.Fatalf("first stub was changed: %+v %v", got, err)
	}
}

func TestLocalStubsRoundTripAndTextBodies(t *testing.T) {
	s := localStore(t)
	if got, err := s.LocalStubs(); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty store: %#v %v", got, err)
	}
	if _, err := s.SaveLocalStub(LocalStub{Title: "Plain text", Method: "POST", Path: "/p", Status: 418,
		Headers: map[string]string{"Content-Type": "text/plain"}, Body: `{"looks":"like json"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveLocalStub(LocalStub{Title: "Empty", Method: "DELETE", Path: "/e", Status: 204}); err != nil {
		t.Fatal(err)
	}
	all, err := s.LocalStubs()
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %+v %v", all, err)
	}
	byName := map[string]LocalStub{}
	for _, st := range all {
		byName[st.Name] = st
	}
	if p := byName["local/plain-text"]; p.Body != `{"looks":"like json"}` || p.Title != "Plain text" || p.Status != 418 || p.Headers["Content-Type"] != "text/plain" {
		t.Errorf("plain = %+v", p)
	}
	if e := byName["local/empty"]; e.Body != "" || e.Method != "DELETE" || e.Status != 204 {
		t.Errorf("empty = %+v", e)
	}
	raw, _ := os.ReadFile(s.StubPath("local/plain-text"))
	if strings.Contains(string(raw), "jsonBody") {
		t.Errorf("text/plain body must stay a string:\n%s", raw)
	}
}

func TestSaveLocalStubRejectsBadTitlesAndLeavesNothingBehind(t *testing.T) {
	s := localStore(t)
	for _, title := range []string{"", "   ", "!!!", strings.Repeat("a", 81)} {
		if _, err := s.SaveLocalStub(LocalStub{Title: title, Method: "GET", Path: "/a", Status: 200}); err == nil {
			t.Errorf("title %q should be rejected", title)
		}
	}
	// A stub the validator rejects (no method) is removed again.
	if _, err := s.SaveLocalStub(LocalStub{Title: "No method", Path: "/a", Status: 200}); err == nil {
		t.Error("stub without a method should fail validation")
	}
	if entries, _ := os.ReadDir(s.localRoot()); len(entries) != 0 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestDeleteLocalStubOnlyTouchesLocalNames(t *testing.T) {
	s := localStore(t)
	other := s.StubPath("account/real")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveLocalStub(LocalStub{Title: "Mine", Method: "GET", Path: "/m", Status: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"account/real", "local/../account/real", "../x", "local/UPPER", "local/", "local/a/b"} {
		if err := s.DeleteLocalStub(bad); err == nil {
			t.Errorf("delete %q should be refused", bad)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a team stub was touched: %v", err)
	}
	if err := s.DeleteLocalStub(saved.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLocalStub(saved.Name); err == nil {
		t.Error("second delete should report it is gone")
	}
}
