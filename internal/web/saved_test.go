package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveListDeleteSavedResponses(t *testing.T) {
	s, p := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}

	rec := do(s, "POST", "/api/saved", goodHost, h, `{"title":"Usage 500","method":"GET","path":"/api/usage","status":500,"body":"{\"error\":\"boom\"}","delayMs":100}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	var saved struct{ Name string }
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.Name != "local/usage-500" {
		t.Fatalf("name = %q", saved.Name)
	}
	file := filepath.Join(p.StubsRoot(), "local", "usage-500.json")
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), `"jsonBody"`) || !strings.Contains(string(raw), `"Content-Type": "application/json"`) {
		t.Fatalf("stub file: %v\n%s", err, raw)
	}

	var list struct {
		Items []struct {
			Name, Title, Method, Path string
			Status                    int
		}
		Dir string
	}
	rec = do(s, "GET", "/api/saved", goodHost, nil, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Title != "Usage 500" || list.Items[0].Status != 500 || !strings.HasSuffix(list.Dir, "stubs/local") {
		t.Fatalf("list = %+v", list)
	}

	if rec := do(s, "POST", "/api/saved/delete", goodHost, h, `{"name":"local/usage-500"}`); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
}

func TestSaveRejectsInvalidInputBeforeWritingAnything(t *testing.T) {
	s, p := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	for name, body := range map[string]string{
		"no title":    `{"title":"","method":"GET","path":"/a","status":200}`,
		"bad status":  `{"title":"x","method":"GET","path":"/a","status":42}`,
		"bad method":  `{"title":"x","method":"TRACE","path":"/a","status":200}`,
		"query":       `{"title":"x","method":"GET","path":"/a?b=1","status":200}`,
		"broken json": `{"title":"x","method":"GET","path":"/a","status":200,"body":"{","headers":{"Content-Type":"application/json"}}`,
	} {
		if rec := do(s, "POST", "/api/saved", goodHost, h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(p.StubsRoot(), "local")); len(entries) != 0 {
		t.Fatalf("files written for invalid input: %v", entries)
	}
}

func TestApplyAndDeleteRefuseNamesOutsideLocal(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	for _, name := range []string{"account/real", "local/../x", "../../etc/passwd", "local/missing"} {
		body := `{"name":"` + name + `"}`
		if rec := do(s, "POST", "/api/saved/delete", goodHost, h, body); rec.Code != http.StatusNotFound {
			t.Errorf("delete %q: %d, want 404", name, rec.Code)
		}
		if rec := do(s, "POST", "/api/saved/apply", goodHost, h, body); rec.Code != http.StatusNotFound {
			t.Errorf("apply %q: %d, want 404", name, rec.Code)
		}
	}
}

func TestApplyWhileNothingIsServingExplainsWhy(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	do(s, "POST", "/api/saved", goodHost, h, `{"title":"One","method":"GET","path":"/a","status":200}`)
	rec := do(s, "POST", "/api/saved/apply", goodHost, h, `{"name":"local/one"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "start one first") {
		t.Fatalf("apply while idle: %d %s", rec.Code, rec.Body)
	}
}
