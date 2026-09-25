// Package wiremock manages the pinned WireMock jar, per-run servers and their admin API.
package wiremock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fsx"
	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/paths"
)

const (
	Version   = "3.13.2"
	SHA256    = "d097b19bd483c5038479b13a5c71e9faf8f2f5106584f0c120a7770ab0bdb367"
	jarURL    = "https://repo1.maven.org/maven2/org/wiremock/wiremock-standalone/" + Version + "/wiremock-standalone-" + Version + ".jar"
	NoncePath = "/__maestro/nonce"
	Fallback  = "maestro-fallback"
)

func JarPath() string {
	return filepath.Join(paths.CacheRoot(), "wiremock", "wiremock-standalone-"+Version+".jar")
}

// EnsureJar returns a SHA-verified jar, reusing a project's copy before downloading.
func EnsureJar(ctx context.Context, projectCopy string, logf func(string, ...any)) (string, error) {
	jar := JarPath()
	if sum, err := fsx.SHA256(jar); err == nil {
		if sum == SHA256 {
			return jar, nil
		}
		logf("Setting aside WireMock JAR with invalid SHA-256: %s", sum)
		_ = os.Rename(jar, fmt.Sprintf("%s.bad.%d.%d", jar, time.Now().Unix(), os.Getpid()))
	}
	if err := os.MkdirAll(filepath.Dir(jar), 0o755); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.download.%d", jar, os.Getpid())
	defer os.Remove(tmp)
	if projectCopy != "" {
		if sum, err := fsx.SHA256(projectCopy); err == nil && sum == SHA256 {
			if err := fsx.CopyAtomic(projectCopy, tmp); err == nil {
				return jar, publish(tmp, jar)
			}
		}
	}
	logf("Downloading WireMock standalone %s...", Version)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, jarURL, nil)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		f, err := os.Create(tmp)
		if err != nil {
			resp.Body.Close()
			return "", err
		}
		_, err = io.Copy(f, resp.Body)
		resp.Body.Close()
		f.Close()
		if err != nil || resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HTTP %d %v", resp.StatusCode, err)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return "", fmt.Errorf("failed to download WireMock from %s: %w", jarURL, lastErr)
	}
	sum, _ := fsx.SHA256(tmp)
	if sum != SHA256 {
		return "", fmt.Errorf("WireMock SHA-256 mismatch: expected %s, got %s", SHA256, sum)
	}
	return jar, publish(tmp, jar)
}

// publish only replaces the jar if nobody else already won the race with a valid one.
func publish(tmp, jar string) error {
	if sum, err := fsx.SHA256(jar); err == nil && sum == SHA256 {
		return nil
	}
	return os.Rename(tmp, jar)
}

// PortListener describes whoever is listening on port, or "" when free.
func PortListener(ctx context.Context, ex execx.Runner, port int) string {
	out, err := ex.Output(ctx, execx.Cmd{Name: "lsof", Args: []string{"-nP", "-iTCP:" + strconv.Itoa(port), "-sTCP:LISTEN"}})
	if err == nil && out != "" {
		lines := strings.Split(out, "\n")
		if len(lines) > 1 {
			f := strings.Fields(lines[1])
			if len(f) >= 2 {
				return fmt.Sprintf("%s (pid %s)", f[0], f[1])
			}
		}
	}
	return "" // lsof is a checked prerequisite, so no bind-probe fallback
}

// Server is one run's WireMock.
type Server struct {
	Port  int
	RunID string
	Log   string
	cmd   *exec.Cmd
	exit  chan struct{}
	http  *http.Client
}

func (s *Server) base() string { return "http://127.0.0.1:" + strconv.Itoa(s.Port) }

// Start launches the JVM against root and proves ownership with a nonce before returning.
func Start(ctx context.Context, ex execx.Runner, jar, root string, port int, runID, logPath string) (*Server, error) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("WireMock root does not exist: %s", root)
	}
	if l := PortListener(ctx, ex, port); l != "" {
		return nil, fmt.Errorf("port %d is already in use by %s.\n  That is usually another Maestro run. Use --server-port <port> to pick a free one", port, l)
	}
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd, err := execx.Start(execx.Cmd{
		Name:   "java",
		Args:   []string{"-jar", jar, "--port", strconv.Itoa(port), "--bind-address", "127.0.0.1", "--root-dir", root},
		Stdout: logf, Stderr: logf,
	}, false)
	if err != nil {
		logf.Close()
		return nil, fmt.Errorf("start WireMock: %w", err)
	}
	s := &Server{Port: port, RunID: runID, Log: logPath, cmd: cmd, exit: make(chan struct{}), http: &http.Client{Timeout: 10 * time.Second}}
	go func() { _ = cmd.Wait(); logf.Close(); close(s.exit) }()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		// Liveness before readiness: if our JVM died because something else took the port,
		// a probe answered by that other server must not read as success.
		select {
		case <-s.exit:
			return nil, fmt.Errorf("WireMock exited during startup; see %s", logPath)
		case <-ctx.Done():
			s.Stop()
			return nil, ctx.Err()
		default:
		}
		if resp, err := s.http.Get(s.base() + "/__admin/mappings"); err == nil {
			resp.Body.Close()
			if err := s.post("/__admin/mappings", NonceMapping(runID)); err != nil {
				s.Stop()
				return nil, fmt.Errorf("failed to install the run nonce on port %d: %w", port, err)
			}
			if err := s.AssertOwn(); err != nil {
				s.Stop()
				return nil, err
			}
			return s, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	s.Stop()
	return nil, fmt.Errorf("WireMock did not become ready on 127.0.0.1:%d; see %s", port, logPath)
}

