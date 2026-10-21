package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/engine"
)

func TestJSONBodyIsCheckedAgainstContentType(t *testing.T) {
	bad := engine.CustomResponse{Body: "{\n  \"a\": 1,\n  \"b\": \n}", Headers: map[string]string{"content-type": "application/json"}}
	err := checkBodyMatchesContentType(&bad)
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("want a line 4 error, got %v", err)
	}

	plain := engine.CustomResponse{Body: "not json", Headers: map[string]string{"Content-Type": "text/plain"}}
	if err := checkBodyMatchesContentType(&plain); err != nil {
		t.Fatalf("text/plain body should pass: %v", err)
	}

	missing := engine.CustomResponse{Body: `{"ok":true}`}
	if err := checkBodyMatchesContentType(&missing); err != nil || missing.Headers["Content-Type"] != "application/json" {
		t.Fatalf("content type not defaulted: %v %v", err, missing.Headers)
	}

	typo := engine.CustomResponse{Body: `{"ok":`}
	if err := checkBodyMatchesContentType(&typo); err != nil || typo.Headers != nil {
		t.Fatalf("invalid JSON with no declared type is left alone: %v %v", err, typo.Headers)
	}

	empty := engine.CustomResponse{Headers: map[string]string{"Content-Type": "application/json"}}
	if err := checkBodyMatchesContentType(&empty); err != nil {
		t.Fatalf("empty body is allowed: %v", err)
	}
}

func TestEveryMutatingMethodNeedsTheToken(t *testing.T) {
	s, _ := testServer(t)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		rec := do(s, method, "/api/custom", goodHost, map[string]string{"Content-Type": "application/json"}, "{}")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without token: %d, want 403", method, rec.Code)
		}
	}
}

func TestCustomResponseEndpointsWhenNothingIsServing(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}

	if rec := do(s, "POST", "/api/custom", goodHost, h, `{"method":"GET","path":"/a","status":200,"body":"{}"}`); rec.Code != http.StatusConflict {
		t.Errorf("set while idle: %d, want 409", rec.Code)
	}
	if rec := do(s, "POST", "/api/custom", goodHost, h, `{"method":"GET","path":"/a","status":200,"body":"{","headers":{"Content-Type":"application/json"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d, want 400", rec.Code)
	}
	if rec := do(s, "POST", "/api/custom/remove", goodHost, h, `{"id":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("remove while idle: %d, want 400", rec.Code)
	}
	if rec := do(s, "GET", "/api/journal/abc", goodHost, nil, ""); rec.Code != http.StatusConflict {
		t.Errorf("detail while idle: %d, want 409", rec.Code)
	}
	if st := mgr(s).State(); st.Custom == nil || len(st.Custom) != 0 {
		t.Errorf("state.custom should be an empty list, got %#v", st.Custom)
	}
}
