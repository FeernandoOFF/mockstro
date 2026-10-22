package mockruntime

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

func entriesOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	resp, _ := m["response"].(map[string]any)
	body, _ := resp["jsonBody"].(map[string]any)
	entries, _ := body["entries"].(map[string]any)
	return entries
}

func fbrcSession(t *testing.T) (*Session, *fakeWireMock) {
	s, f := newFakeSession(t)
	s.target = &Target{Kind: "scenario", Name: "survey-on", FBRC: map[string]string{"Config_Survey_Enabled": "true", "Config_Survey_Probability": "100"}}
	s.fbrc = maps.Clone(s.target.FBRC)
	return s, f
}

func TestSetFBRCReplacesOnlyTheRemoteConfigMapping(t *testing.T) {
	s, f := fbrcSession(t)
	// A custom response the tester made must survive an edit to Remote Config.
	custom, err := s.SetCustomResponse(CustomResponse{Method: "GET", Path: "/api/usage", Status: 500, Body: "{}"})
	if err != nil {
		t.Fatal(err)
	}

	err = s.SetFBRC(context.Background(), map[string]string{"Config_Survey_Enabled": "false", "Config_New_Key": "x"})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := f.mappings[wiremock.FBRCFetchID]
	if !ok || f.puts != 1 {
		t.Fatalf("expected one in-place replace of %s, got puts=%d mappings=%v", wiremock.FBRCFetchID, f.puts, f.mappings)
	}
	got := entriesOf(t, m)
	if len(got) != 2 || got["Config_Survey_Enabled"] != "false" || got["Config_New_Key"] != "x" {
		t.Fatalf("entries = %v; a key left out must be absent, not kept", got)
	}
	if _, kept := f.mappings[custom.ID]; !kept || len(s.CustomResponses()) != 1 {
		t.Fatal("editing Remote Config dropped the custom response")
	}
	if cur := s.FBRC(); len(cur) != 2 || cur["Config_Survey_Enabled"] != "false" {
		t.Fatalf("session values = %v", cur)
	}
	if def := s.Snapshot().Defaults; len(def) != 2 || def["Config_Survey_Enabled"] != "true" {
		t.Fatalf("the scenario's own defaults must stay visible, got %v", def)
	}
}

func TestResetFBRCRestoresTheTargetsValues(t *testing.T) {
	s, f := fbrcSession(t)
	if err := s.SetFBRC(context.Background(), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if len(s.FBRC()) != 0 {
		t.Fatalf("emptying should leave no values, got %v", s.FBRC())
	}
	if err := s.ResetFBRC(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := entriesOf(t, f.mappings[wiremock.FBRCFetchID])
	if len(got) != 2 || got["Config_Survey_Probability"] != "100" {
		t.Fatalf("after reset entries = %v", got)
	}
}

func TestInvalidFBRCNeverReachesWireMock(t *testing.T) {
	s, f := fbrcSession(t)
	for name, values := range map[string]map[string]string{
		"empty key":     {"": "x"},
		"space in key":  {"Bad Key": "x"},
		"slash in key":  {"a/b": "x"},
		"huge value":    {"K": strings.Repeat("a", remoteconfig.MaxValue+1)},
		"too many keys": manyKeys(remoteconfig.MaxKeys + 1),
	} {
		if err := s.SetFBRC(context.Background(), values); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if f.puts != 0 || len(s.FBRC()) != 2 {
		t.Fatalf("invalid input changed state: puts=%d values=%v", f.puts, s.FBRC())
	}
}

func manyKeys(n int) map[string]string {
	m := make(map[string]string, n)
	for i := range n {
		m[fmt.Sprintf("Config_K_%d", i)] = "x"
	}
	return m
}

func TestFBRCFetchMappingHasAStableIDAndETag(t *testing.T) {
	a := wiremock.FBRCFetchMapping(map[string]string{"A": "1"})
	b := wiremock.FBRCFetchMapping(map[string]string{"A": "2"})
	if a["id"] != wiremock.FBRCFetchID || b["id"] != wiremock.FBRCFetchID {
		t.Fatalf("ids = %v %v", a["id"], b["id"])
	}
	etag := func(m map[string]any) string {
		return m["response"].(map[string]any)["headers"].(map[string]string)["ETag"]
	}
	if etag(a) == etag(b) {
		t.Fatal("a changed value must change the ETag so the app refetches")
	}
	for _, m := range wiremock.FBRCMappings(map[string]string{"A": "1"}) {
		if mm := m.(map[string]any); mm["name"] == "firebase-remote-config/fetch" && mm["id"] != wiremock.FBRCFetchID {
			t.Fatal("the bulk-imported mapping must carry the same id")
		}
	}
}

func TestWireMockDeliveryFailureKeepsEffectiveValues(t *testing.T) {
	s, f := fbrcSession(t)
	f.mu.Lock()
	f.failPut = true
	f.mu.Unlock()
	if err := s.SetFBRC(context.Background(), map[string]string{"K": "new"}); err == nil {
		t.Fatal("failed HTTP update accepted")
	}
	if s.FBRC()["Config_Survey_Enabled"] != "true" {
		t.Fatal("failed HTTP delivery published values")
	}
	if err := s.ResetFBRC(context.Background()); err == nil {
		t.Fatal("failed HTTP reset accepted")
	}
}
