package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/mockapp"
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

// render prints steps one per line so a test can compare a whole card at once.
func render(steps []Step, indent string) string {
	var b strings.Builder
	for _, s := range steps {
		line := indent + s.Kind + ": " + s.Text
		if s.Platform != "" {
			line += " [" + s.Platform + "]"
		}
		if s.Optional {
			line += " (optional)"
		}
		if s.Note != "" {
			line += " -- " + s.Note
		}
		b.WriteString(line + "\n")
		b.WriteString(render(s.Steps, indent+"  "))
	}
	return b.String()
}

func TestFlowStepsReadTheFlowItself(t *testing.T) {
	root := t.TempDir()
	write(t, root, "shared/launch-logged-in.yaml", "appId: x\n---\n- tapOn: \"should not appear\"\n")
	write(t, root, "shared/assert-home-loaded.yaml", "appId: x\n---\n- assertVisible: \"Home\"\n")
	write(t, root, "shared/close-survey.yaml", "appId: x\n---\n- tapOn:\n    id: \"survey_close\"\n")
	write(t, root, "flows/pay.yaml", `appId: x
name: pay
properties:
  apiProfile: electricity
---
- runFlow: ../shared/launch-logged-in.yaml
- runFlow: ../shared/assert-home-loaded.yaml
# Close the popup to reach the tabs.
- tapOn: "Close dialog"
- assertNotVisible: ".*Payment Required.*"
- scrollUntilVisible:
    element: "Make a payment"
    direction: DOWN
- tapOn:
    id: "payNowButton"
    label: Press Pay now
- inputText: "5000"
- extendedWaitUntil:
    visible: "(?i).*(thank you|thanks).*"
    timeout: 5000
- extendedWaitUntil:
    notVisible: ".*50.*Outstanding.*"
    timeout: 5000
- assertVisible:
    text: "Maybe shown"
    optional: true
- runFlow:
    when:
      platform: Android
    commands:
      - tapOn:
          id: "pay_electricity_now"
- runFlow:
    when:
      platform: iOS
    commands:
      - tapOn: "PAY ELECTRICITY NOW"
- runFlow:
    when:
      visible: "Open Settings"
    commands:
      - tapOn: "Cancel"
- runFlow:
    when:
      true: ${LAUNCH_MODE != "warm"}
    commands:
      - clearState
- runFlow: ../shared/close-survey.yaml
- runFlow: ../shared/relaunch.yaml
- hideKeyboard
- waitForAnimationToEnd
`)
	got := render(FlowSteps(filepath.Join(root, "flows/pay.yaml")), "")
	want := `do: Launch the app (fresh start)
do: Tap “Close dialog” -- Close the popup to reach the tabs.
gone: “Payment Required” should not be shown
do: Scroll to “Make a payment”
do: Press Pay now
do: Type 5000
see: You should see “thank you or thanks”
gone: “50…Outstanding” should go away
see: You should see “Maybe shown” (optional)
do: Tap “Pay electricity now” [android]
do: Tap “PAY ELECTRICITY NOW” [ios]
if: If “Open Settings” is shown
  do: Tap “Cancel”
do: Tap “Survey close”
do: Close and reopen the app
`
	if got != want {
		t.Errorf("steps:\n%s\nwant:\n%s", got, want)
	}
}

func TestFlowStepsToleratesMissingAndStepLessFlows(t *testing.T) {
	root := t.TempDir()
	write(t, root, "flows/header-only.yaml", "appId: x\nname: nothing here\n")
	write(t, root, "flows/loop.yaml", "appId: x\n---\n- runFlow: loop.yaml\n- tapOn: \"Go\"\n")
	if got := FlowSteps(filepath.Join(root, "flows/missing.yaml")); got != nil {
		t.Errorf("missing file: %v", got)
	}
	if got := FlowSteps(filepath.Join(root, "flows/header-only.yaml")); got != nil {
		t.Errorf("header only: %v", got)
	}
	// A flow that runs itself must stop at the depth limit instead of recursing forever.
	if got := render(FlowSteps(filepath.Join(root, "flows/loop.yaml")), ""); !strings.Contains(got, "Tap “Go”") {
		t.Errorf("self-referencing flow: %q", got)
	}
}

