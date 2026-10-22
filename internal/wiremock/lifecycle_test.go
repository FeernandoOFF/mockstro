package wiremock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

type fakeProcess struct {
	exit    chan struct{}
	once    sync.Once
	waits   atomic.Int32
	signals atomic.Int32
	kills   atomic.Int32
}

func newProcess() *fakeProcess                { return &fakeProcess{exit: make(chan struct{})} }
func (p *fakeProcess) finish()                { p.once.Do(func() { close(p.exit) }) }
func (p *fakeProcess) Wait() error            { p.waits.Add(1); <-p.exit; return nil }
func (p *fakeProcess) Signal(os.Signal) error { p.signals.Add(1); p.finish(); return nil }
func (p *fakeProcess) Kill() error            { p.kills.Add(1); p.finish(); return nil }
func (*fakeProcess) PID() int                 { return 42 }

func loopbackServer(t *testing.T, handler http.Handler) (*httptest.Server, int) {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Listener = ln
	s.Start()
	t.Cleanup(s.Close)
	return s, ln.Addr().(*net.TCPAddr).Port
}

func TestInjectedStartupProvesOwnershipAndStopWaits(t *testing.T) {
	const runID = "test-run"
	var mu sync.Mutex
	var calls []string
	_, port := loopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == wiremock.NoncePath {
			io.WriteString(w, runID)
			return
		}
		if r.Method == http.MethodPost {
			var mapping struct{ Response struct{ Body string } }
			if err := json.NewDecoder(r.Body).Decode(&mapping); err != nil || mapping.Response.Body != runID {
				t.Errorf("nonce mapping %+v err %v", mapping, err)
			}
		}
		io.WriteString(w, `{}`)
	}))
	dir := t.TempDir()
	p := newProcess()
	t.Cleanup(p.finish)
	fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", errors.New("no listener") }, StartHandler: func(c execx.Cmd, detach bool) (execx.Process, error) {
		if c.Name != "java" || detach || !strings.Contains(c.String(), "-Xmx128m -jar wiremock-"+wiremock.WebSocketVersion+".jar --port ") || !strings.HasSuffix(c.String(), "--bind-address 127.0.0.1 --root-dir "+dir) {
			t.Errorf("command %+v detach %v", c, detach)
		}
		io.WriteString(c.Stdout, "server output\n")
		io.WriteString(c.Stderr, "server error\n")
		return p, nil
	}}
	log := filepath.Join(dir, "wiremock.log")
	s, err := wiremock.Start(context.Background(), fake, "wiremock-"+wiremock.WebSocketVersion+".jar", dir, port, runID, log, "-Xmx128m")
	if err != nil {
		t.Fatal(err)
	}
	if !s.MessageSupport {
		t.Error("WebSocket JAR not recognized")
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"GET /__admin/mappings", "POST /__admin/mappings", "GET " + wiremock.NoncePath}) {
		t.Fatalf("requests %v", got)
	}
	s.Stop()
	s.Stop()
	select {
	case <-s.Done():
	default:
		t.Error("Stop returned before process exit")
	}
	if p.waits.Load() != 1 || p.signals.Load() != 1 || p.kills.Load() != 0 {
		t.Fatalf("waits %d signals %d kills %d", p.waits.Load(), p.signals.Load(), p.kills.Load())
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "server output\nserver error\n" {
		t.Fatalf("log %q err %v", data, err)
	}
}

func TestStartupFailureReleasesOwnedProcess(t *testing.T) {
	for _, failure := range []string{"nonce install", "wrong owner"} {
		t.Run(failure, func(t *testing.T) {
			_, port := loopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "nonce install" && r.Method == http.MethodPost {
					http.Error(w, "rejected", 500)
					return
				}
				io.WriteString(w, "someone else")
			}))
			p := newProcess()
			t.Cleanup(p.finish)
			fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }, StartHandler: func(execx.Cmd, bool) (execx.Process, error) { return p, nil }}
			dir := t.TempDir()
			s, err := wiremock.Start(context.Background(), fake, "fake.jar", dir, port, "run", filepath.Join(dir, "log"))
			if err == nil || s != nil || p.waits.Load() != 1 || p.signals.Load() != 1 {
				t.Fatalf("server %v err %v waits %d signals %d", s, err, p.waits.Load(), p.signals.Load())
			}
		})
	}
}

func TestStartupCancellationInterruptsReadinessRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	_, port := loopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	p := newProcess()
	t.Cleanup(p.finish)
	fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }, StartHandler: func(execx.Cmd, bool) (execx.Process, error) { return p, nil }}
	dir := t.TempDir()
	result := make(chan error, 1)
	go func() {
		_, err := wiremock.Start(ctx, fake, "fake.jar", dir, port, "run", filepath.Join(dir, "log"))
		result <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup request did not respect cancellation")
	}
	if p.waits.Load() != 1 || p.signals.Load() != 1 {
		t.Fatalf("waits %d signals %d", p.waits.Load(), p.signals.Load())
	}
}

func TestStartupRejectsFailedAcquisitionAndEarlyExit(t *testing.T) {
	t.Run("occupied port", func(t *testing.T) {
		dir := t.TempDir()
		fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "COMMAND PID USER\njava 123 user", nil }}
		_, err := wiremock.Start(context.Background(), fake, "fake.jar", dir, 1234, "run", filepath.Join(dir, "log"))
		if err == nil || !strings.Contains(err.Error(), "java (pid 123)") || len(fake.Calls) != 1 {
			t.Fatalf("err %v calls %v", err, fake.Calls)
		}
	})
	t.Run("start error", func(t *testing.T) {
		boom := errors.New("launch failed")
		dir := t.TempDir()
		fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }, StartHandler: func(execx.Cmd, bool) (execx.Process, error) { return nil, boom }}
		_, err := wiremock.Start(context.Background(), fake, "fake.jar", dir, 1234, "run", filepath.Join(dir, "log"))
		if !errors.Is(err, boom) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("early exit", func(t *testing.T) {
		dir := t.TempDir()
		p := newProcess()
		p.finish()
		fake := &execx.Fake{Handler: func(execx.Cmd) (string, error) { return "", nil }, StartHandler: func(execx.Cmd, bool) (execx.Process, error) { return p, nil }}
		_, err := wiremock.Start(context.Background(), fake, "fake.jar", dir, 0, "run", filepath.Join(dir, "log"))
		if err == nil || !strings.Contains(err.Error(), "exited during startup") || p.waits.Load() != 1 {
			t.Fatalf("err %v waits %d", err, p.waits.Load())
		}
	})
}