// Done is closed when the JVM exits.
func (s *Server) Done() <-chan struct{} { return s.exit }

func (s *Server) Stop() {
	if s == nil || s.cmd == nil {
		return
	}
	select {
	case <-s.exit:
		return
	default:
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.exit:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exit
	}
}

// AssertOwn refuses to touch a server that is not this run's (the nonce answers the run id).
func (s *Server) AssertOwn() error {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(s.base() + NoncePath)
	seen := ""
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen = strings.TrimSpace(string(b))
	}
	if seen == s.RunID {
		return nil
	}
	if seen == "" {
		seen = "no response"
	}
	return fmt.Errorf("WireMock on port %d is not this run's server (expected run %s, got '%s').\n  Another Maestro run almost certainly owns it. Check with: lsof -nP -iTCP:%d -sTCP:LISTEN", s.Port, s.RunID, seen, s.Port)
}

func (s *Server) do(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		var b []byte
		switch t := body.(type) {
		case []byte:
			b = t
		default:
			var err error
			b, err = ojson.Marshal(t)
			if err != nil {
				return nil, err
			}
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base()+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (s *Server) post(path string, body any) error {
	_, err := s.do(http.MethodPost, path, body)
	return err
}

// Replace wipes every mapping and imports the given set (the nonce must be in it).
func (s *Server) Replace(mappings []any) error {
	if err := s.AssertOwn(); err != nil {
		return err
	}
	if _, err := s.do(http.MethodDelete, "/__admin/mappings", nil); err != nil {
		return fmt.Errorf("failed to clear WireMock mappings: %w", err)
	}
	payload := map[string]any{"mappings": mappings}
	if err := s.post("/__admin/mappings/import", payload); err != nil {
		return fmt.Errorf("failed to bulk import WireMock scenario: %w", err)
	}
	if err := s.AssertOwn(); err != nil {
		return err
	}
	if _, err := s.do(http.MethodDelete, "/__admin/requests", nil); err != nil {
		return fmt.Errorf("failed to reset WireMock request journal: %w", err)
	}
	return nil
}

type LoggedRequest struct {
	Request struct {
		URL        string `json:"url"`
		Method     string `json:"method"`
		LoggedDate int64  `json:"loggedDate"`
	} `json:"request"`
	StubMapping *struct {
		Name string `json:"name"`
	} `json:"stubMapping"`
}

func (r LoggedRequest) StubName() string {
	if r.StubMapping == nil {
		return ""
	}
	return r.StubMapping.Name
}

func (r LoggedRequest) Path() string {
	p, _, _ := strings.Cut(r.Request.URL, "?")
	return p
}

// Journal returns requests newest first. own=false skips the nonce check (read-only views).
func (s *Server) Journal(own bool) ([]LoggedRequest, error) {
	if own {
		if err := s.AssertOwn(); err != nil {
			return nil, err
		}
	}
	b, err := s.do(http.MethodGet, "/__admin/requests", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read WireMock request journal: %w", err)
	}
	var j struct {
		Requests []LoggedRequest `json:"requests"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	return j.Requests, nil
}

func (s *Server) StartRecording(target string) error {
	return s.post("/__admin/recordings/start", map[string]any{
		"targetBaseUrl": target,
		"filters":       map[string]any{"urlPathPattern": "/api/.*"},
		"persist":       true,
	})
}

func (s *Server) StopRecording() error {
	if s.AssertOwn() != nil {
		return nil
	}
	return s.post("/__admin/recordings/stop", nil)
}

// Mapping builders. WireMock ignores key order, so plain maps will do.

func NonceMapping(runID string) map[string]any {
	return map[string]any{
		"name":     "maestro-run-nonce",
		"priority": 1,
		"request":  map[string]any{"method": "GET", "urlPath": NoncePath},
		"response": map[string]any{"status": 200, "headers": map[string]string{"Content-Type": "text/plain"}, "body": runID},
	}
}

// FallbackMapping answers every unstubbed request with a diagnostic 599 (or proxies).
func FallbackMapping(proxyURL string) map[string]any {
	response := map[string]any{
		"status":   599,
		"headers":  map[string]string{"Content-Type": "application/json"},
		"jsonBody": map[string]string{"error": "No WireMock stub matched this request"},
	}
	if proxyURL != "" {
		response = map[string]any{"proxyBaseUrl": proxyURL}
	}
	return map[string]any{
		"name":     Fallback,
		"priority": 100,
		"request":  map[string]any{"method": "ANY", "urlPattern": ".*"},
		"response": response,
	}
}

// FBRCMappings serve Remote Config over HTTP for Map Remote setups (no device override).
func FBRCMappings(entries map[string]string) []any {
	eb, _ := ojson.Marshal(entries) // map keys sort, so the ETag is stable
	version := strconv.FormatUint(uint64(crc32.ChecksumIEEE(eb)), 10)
	perf := map[string]any{
		"name":     "firebase-performance/fetch",
		"priority": 1,
		"request":  map[string]any{"method": "POST", "urlPathPattern": "^/v1/projects/[^/]+/namespaces/fireperf:fetch$"},
		"response": map[string]any{"status": 200},
	}
	rc := map[string]any{
		"name":     "firebase-remote-config/fetch",
		"priority": 1,
		"request":  map[string]any{"method": "POST", "urlPathPattern": "^/v1/projects/[^/]+/namespaces/firebase:fetch$"},
		"response": map[string]any{
			"status":   200,
			"headers":  map[string]string{"Content-Type": "application/json", "ETag": `"maestro-` + version + `"`},
			"jsonBody": map[string]any{"state": "UPDATE", "entries": entries, "templateVersion": version},
		},
	}
	return []any{perf, rc}
}
