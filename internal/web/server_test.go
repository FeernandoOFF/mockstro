package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

const testPort = 18090

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testServer(t *testing.T) (*Server, *project.Project) {
	t.Helper()
	t.Setenv("MOCKSTRO_STATE_DIR", t.TempDir())
	root := t.TempDir()
	m := filepath.Join(root, ".maestro")
	write(t, m, "wiremock/stubs/ping.json", `{"name":"ping","request":{"method":"GET","urlPath":"/ping"},"response":{"status":200}}`)
	write(t, m, "wiremock/__files/ping.json", `{}`)
	write(t, m, "wiremock/profiles/shared.json", `{"mappings": []}`)
	write(t, m, "wiremock/profiles/member.json", `{"mappings": []}`)
	write(t, m, "scenarios/member-survey.yaml", "name: \"Member with survey\"\ndescription: \"Survey at 100%.\"\nproperties:\n  apiProfile: member\n  fbrcOverrides:\n    survey.enabled: \"true\"\n")
	write(t, m, "flows/survey/shown.yaml", "appId: com.example\nname: \"Survey shown\"\ntags:\n  - regression\n  - survey\nproperties:\n  apiProfile: member\n---\n- launchApp\n")
	p := &project.Project{
		Root: root, MaestroRoot: m, Env: map[string]string{}, Platform: "ios", AppID: "com.example", Integration: "lite",
		ProxyBaseURL: "https://api.example.test", FBRCURL: project.DefaultFBRCURL,
		OverrideKey: project.DefaultOverrideKy, AcceptKey: project.DefaultAcceptKey,
		Catalog: project.CatalogConfig{ProfileGroups: map[string]string{"member": "Membership"}, FeatureTags: map[string]string{"survey.enabled": "feedback"}},
	}
	o := engine.DefaultOptions()
	ws := NewWorkspace(nil, "", &execx.Fake{})
	ws.Use(p, o)
	s, err := NewServer(ws, testPort)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

// mgr is the open project's manager.
func mgr(s *Server) *Manager {
	_, m := s.ws.Current()
	return m
}

func do(s *Server, method, path, host string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const goodHost = "127.0.0.1:18090"

func TestRejectsForeignHost(t *testing.T) {
	s, _ := testServer(t)
	for _, host := range []string{"evil.example:18090", "127.0.0.1:9999", "127.0.0.1.evil.example:18090", ""} {
		if rec := do(s, "GET", "/api/state", host, nil, ""); rec.Code != http.StatusForbidden {
			t.Errorf("host %q: status %d, want 403", host, rec.Code)
		}
	}
	if rec := do(s, "GET", "/api/state", "localhost:18090", nil, ""); rec.Code != http.StatusOK {
		t.Errorf("localhost: status %d", rec.Code)
	}
}

func TestPostNeedsTokenAndJSON(t *testing.T) {
	s, _ := testServer(t)
	json := map[string]string{"Content-Type": "application/json"}
	rec := do(s, "POST", "/api/stop", goodHost, json, "{}")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"reload":true`) || !strings.Contains(rec.Body.String(), "restarted") {
		t.Errorf("no token: %d %s, want 403 telling the page to reload", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Mockstro-Instance") == "" {
		t.Error("the refusal must carry the instance, so the page can tell a restart from a wrong token")
	}
	if rec := do(s, "POST", "/api/stop", goodHost, map[string]string{tokenHeader: "wrong", "Content-Type": "application/json"}, "{}"); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: %d, want 403", rec.Code)
	}
	if rec := do(s, "POST", "/api/stop", goodHost, map[string]string{tokenHeader: s.token, "Content-Type": "text/plain"}, "{}"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d, want 415", rec.Code)
	}
	if rec := do(s, "POST", "/api/stop", goodHost, map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}, "{}"); rec.Code != http.StatusAccepted {
		t.Errorf("valid stop while idle: %d, want 202", rec.Code)
	}
}

func TestIndexEmbedsToken(t *testing.T) {
	s, _ := testServer(t)
	rec := do(s, "GET", "/", goodHost, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), s.token) || strings.Contains(rec.Body.String(), "__TOKEN__") {
		t.Fatalf("index: %d, token embedded: %v", rec.Code, strings.Contains(rec.Body.String(), s.token))
	}
}

func TestCatalogListsScenariosProfilesAndFlows(t *testing.T) {
	s, _ := testServer(t)
	rec := do(s, "GET", "/api/catalog", goodHost, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Items []CatalogItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]CatalogItem{}
	for _, it := range out.Items {
		byKind[it.Kind] = append(byKind[it.Kind], it)
	}
	if len(byKind["scenario"]) != 1 || byKind["scenario"][0].Title != "Member with survey" || byKind["scenario"][0].FBRC["survey.enabled"] != "true" {
		t.Errorf("scenarios = %+v", byKind["scenario"])
	}
	if len(byKind["profile"]) != 1 || byKind["profile"][0].Ref != "member" {
		t.Errorf("profiles (shared must be hidden) = %+v", byKind["profile"])
	}
	if sc := byKind["scenario"][0]; sc.ProfileGroup != "Membership" || len(sc.Tags) != 1 || sc.Tags[0] != "feedback" {
		t.Errorf("scenario group/tags = %q %v, want Membership [feedback]", sc.ProfileGroup, sc.Tags)
	}
	if byKind["profile"][0].ProfileGroup != "Membership" || byKind["flow"][0].ProfileGroup != "Membership" {
		t.Errorf("profile groups: profile %q flow %q", byKind["profile"][0].ProfileGroup, byKind["flow"][0].ProfileGroup)
	}
	if len(byKind["flow"]) != 1 || byKind["flow"][0].Group != "survey" || byKind["flow"][0].Tags[0] != "regression" {
		t.Errorf("flows = %+v", byKind["flow"])
	}
}

func TestServeRefusesRefsOutsideTheCatalog(t *testing.T) {
	s, p := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	for _, body := range []string{
		`{"kind":"flow","ref":"/etc/passwd.yaml"}`,
		`{"kind":"flow","ref":"` + filepath.Join(p.Root, "..", "other.yaml") + `"}`,
		`{"kind":"scenario","ref":"missing"}`,
		`{"kind":"profile","ref":"shared"}`,
	} {
		if rec := do(s, "POST", "/api/serve", goodHost, h, body); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", body, rec.Code)
		}
	}
	if mgr(s).State().State != StateIdle {
		t.Fatalf("state = %s, want idle", mgr(s).State().State)
	}
}

func TestProxyRulesForLiteProject(t *testing.T) {
	s, _ := testServer(t)
	var v ProxyView
	rec := do(s, "GET", "/api/proxy", goodHost, nil, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Needed || len(v.Rules) != 2 || v.Rules[0].Match != "https://api.example.test/*" || v.Rules[0].MapTo != "http://127.0.0.1:18080" ||
		v.Rules[1].Match != project.DefaultFBRCURL+"/*" {
		t.Fatalf("proxy = %+v", v)
	}
}

func TestJournalIsEmptyWhenIdle(t *testing.T) {
	s, _ := testServer(t)
	rec := do(s, "GET", "/api/journal", goodHost, nil, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("journal: %d %q", rec.Code, rec.Body)
	}
}

func TestEveryResponseNamesTheLaunch(t *testing.T) {
	s, _ := testServer(t)
	a := do(s, "GET", "/api/ping", goodHost, nil, "").Header().Get("X-Mockstro-Instance")
	b := do(s, "GET", "/api/state", goodHost, nil, "").Header().Get("X-Mockstro-Instance")
	if a == "" || a != b {
		t.Fatalf("instance = %q then %q; it must be present and stable within a launch", a, b)
	}
	other, _ := testServer(t)
	if c := do(other, "GET", "/api/ping", goodHost, nil, "").Header().Get("X-Mockstro-Instance"); c == a {
		t.Fatalf("a second launch reused instance %q", c)
	}
}
