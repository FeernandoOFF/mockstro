package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/feernandooff/mockstro/internal/wiremock"
)

type fakeWireMock struct {
	mu       sync.Mutex
	next     int
	puts     int
	mappings map[string]map[string]any
}

func newFakeSession(t *testing.T) (*MockSession, *fakeWireMock) {
	t.Helper()
	f := &fakeWireMock{mappings: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == wiremock.NoncePath:
			_, _ = io.WriteString(w, "run1")
		case r.Method == http.MethodPost && r.URL.Path == "/__admin/mappings":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.next++
			id := fmt.Sprintf("id-%d", f.next)
			f.mappings[id] = m
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/__admin/mappings/"):
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.mappings[strings.TrimPrefix(r.URL.Path, "/__admin/mappings/")] = m
			f.puts++
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/__admin/mappings/"):
			delete(f.mappings, strings.TrimPrefix(r.URL.Path, "/__admin/mappings/"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return &MockSession{Run: &Run{Port: port, WM: wiremock.Connect(port, "run1")}, target: &MockTarget{Name: "t"}}, f
}

func TestCustomResponseValidate(t *testing.T) {
	ok := CustomResponse{Method: "GET", Path: "/api/splash/iOS/4.5", Status: 200, Body: "{}"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	cases := map[string]func(c *CustomResponse){
		"method":   func(c *CustomResponse) { c.Method = "TRACE" },
		"path":     func(c *CustomResponse) { c.Path = "api/x" },
		"query":    func(c *CustomResponse) { c.Path = "/api/x?a=1" },
		"status":   func(c *CustomResponse) { c.Status = 99 },
		"delay":    func(c *CustomResponse) { c.DelayMs = 61_000 },
		"header":   func(c *CustomResponse) { c.Headers = map[string]string{"Bad Name": "x"} },
		"newline":  func(c *CustomResponse) { c.Headers = map[string]string{"X-A": "a\r\nSet-Cookie: b"} },
		"toolarge": func(c *CustomResponse) { c.Body = strings.Repeat("a", maxCustomBody+1) },
	}
	for name, mutate := range cases {
		c := ok
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSetCustomResponseSendsPriorityOneMappingAndReplacesSameRoute(t *testing.T) {
	s, f := newFakeSession(t)

	first, err := s.SetCustomResponse(CustomResponse{Method: "GET", Path: "/api/usage", Status: 500, Body: `{"error":"x"}`, DelayMs: 250,
		Headers: map[string]string{"Content-Type": "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	m := f.mappings[first.ID]
	req := m["request"].(map[string]any)
	resp := m["response"].(map[string]any)
	if m["priority"] != float64(1) || req["method"] != "GET" || req["urlPath"] != "/api/usage" || resp["status"] != float64(500) ||
		resp["fixedDelayMilliseconds"] != float64(250) || resp["body"] != `{"error":"x"}` {
		t.Fatalf("mapping = %v", m)
	}

	second, err := s.SetCustomResponse(CustomResponse{Method: "GET", Path: "/api/usage", Status: 200, Body: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := f.mappings[first.ID]; stale || len(f.mappings) != 1 {
		t.Fatalf("same route should replace the old mapping: %v", f.mappings)
	}
	if got := s.CustomResponses(); len(got) != 1 || got[0].ID != second.ID || got[0].Status != 200 {
		t.Fatalf("custom = %+v", got)
	}

	if err := s.RemoveCustomResponse(second.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.mappings) != 0 || len(s.CustomResponses()) != 0 {
		t.Fatalf("remove left %v / %v", f.mappings, s.CustomResponses())
	}
	if err := s.RemoveCustomResponse("nope"); err == nil {
		t.Fatal("removing an unknown id should fail")
	}
}

func TestCustomResponsesIsNeverNil(t *testing.T) {
	s, _ := newFakeSession(t)
	if got := s.CustomResponses(); got == nil || len(got) != 0 {
		t.Fatalf("want an empty non-nil list, got %#v", got)
	}
}

func TestInvalidCustomResponseNeverReachesWireMock(t *testing.T) {
	s, f := newFakeSession(t)
	if _, err := s.SetCustomResponse(CustomResponse{Method: "GET", Path: "/a", Status: 42}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(f.mappings) != 0 {
		t.Fatalf("wiremock got %v", f.mappings)
	}
}