func TestChecksForCarryTheBounds(t *testing.T) {
	one, two := 1, 2
	got := ChecksFor([]fixtures.Verification{
		{Stub: "electricity-only/billing-details-paid", AtLeast: &one},
		{Stub: "payments/topup-clears-outstanding", Exactly: &one},
		{Stub: "a/b", AtLeast: &one, AtMost: &two},
	})
	if len(got) != 3 {
		t.Fatalf("checks: %v", got)
	}
	if c := got[0]; c.Label != "Billing details paid" || c.Expect != "at least 1" || c.Min != 1 || c.Max != nil {
		t.Errorf("at least: %+v", c)
	}
	if c := got[1]; c.Expect != "exactly 1" || c.Min != 1 || c.Max == nil || *c.Max != 1 {
		t.Errorf("exactly: %+v", c)
	}
	if c := got[2]; c.Max == nil || *c.Max != 2 || c.Expect != "between 1 and 2" {
		t.Errorf("between: %+v", c)
	}
}

func TestGuideIsReadOncePerServedTarget(t *testing.T) {
	s, p := testServer(t)
	m := mgr(s)
	flow := filepath.Join(p.MaestroRoot, "flows", "survey", "shown.yaml")
	write(t, p.MaestroRoot, "flows/survey/shown.yaml", "appId: com.example\nproperties:\n  apiProfile: member\n  apiVerifications:\n    - stub: ping\n      atLeast: 1\n---\n- tapOn: \"Go\"\n")
	r := &managerRuntime{done: make(chan struct{}), event: func() {}}
	b := &managerBackend{runtime: r, kind: "flow"}
	m.life = mockapp.New(func(context.Context) (mockapp.Backend, error) { return b, nil })
	t.Cleanup(m.Shutdown)
	serve := func(ref string) *Serving {
		t.Helper()
		if err := m.Serve(ref, nil, nil); err != nil {
			t.Fatal(err)
		}
		m.life.Wait()
		return m.State().Serving
	}
	ref := p.Rel(flow)
	got := serve(ref)
	if got == nil || len(got.Steps) != 1 || got.Steps[0].Text != "Tap “Go”" || len(got.Checks) != 1 || got.Checks[0].Stub != "ping" {
		t.Fatalf("guide: %+v", got)
	}

	// Polling keeps the guide stable, even though Target returns a fresh copy each time.
	write(t, p.MaestroRoot, "flows/survey/shown.yaml", "appId: com.example\n---\n- tapOn: \"Stop\"\n")
	if got := m.State().Serving; got.Steps[0].Text != "Tap “Go”" {
		t.Errorf("same target was read again: %v", got.Steps)
	}
	if got := serve(ref); got.Steps[0].Text != "Tap “Stop”" || len(got.Checks) != 0 {
		t.Errorf("a newly served target must be read fresh: %+v", got)
	}
	for _, kind := range []string{"scenario", "profile", "flow"} {
		b.kind = kind
		if got := serve("missing"); len(got.Steps) != 0 || len(got.Checks) != 0 {
			t.Errorf("%s without a flow file has no guide: %+v", kind, got)
		}
	}
}

func TestPreviewDescribesAMockWithoutServingIt(t *testing.T) {
	s, p := testServer(t)
	write(t, p.MaestroRoot, "flows/pay/pay.yaml", `appId: com.example
name: "Pay it"
properties:
  apiProfile: member
  apiOverrides:
    - ping
  fbrcOverrides:
    Config_Survey_Enabled: "false"
  apiVerifications:
    - stub: ping
      exactly: 1
---
# Open the app first.
- tapOn: "Pay now"
- assertVisible: "Thanks"
`)
	flow := filepath.Join(p.MaestroRoot, "flows", "pay", "pay.yaml")

	rec := do(s, "GET", "/api/preview?kind=flow&ref="+flow, goodHost, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("flow preview: %d %s", rec.Code, rec.Body)
	}
	var pv Preview
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}
	if pv.Title != "Pay it" || pv.ProfileGroup != "Membership" || pv.FBRC["Config_Survey_Enabled"] != "false" {
		t.Errorf("title or Remote Config: %+v", pv)
	}
	if got := render(pv.Steps, ""); got != "do: Tap “Pay now” -- Open the app first.\nsee: You should see “Thanks”\n" {
		t.Errorf("steps:\n%s", got)
	}
	if want := (StubInfo{Name: "ping", Group: GroupTest, Method: "GET", Path: "/ping", Status: 200, Editable: true, Checked: true}); len(pv.Stubs) != 1 || pv.Stubs[0] != want {
		t.Errorf("stubs: %+v", pv.Stubs)
	}
	if len(pv.Checks) != 1 || pv.Checks[0].Expect != "exactly 1" {
		t.Errorf("checks: %+v", pv.Checks)
	}
	if st := mgr(s).State(); st.State != StateIdle || st.Serving != nil {
		t.Errorf("previewing must not start anything: %+v", st)
	}

	rec = do(s, "GET", "/api/preview?kind=scenario&ref=member-survey", goodHost, nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "survey.enabled") {
		t.Errorf("scenario preview: %d %s", rec.Code, rec.Body)
	}
	if rec := do(s, "GET", "/api/preview?kind=flow&ref=/etc/passwd", goodHost, nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a ref the catalog does not list: %d, want 404", rec.Code)
	}
}

