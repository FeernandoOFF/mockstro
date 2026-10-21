package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

func TestKnownKeysListsEveryKeyWithItsValues(t *testing.T) {
	keys := KnownKeys([]CatalogItem{
		{FBRC: map[string]string{"Config_Survey_Enabled": "true", "Config_Survey_Probability": "100"}},
		{FBRC: map[string]string{"Config_Survey_Enabled": "false", "Config_Survey_Probability": "100"}},
		{},
	})
	if len(keys) != 2 || keys[0].Key != "Config_Survey_Enabled" || !slices.Equal(keys[0].Values, []string{"false", "true"}) ||
		keys[1].Key != "Config_Survey_Probability" || !slices.Equal(keys[1].Values, []string{"100"}) {
		t.Fatalf("keys = %+v", keys)
	}
	if got := KnownKeys(nil); got == nil || len(got) != 0 {
		t.Fatalf("no items should give an empty list, got %#v", got)
	}
}

func TestFBRCKeysEndpointUsesTheProjectsScenarios(t *testing.T) {
	s, _ := testServer(t)
	rec := do(s, "GET", "/api/fbrc/keys", goodHost, nil, "")
	var out struct{ Keys []KnownKey }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body)
	}
	if len(out.Keys) != 1 || out.Keys[0].Key != "Config_Survey_Enabled" || out.Keys[0].Values[0] != "true" {
		t.Fatalf("keys = %+v", out.Keys)
	}
}

func TestFBRCEndpointsWhenNothingIsServing(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	if rec := do(s, "POST", "/api/fbrc", goodHost, h, `{"values":{"Config_A_B":"1"}}`); rec.Code != http.StatusConflict {
		t.Errorf("set while idle: %d, want 409", rec.Code)
	}
	if rec := do(s, "POST", "/api/fbrc/reset", goodHost, h, `{}`); rec.Code != http.StatusConflict {
		t.Errorf("reset while idle: %d, want 409", rec.Code)
	}
	if rec := do(s, "POST", "/api/fbrc", goodHost, nil, `{"values":{}}`); rec.Code != http.StatusForbidden {
		t.Errorf("no token: %d, want 403", rec.Code)
	}
	if rec := do(s, "POST", "/api/fbrc", goodHost, h, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d, want 400", rec.Code)
	}
}
