package fixtures

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/feernandooff/mockstro/internal/ojson"
)

func message(t *testing.T, body string) *ojson.Object {
	t.Helper()
	v, err := ojson.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*ojson.Object)
}
func TestMessageIdentityAndValidation(t *testing.T) {
	s := newStore(t)
	s.P.WireMockVersion = "4.0.0-beta.40"
	a := `{"name":"a","trigger":{"type":"message","channel":{"type":"websocket","initiatingRequestPattern":{"url":"/ws"}},"message":{"body":{"equalTo":"connect"}}},"actions":[{"type":"send","channelTarget":{"type":"originating"},"message":{"body":{"data":"connected"}}}]}`
	b := strings.ReplaceAll(strings.ReplaceAll(a, `"name":"a"`, `"name":"b"`), `"equalTo":"connect"`, `"equalTo":"chat.send"`)
	write(t, s.StubPath("a"), a)
	write(t, s.StubPath("b"), b)
	if err := s.ValidateLayerDuplicates("profile", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateStub(message(t, a), "a", "a.json"); err != nil {
		t.Fatal(err)
	}
	c := message(t, `{"trigger":{"message":{"body":{"equalTo":"connect"}},"channel":{"initiatingRequestPattern":{"url":"/ws"},"type":"websocket"},"type":"message"}}`)
	if Identity(c) != Identity(message(t, a)) {
		t.Fatal("JSON key order changes identity")
	}
	s.P.WireMockVersion = "3.13.2"
	if err := s.ValidateStub(message(t, a), "a", "a.json"); err == nil {
		t.Fatal("accepted message on v3")
	}
	s.P.WireMockVersion = "4.0.0-beta.40"
	for _, bad := range []string{strings.Replace(a, `"data":"connected"`, `"filePath":"../outside"`, 1), strings.Replace(a, `"data":"connected"`, `"filePath":"missing.json"`, 1), strings.Replace(a, `"actions":[`, `"actions":null,"ignored":[`, 1), strings.Replace(a, `"type":"message"`, `"type":"bogus"`, 1), strings.Replace(a, `"name":"a"`, `"name":"a","request":{}`, 1)} {
		if err := s.ValidateStub(message(t, bad), "a", "a.json"); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	write(t, filepath.Join(s.P.BodiesRoot(), "response.json"), `{"ok":true}`)
	if err := s.ValidateStub(message(t, strings.Replace(a, `"data":"connected"`, `"filePath":"response.json"`, 1)), "a", "a.json"); err != nil {
		t.Fatal(err)
	}
	write(t, s.StubPath("a"), strings.Replace(a, `"name":"a"`, `"name":"a","extends":"b"`, 1))
	if _, err := s.ResolvedStub("a"); err == nil {
		t.Fatal("message extends accepted")
	}
}
