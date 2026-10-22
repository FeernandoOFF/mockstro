package wiremock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/ojson"
)

func mapping(t *testing.T, raw string) any {
	t.Helper()
	v, err := ojson.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func fakeServer(t *testing.T, h http.HandlerFunc) *Server {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	port, _ := strconv.Atoi(strings.Split(server.URL, ":")[2])
	return &Server{Port: port, RunID: "test-run", MessageSupport: true, http: server.Client()}
}
func TestMessageTrafficDirectionsAndOwnership(t *testing.T) {
	s := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case NoncePath:
			fmt.Fprint(w, "test-run")
		case "/__admin/requests":
			fmt.Fprint(w, `{"requests":[]}`)
		case "/__admin/messages":
			fmt.Fprint(w, `{"messageServeEvents":[{"eventType":"received","timestamp":"2026-10-12T00:00:00Z","wasMatched":false,"channel":{"initiatingRequest":{"url":"/ws"}},"message":{"body":"unknown"}},{"eventType":"sent","timestamp":"2026-10-12T00:00:01Z","wasMatched":true,"message":{"body":"push"}}]}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	traffic, err := s.Traffic(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 2 || traffic[0].Unmatched() || !traffic[1].Unmatched() || traffic[0].Request.Method != "WS out" {
		t.Fatalf("traffic: %+v", traffic)
	}
	s.RunID = "other"
	if err := s.SendMessage("/ws", "no"); err == nil {
		t.Fatal("sent to another run's server")
	}
}
func TestReplaceMessagesResetsSeparateJournals(t *testing.T) {
	var calls []string
	s := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == NoncePath {
			fmt.Fprint(w, "test-run")
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == "POST" {
			var v any
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := s.Replace([]any{NonceMapping("test-run")}, []any{mapping(t, `{"trigger":{"type":"message"},"actions":[]}`)}); err != nil {
		t.Fatal(err)
	}
	want := "DELETE /__admin/mappings,DELETE /__admin/message-mappings,POST /__admin/message-mappings,POST /__admin/mappings/import,DELETE /__admin/requests,DELETE /__admin/messages"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls: %v", calls)
	}
	s.MessageSupport = false
	before := len(calls)
	if _, err := s.Replace(nil, []any{map[string]any{}}); err == nil || len(calls) != before {
		t.Fatal("v3 mutated before message support check")
	}
}

func TestReplaceReportsWhetherFailureClearedPreviousMappings(t *testing.T) {
	for _, tc := range []struct {
		failure string
		reset   bool
	}{
		{"GET " + NoncePath, false},
		{"DELETE /__admin/mappings", false},
		{"DELETE /__admin/message-mappings", true},
		{"POST /__admin/mappings/import", true},
		{"DELETE /__admin/requests", true},
		{"", true},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			s := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method+" "+r.URL.Path == tc.failure {
					http.Error(w, "failed", http.StatusInternalServerError)
					return
				}
				if r.URL.Path == NoncePath {
					fmt.Fprint(w, "test-run")
					return
				}
				fmt.Fprint(w, `{}`)
			})
			reset, err := s.Replace([]any{NonceMapping("test-run")})
			if reset != tc.reset || (err != nil) != (tc.failure != "") {
				t.Fatalf("reset=%v error=%v, want reset=%v", reset, err, tc.reset)
			}
		})
	}
}

// Opt in with the checksum-pinned standalone JAR. No Go WebSocket dependency is needed.
func TestRealWebSocketRoundTripAndReset(t *testing.T) {
	jar := os.Getenv("MOCKSTRO_TEST_WIREMOCK_JAR")
	if jar == "" {
		t.Skip("set MOCKSTRO_TEST_WIREMOCK_JAR to the pinned beta JAR")
	}
	sum, err := fsx.SHA256(jar)
	if err != nil || sum != WebSocketSHA256 {
		t.Fatalf("beta checksum: %s %v", sum, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, execx.Real{}, jar, root, port, "integration", filepath.Join(root, "wiremock.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	messages := []any{mapping(t, `{"name":"connect","trigger":{"type":"message","channel":{"type":"websocket","initiatingRequestPattern":{"url":"/ws"}},"message":{"body":{"matchesJsonPath":{"expression":"$.method","equalTo":"connect"}}}},"actions":[{"type":"send","channelTarget":{"type":"originating"},"message":{"body":{"data":"{\"type\":\"res\",\"id\":\"{{jsonPath message.body '$.id'}}\",\"ok\":true}"}}}]}`)}
	httpMappings := []any{NonceMapping("integration"), mapping(t, `{"name":"upgrade","request":{"method":"GET","url":"/ws"},"response":{"openWebsocketChannel":true}}`)}
	if _, err := s.Replace(httpMappings, messages); err != nil {
		t.Fatal(err)
	}
	client := exec.CommandContext(ctx, "java", "testdata/WebSocketClient.java", fmt.Sprintf("ws://127.0.0.1:%d/ws", port))
	client.Stderr = os.Stderr
	stdout, err := client.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if client.ProcessState == nil {
			client.Process.Kill()
			client.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if line != "CONNECTED" {
			t.Fatalf("client: %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := s.SendMessage("/ws", "pushed-event"); err != nil {
		t.Fatal(err)
	}
	if err := client.Wait(); err != nil {
		t.Fatal(err)
	}
	events, err := s.MessageJournal(true)
	if err != nil {
		t.Fatal(err)
	}
	received, sent := 0, 0
	for _, e := range events {
		if e.EventType == "received" && e.StubName() == "connect" {
			received++
		}
		if e.EventType == "sent" {
			sent++
		}
	}
	if received != 1 || sent != 2 {
		t.Fatalf("received=%d sent=%d", received, sent)
	}
	if _, err := s.Replace(httpMappings); err != nil {
		t.Fatal(err)
	}
	events, err = s.MessageJournal(true)
	if err != nil || len(events) != 0 {
		t.Fatalf("journal leaked: %v %v", events, err)
	}
	data, err := s.do(http.MethodGet, "/__admin/message-mappings", nil)
	if err != nil || strings.Contains(string(data), `"name" : "connect"`) {
		t.Fatalf("mapping leaked: %s %v", data, err)
	}
}