func TestServeChecksTheRemoteConfigDraftBeforeStarting(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	bad := do(s, "POST", "/api/serve", goodHost, h, `{"kind":"scenario","ref":"member-survey","fbrc":{"not a key":"x"}}`)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "not a valid Remote Config key") {
		t.Errorf("invalid draft: %d %s", bad.Code, bad.Body)
	}
	if rec := do(s, "POST", "/api/serve", goodHost, h, `{"kind":"scenario","ref":"nope","fbrc":{"A":"b"}}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown ref still 404: %d", rec.Code)
	}
	if st := mgr(s).State(); st.State != StateIdle {
		t.Errorf("a refused draft must not start the mock: %v", st.State)
	}
}

func TestPreviewListsEveryStubByPriorityAndMarksReplacedOnes(t *testing.T) {
	s, p := testServer(t)
	m := p.MaestroRoot
	write(t, m, "wiremock/stubs/shared-health.json", `{"name":"shared-health","request":{"method":"GET","urlPath":"/health"},"response":{"status":200,"jsonBody":{"ok":true}}}`)
	write(t, m, "wiremock/stubs/customer-who.json", `{"name":"customer-who","request":{"method":"GET","urlPath":"/who"},"response":{"status":200}}`)
	write(t, m, "wiremock/stubs/any-api.json", `{"name":"any-api","request":{"method":"ANY","urlPathPattern":"^/api/.*$"},"response":{"status":404}}`)
	write(t, m, "wiremock/stubs/down.json", `{"name":"down","request":{"method":"GET","urlPath":"/health"},"response":{"status":503}}`)
	write(t, m, "wiremock/profiles/shared.json", `{"mappings": ["shared-health", "any-api"]}`)
	write(t, m, "wiremock/profiles/member.json", `{"mappings": ["customer-who"]}`)
	write(t, m, "flows/h/down.yaml", "appId: x\nproperties:\n  apiProfile: member\n  apiOverrides:\n    - down\n---\n- tapOn: \"Go\"\n")

	rec := do(s, "GET", "/api/preview?kind=flow&ref="+filepath.Join(m, "flows", "h", "down.yaml"), goodHost, nil, "")
	var pv Preview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pv) != nil {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var got []string
	for _, st := range pv.Stubs {
		line := st.Group + ": " + st.Name
		if st.Replaced {
			line += " (replaced)"
		}
		if !st.Editable {
			line += " (not editable)"
		}
		got = append(got, line)
	}
	want := []string{"test: down", "customer: customer-who", "shared: shared-health (replaced)", "shared: any-api (not editable)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("stubs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStubDetailShowsTheResponseTheAppWouldGet(t *testing.T) {
	s, p := testServer(t)
	m := p.MaestroRoot
	write(t, m, "wiremock/stubs/stateful.json", `{"name":"stateful","scenarioName":"balance","requiredScenarioState":"Started","request":{"method":"GET","urlPath":"/bal"},"response":{"status":200}}`)
	write(t, m, "wiremock/stubs/json-stub.json", `{"name":"json-stub","request":{"method":"GET","urlPath":"/j"},"response":{"status":201,"headers":{"Content-Type":"application/json"},"fixedDelayMilliseconds":250,"jsonBody":{"a":1,"b":[true]}}}`)
	write(t, m, "wiremock/stubs/file-stub.json", `{"name":"file-stub","request":{"method":"GET","urlPath":"/f"},"response":{"status":200,"bodyFileName":"pages/hello.html","transformers":["response-template"]}}`)
	write(t, m, "wiremock/__files/pages/hello.html", "<p>hello {{now}}</p>")
	write(t, m, "wiremock/stubs/escape.json", `{"name":"escape","request":{"method":"GET","urlPath":"/e"},"response":{"status":200,"bodyFileName":"../../../outside.txt"}}`)

	get := func(name string) (StubDetail, int) {
		rec := do(s, "GET", "/api/stub?name="+name, goodHost, nil, "")
		var d StubDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d, rec.Code
	}
	d, code := get("json-stub")
	if code != http.StatusOK || d.Status != 201 || d.DelayMs != 250 || d.Headers["Content-Type"] != "application/json" || !strings.Contains(d.Body, "\n  \"a\": 1") || !d.Editable {
		t.Errorf("json stub: %d %+v", code, d)
	}
	if d, code = get("stateful"); code != http.StatusOK || d.Scenario != "balance" {
		t.Errorf("a stateful stub must say which scenario it belongs to: %d %+v", code, d)
	}
	d, code = get("file-stub")
	if code != http.StatusOK || d.Body != "<p>hello {{now}}</p>" || !d.Template {
		t.Errorf("file stub: %d %+v", code, d)
	}
	if _, code = get("escape"); code == http.StatusOK {
		t.Error("a body file outside __files must not be read")
	}
	if _, code = get("nope"); code != http.StatusNotFound {
		t.Errorf("unknown stub: %d, want 404", code)
	}
	// A real JSON file next to the stubs folder, which a crafted name could otherwise reach.
	write(t, m, "wiremock/outside.json", `{"request":{"method":"GET","urlPath":"/secret"},"response":{"status":200,"jsonBody":{"secret":true}}}`)
	if d, code = get("..%2Foutside"); code != http.StatusNotFound || strings.Contains(d.Body, "secret") {
		t.Errorf("a name that climbs out of the stubs folder: %d %+v, want 404", code, d)
	}
	if _, code = get("..%2F..%2Fetc%2Fpasswd"); code != http.StatusNotFound {
		t.Errorf("a name that is not a stub: %d, want 404", code)
	}
}

func TestServeChecksEditedResponsesBeforeStarting(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	for name, body := range map[string]string{
		"bad method":               `{"kind":"scenario","ref":"member-survey","custom":[{"method":"BREW","path":"/a","status":200}]}`,
		"bad path":                 `{"kind":"scenario","ref":"member-survey","custom":[{"method":"GET","path":"a","status":200}]}`,
		"bad JSON":                 `{"kind":"scenario","ref":"member-survey","custom":[{"method":"GET","path":"/a","status":200,"body":"{","headers":{"Content-Type":"application/json"}}]}`,
		"bad status":               `{"kind":"scenario","ref":"member-survey","custom":[{"method":"GET","path":"/a","status":99}]}`,
		"scenario without a state": `{"kind":"scenario","ref":"member-survey","custom":[{"method":"GET","path":"/a","status":200,"scenario":"balance"}]}`,
	} {
		rec := do(s, "POST", "/api/serve", goodHost, h, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "response for") {
			t.Errorf("%s: %d %s, want 400 naming the response", name, rec.Code, rec.Body)
		}
	}
	if st := mgr(s).State(); st.State != StateIdle {
		t.Errorf("a refused response must not start the mock: %v", st.State)
	}
}

func TestPreviewTellsStatesOfOneCallApartAndNamesWhatMovesThem(t *testing.T) {
	s, p := testServer(t)
	m := p.MaestroRoot
	write(t, m, "wiremock/stubs/bal-before.json", `{"name":"bal-before","scenarioName":"balance","requiredScenarioState":"Started","request":{"method":"GET","urlPath":"/bal"},"response":{"status":200,"jsonBody":{"overdue":50}}}`)
	write(t, m, "wiremock/stubs/bal-after.json", `{"name":"bal-after","scenarioName":"balance","requiredScenarioState":"paid","request":{"method":"GET","urlPath":"/bal"},"response":{"status":200,"jsonBody":{"overdue":0}}}`)
	write(t, m, "wiremock/stubs/pay.json", `{"name":"pay","scenarioName":"balance","newScenarioState":"paid","request":{"method":"POST","urlPath":"/pay"},"response":{"status":200}}`)
	write(t, m, "flows/b/pay.yaml", "appId: x\nproperties:\n  apiProfile: member\n  apiOverrides:\n    - bal-before\n    - bal-after\n    - pay\n---\n- tapOn: \"Go\"\n")

	rec := do(s, "GET", "/api/preview?kind=flow&ref="+filepath.Join(m, "flows", "b", "pay.yaml"), goodHost, nil, "")
	var pv Preview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pv) != nil {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	by := map[string]StubInfo{}
	for _, st := range pv.Stubs {
		by[st.Name] = st
	}
	if b := by["bal-before"]; b.Scenario != "balance" || b.State != "Started" || b.After != "" {
		t.Errorf("the first state: %+v", b)
	}
	if a := by["bal-after"]; a.State != "paid" || a.After != "POST /pay" {
		t.Errorf("the later state names what gets it there: %+v", a)
	}
	if pay := by["pay"]; pay.Next != "paid" {
		t.Errorf("the call that moves the scenario on: %+v", pay)
	}
}
