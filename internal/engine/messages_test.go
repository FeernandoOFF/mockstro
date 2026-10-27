package engine

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestRealWebSocketScenarioLifecycleAndVerification(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(fmt.Sprintf("attached=%v", attached), func(t *testing.T) {
			testRealWebSocketScenarioLifecycle(t, attached)
		})
	}
}

func testRealWebSocketScenarioLifecycle(t *testing.T, attached bool) {
	jar := os.Getenv("MOCKSTRO_TEST_WIREMOCK_JAR")
	if jar == "" {
		t.Skip("set MOCKSTRO_TEST_WIREMOCK_JAR to the pinned beta JAR")
	}
	var calls []string
	e, fake := testEngine(t, "agent", func(c execx.Cmd) (string, error) { calls = append(calls, c.String()); return "", nil })
	fake.StartHandler = (execx.Real{}).Start
	e.jar = jar
	e.P.WebSocketPath = "/ws"
	for _, dir := range []string{e.P.ProfilesRoot(), e.P.StubsRoot(), e.P.BodiesRoot()} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(e.P.ProfilesRoot(), "shared.json"): `{"mappings":["upgrade","connect"]}`,
		e.Store.StubPath("upgrade"):                      `{"name":"upgrade","request":{"method":"GET","url":"/ws"},"response":{"openWebsocketChannel":true}}`,
		e.Store.StubPath("connect"):                      `{"name":"connect","trigger":{"type":"message","channel":{"type":"websocket","initiatingRequestPattern":{"url":"/ws"}},"message":{"body":{"matchesJsonPath":{"expression":"$.method","equalTo":"connect"}}}},"actions":[{"type":"send","channelTarget":{"type":"originating"},"message":{"body":{"data":"{\"id\":\"{{jsonPath message.body '$.id'}}\"}"}}}]}`,
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := e.newRun("run", 0)
	r.ID, r.Port, r.preflightDone = "test-run", port, true
	if attached {
		r.Device = "emulator-test"
	}
	if err := r.initDir(); err != nil {
		t.Fatal(err)
	}
	if err := r.startWiremock(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r.WM != nil {
			r.WM.Stop()
		}
	}()
	if err := r.Activate(ctx, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !r.websocketActive || attached && !strings.Contains(strings.Join(calls, "\n"), "am force-stop") {
		t.Fatalf("calls: %v", calls)
	}
	client := exec.CommandContext(ctx, "java", "../wiremock/testdata/WebSocketClient.java", fmt.Sprintf("ws://127.0.0.1:%d/ws", port), "hold")
	stdout, err := client.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.Stderr = os.Stderr
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if client.ProcessState == nil {
			client.Process.Kill()
			client.Wait()
		}
	}()
	ready := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			ready <- scanner.Text()
		}
		close(ready)
	}()
	select {
	case line := <-ready:
		if line != "CONNECTED" {
			t.Fatalf("client: %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	one := 1
	results, err := r.Verify([]fixtures.Verification{{Stub: "connect", Exactly: &one}})
	if err != nil || len(results) != 1 || !results[0].Pass || results[0].Got != 1 {
		t.Fatalf("results: %+v %v", results, err)
	}
	old := r.WM
	if err := r.Activate(ctx, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if r.WM != old {
		t.Fatal("scenario switch restarted the JVM")
	}
	select {
	case <-old.Done():
		t.Fatal("server exited during scenario switch")
	default:
	}
	select {
	case line := <-ready:
		if line != "CLOSED 1000" {
			t.Fatalf("stale client: %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.Wait(); err != nil {
		t.Fatal(err)
	}
	events, err := r.WM.MessageJournal(true)
	if err != nil || len(events) != 0 {
		t.Fatalf("journal leaked: %+v %v", events, err)
	}
	// Reconnect to the same process and prove only the new scenario's messages count.
	reconnected := exec.CommandContext(ctx, "java", "../wiremock/testdata/WebSocketClient.java", fmt.Sprintf("ws://127.0.0.1:%d/ws", port))
	out, err := reconnected.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	reconnected.Stderr = os.Stderr
	if err := reconnected.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if reconnected.ProcessState == nil {
			reconnected.Process.Kill()
			reconnected.Wait()
		}
	}()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != "CONNECTED" {
		t.Fatal("client could not reconnect")
	}
	if err := r.WM.SendMessage("/ws", "pushed-event"); err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Wait(); err != nil {
		t.Fatal(err)
	}
	results, err = r.Verify([]fixtures.Verification{{Stub: "connect", Exactly: &one}})
	if err != nil || len(results) != 1 || !results[0].Pass {
		t.Fatalf("new scenario: %+v %v", results, err)
	}
}

func TestIOSPackageSelectionDoesNotChangeAndroidProject(t *testing.T) {
	p := testProject(t)
	ios := *p
	ios.Platform, ios.AppID = "ios", "dev.example.ios"
	p.Targets = map[string]*project.Project{"ios": &ios}
	opts := DefaultOptions()
	opts.Platform = "ios"
	e := New(p, opts, &execx.Fake{}, nil)
	if e.P.AppID != ios.AppID || p.AppID != "ie.example.envtest" {
		t.Fatalf("package selection: %s %s", e.P.AppID, p.AppID)
	}
}
