// Package web serves a local, browser-based front end for `mockstro mock`.
//
// It binds to loopback only and is meant for one person on their own machine: every request must
// carry a Host header naming that loopback address, and every state-changing request must carry
// the per-launch token embedded in the page, so another website cannot drive the mock.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/remoteconfig"
)

//go:embed static/index.html
var indexHTML string

//go:embed static/favicon.png
var faviconPNG []byte

// DefaultPort is where the UI listens unless told otherwise; 18080 is reserved for WireMock.
const DefaultPort = 18090

const (
	tokenHeader = "X-Mockstro-Token"
	appName     = "mockstro-ui"
)

// Config is what the CLI hands the UI.
type Config struct {
	// Load opens a folder as a project; Initial, when set, is opened at launch.
	Load    Loader
	Initial string
	Port    int
	Open    bool
	Out     io.Writer
}

// ProxyRule is one Map Remote row QA enters in Charles or Proxyman.
type ProxyRule struct {
	Match string `json:"match"`
	MapTo string `json:"mapTo"`
}

// ProxyView is the Charles/Proxyman setup the page shows.
type ProxyView struct {
	Needed bool        `json:"needed"`
	Rules  []ProxyRule `json:"rules"`
	Notes  []string    `json:"notes"`
}

type Server struct {
	ws    *Workspace
	ex    execx.Runner
	token string
	// instance changes on every launch and is not secret: the page compares it to notice a restart.
	instance string
	port     int
	mux      *http.ServeMux
	quit     func()

	// charlesConfig is where Charles keeps its settings; overridden in tests.
	charlesConfig string
}

func NewServer(ws *Workspace, port int) (*Server, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	inst := make([]byte, 8)
	if _, err := rand.Read(inst); err != nil {
		return nil, err
	}
	s := &Server{ws: ws, ex: ws.runner, token: hex.EncodeToString(buf), instance: hex.EncodeToString(inst), port: port, mux: http.NewServeMux(), quit: func() {}, charlesConfig: defaultCharlesConfig()}
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /api/ping", s.ping)
	s.mux.HandleFunc("GET /favicon.png", s.favicon)
	s.mux.HandleFunc("GET /api/project", s.projectInfo)
	s.mux.HandleFunc("GET /api/project/icon", s.projectIcon)
	s.mux.HandleFunc("POST /api/project/open", s.openProject)
	s.mux.HandleFunc("POST /api/project/pick", s.pickProject)
	s.mux.HandleFunc("POST /api/project/clone", s.cloneProject)
	s.mux.HandleFunc("GET /api/project/setup-branches", s.setupBranches)
	s.mux.HandleFunc("POST /api/project/open-branch", s.openBranch)
	s.mux.HandleFunc("POST /api/project/remove", s.removeProject)
	s.mux.HandleFunc("GET /api/git", s.gitInfo)
	s.mux.HandleFunc("GET /api/git/branches", s.gitBranches)
	s.mux.HandleFunc("POST /api/git/fetch", s.gitFetch)
	s.mux.HandleFunc("POST /api/git/pull", s.gitPull)
	s.mux.HandleFunc("POST /api/git/switch", s.gitSwitch)
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /api/catalog", s.catalog)
	s.mux.HandleFunc("GET /api/preview", s.preview)
	s.mux.HandleFunc("GET /api/stub", s.stubDetail)
	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("GET /api/journal", s.journal)
	s.mux.HandleFunc("GET /api/journal/{id}", s.requestDetail)
	s.mux.HandleFunc("GET /api/proxy", s.proxy)
	s.mux.HandleFunc("GET /api/saved", s.listSaved)
	s.mux.HandleFunc("POST /api/saved", s.saveResponse)
	s.mux.HandleFunc("POST /api/saved/apply", s.applySaved)
	s.mux.HandleFunc("POST /api/saved/delete", s.deleteSaved)
	s.mux.HandleFunc("GET /api/fbrc/keys", s.fbrcKeys)
	s.mux.HandleFunc("POST /api/fbrc", s.setFBRC)
	s.mux.HandleFunc("POST /api/fbrc/reset", s.resetFBRC)
	s.mux.HandleFunc("POST /api/custom", s.setCustom)
	s.mux.HandleFunc("POST /api/custom/remove", s.removeCustom)
	s.mux.HandleFunc("POST /api/serve", s.serve)
	s.mux.HandleFunc("POST /api/stop", s.stop)
	s.mux.HandleFunc("POST /api/quit", s.quitApp)
	return s, nil
}

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.guard) }

// guard rejects requests that did not come from a page on our own loopback address.
func (s *Server) guard(w http.ResponseWriter, r *http.Request) {
	if !s.allowedHost(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Mockstro-Instance", s.instance)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
			// The usual cause is a page left open across a restart; the page reloads itself on this.
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "MockStro was restarted since this page loaded. Reload the page.", "reload": true})
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Mockstro-Instance", s.instance)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) allowedHost(host string) bool {
	port := strconv.Itoa(s.port)
	return host == "127.0.0.1:"+port || host == "localhost:"+port
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	_, _ = io.WriteString(w, strings.Replace(indexHTML, "__TOKEN__", s.token, 1))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

var errNoProject = errors.New("no project is open; choose a project folder first")

// cur returns the open project and manager, or answers 409 and reports false.
func (s *Server) cur(w http.ResponseWriter) (*project.Project, *Manager, bool) {
	p, m := s.ws.Current()
	if p == nil || m == nil {
		writeError(w, http.StatusConflict, errNoProject)
		return nil, nil, false
	}
	return p, m, true
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"app": appName})
}

// ProjectView is what the page's project bar shows.
type ProjectView struct {
	Open     bool            `json:"open"`
	Path     string          `json:"path,omitempty"`
	Name     string          `json:"name,omitempty"`
	Icon     bool            `json:"icon,omitempty"`
	Platform string          `json:"platform,omitempty"`
	CanPick  bool            `json:"canPick"`
	CloneDir string          `json:"cloneDir,omitempty"`
	Recent   []RecentProject `json:"recent"`
}

func (s *Server) projectInfo(w http.ResponseWriter, r *http.Request) {
	v := ProjectView{CanPick: runtime.GOOS == "darwin", CloneDir: s.ws.ClonesDir(), Recent: s.ws.Recent()}
	if p, m := s.ws.Current(); p != nil && m != nil {
		v.Open, v.Path, v.Name, v.Platform = true, p.Root, filepath.Base(p.Root), m.platform()
		v.Icon = projectIconFile(p.Root, false) != ""
	}
	for i := range v.Recent {
		v.Recent[i].Icon = projectIconFile(v.Recent[i].Path, false) != ""
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(faviconPNG)
}

// projectIconFile is the icon IntelliJ and Android Studio show for a project, if it has one.
func projectIconFile(root string, dark bool) string {
	names := []string{"icon.svg", "icon.png"}
	if dark {
		names = append([]string{"icon_dark.svg", "icon_dark.png"}, names...)
	}
	for _, n := range names {
		f := filepath.Join(root, ".idea", n)
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() && st.Size() <= 1<<20 {
			return f
		}
	}
	return ""
}

// projectIcon serves the icon of the open project or a recent one; other paths are refused.
func (s *Server) projectIcon(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	known := false
	if p, _ := s.ws.Current(); p != nil && p.Root == path {
		known = true
	}
	for _, rp := range s.ws.Recent() {
		known = known || rp.Path == path
	}
	f := ""
	if known {
		f = projectIconFile(path, r.URL.Query().Get("dark") == "1")
	}
	if f == "" {
		http.NotFound(w, r)
		return
	}
	// The SVG comes from the repository, so it must never run as a page on this origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasSuffix(f, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	} else {
		w.Header().Set("Content-Type", "image/png")
	}
	http.ServeFile(w, r, f)
}

func (s *Server) openProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" || !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, errors.New("type the full path of the project folder, starting with /"))
		return
	}
	if err := s.ws.Open(path); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "kind": "notProject", "path": path})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// removeProject forgets a project and, for clones MockStro made, deletes the folder.
func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Delete bool   `json:"delete"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, errors.New("path must be absolute"))
		return
	}
	if err := s.ws.Remove(req.Path, req.Delete); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errOpenProject) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pickScript shows the native macOS folder chooser and brings it to the front.
const pickScript = `tell current application to activate
POSIX path of (choose folder with prompt "Choose the project folder (the one that contains a .maestro folder)")`

// pickProject asks macOS for a folder and returns the path; opening it is a separate call.
func (s *Server) pickProject(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "darwin" {
		writeError(w, http.StatusNotImplemented, errors.New("the folder chooser only works on macOS; type the path instead"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	out, err := s.ex.Output(ctx, execx.Cmd{Name: "osascript", Args: []string{"-e", pickScript}})
	if err != nil {
		if strings.Contains(err.Error(), "User canceled") || strings.Contains(out, "User canceled") {
			writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
			return
		}
		writeError(w, http.StatusBadGateway, fmt.Errorf("could not show the folder chooser: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": strings.TrimRight(strings.TrimSpace(out), "/")})
}

func (s *Server) quitApp(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	go s.quit()
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	p, m, ok := s.cur(w)
	if !ok {
		return
	}
	items, err := Catalog(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "project": p.Root, "platform": m.platform()})
}

// preview describes one catalog item without starting it: its steps, checks, stubs and Remote Config.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	items, err := Catalog(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	it, ok := findItem(items, r.URL.Query().Get("kind"), r.URL.Query().Get("ref"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown %s %q", r.URL.Query().Get("kind"), r.URL.Query().Get("ref")))
		return
	}
	pv, err := PreviewOf(p, it)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

// stubDetail returns one stub's full response, for the preview to show before anything is served.
func (s *Server) stubDetail(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	d, err := StubDetailOf(p, r.URL.Query().Get("name"))
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "unknown stub"):
		writeError(w, http.StatusNotFound, err)
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	_, m := s.ws.Current()
	if m == nil {
		writeJSON(w, http.StatusOK, StateView{State: StateIdle, NoProject: true, Custom: []mockruntime.CustomResponse{}, Log: []LogLine{}})
		return
	}
	writeJSON(w, http.StatusOK, m.State())
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request) {
	_, m := s.ws.Current()
	if m == nil {
		writeJSON(w, http.StatusOK, []JournalRow{})
		return
	}
	rows, err := m.Journal()
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	d, err := m.Request(r.PathValue("id"))
	switch {
	case errors.Is(err, errNotServing):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) setCustom(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	var c mockruntime.CustomResponse
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := checkBodyMatchesContentType(&c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	saved, err := m.SetCustom(c)
	switch {
	case errors.Is(err, errNotServing):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

func (s *Server) removeCustom(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := m.RemoveCustom(req.ID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) fbrcKeys(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	items, err := Catalog(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": KnownKeys(items)})
}

func (s *Server) setFBRC(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Values == nil {
		req.Values = map[string]string{}
	}
	switch err := m.SetFBRC(req.Values); {
	case errors.Is(err, errNotServing):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) resetFBRC(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	switch err := m.ResetFBRC(); {
	case errors.Is(err, errNotServing):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadGateway, err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) listSaved(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	stubs, err := fixtures.New(p).LocalStubs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": stubs, "dir": p.Rel(filepath.Join(p.StubsRoot(), fixtures.LocalStubDir))})
}

func (s *Server) saveResponse(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
		mockruntime.CustomResponse
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c := req.CustomResponse
	if err := checkBodyMatchesContentType(&c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := c.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	saved, err := fixtures.New(p).SaveLocalStub(fixtures.LocalStub{Title: req.Title, Method: c.Method, Path: c.Path,
		Status: c.Status, Headers: c.Headers, Body: c.Body, DelayMs: c.DelayMs})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) applySaved(w http.ResponseWriter, r *http.Request) {
	p, m, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stub, err := fixtures.New(p).LocalStub(req.Name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	saved, err := m.SetCustom(mockruntime.CustomResponse{Method: stub.Method, Path: stub.Path, Status: stub.Status,
		Headers: stub.Headers, Body: stub.Body, DelayMs: stub.DelayMs})
	switch {
	case errors.Is(err, errNotServing):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
	default:
		writeJSON(w, http.StatusOK, saved)
	}
}

func (s *Server) deleteSaved(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := fixtures.New(p).DeleteLocalStub(req.Name); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	_, m, ok := s.cur(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, m.Proxy())
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.cur(w)
	if !ok {
		return
	}
	var req struct {
		Kind string            `json:"kind"`
		Ref  string            `json:"ref"`
		FBRC map[string]string `json:"fbrc"` // Remote Config chosen in the preview; absent means the target's own
		// Responses edited in the preview, served over the target as soon as it is up.
		Custom []mockruntime.CustomResponse `json:"custom"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Only refs the catalog lists are served, so a crafted ref cannot point at an arbitrary file.
	items, err := Catalog(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, ok := findItem(items, req.Kind, req.Ref); !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown %s %q", req.Kind, req.Ref))
		return
	}
	fbrc := req.FBRC
	if fbrc != nil {
		var err error
		if fbrc, err = remoteconfig.Validate(fbrc); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	for i := range req.Custom {
		if err := req.Custom[i].Validate(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("response for %s %s: %w", req.Custom[i].Method, req.Custom[i].Path, err))
			return
		}
		if err := checkBodyMatchesContentType(&req.Custom[i]); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("response for %s %s: %w", req.Custom[i].Method, req.Custom[i].Path, err))
			return
		}
	}
	if err := s.ws.Serve(req.Ref, fbrc, req.Custom); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	if err := s.ws.Stop(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// Run serves the UI until ctx ends or the page asks to quit, then stops any mock it started.
// If another UI already owns the port it opens that one instead.
func Run(ctx context.Context, cfg Config) error {
	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) && alreadyRunning(ctx, port) {
			if cfg.Out != nil {
				fmt.Fprintf(cfg.Out, "mockstro UI is already running: %s\n", url)
			}
			if cfg.Open {
				openBrowser(url)
			}
			return nil
		}
		return fmt.Errorf("cannot listen on 127.0.0.1:%d: %w", port, err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ws := NewWorkspace(cfg.Load, DefaultRecentsFile(), execx.Real{})
	if cfg.Initial != "" {
		if err := ws.Open(cfg.Initial); err != nil && cfg.Out != nil {
			fmt.Fprintf(cfg.Out, "Not opening %s: %v\n", cfg.Initial, err)
		}
	}
	if p, _ := ws.Current(); p == nil {
		if last, ok := ws.LastRecent(); ok {
			_ = ws.Open(last)
		}
	}
	srv, err := NewServer(ws, port)
	if err != nil {
		return err
	}
	srv.quit = cancel
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	if cfg.Out != nil {
		fmt.Fprintf(cfg.Out, "mockstro UI: %s\nPress Ctrl-C to stop (this also stops the mock server).\n", url)
	}
	if cfg.Open {
		openBrowser(url)
	}

	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		ws.Shutdown()
		return err
	case <-ctx.Done():
	}
	ws.Shutdown()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := hs.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// alreadyRunning reports whether the busy port belongs to another mockstro UI.
func alreadyRunning(ctx context.Context, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/ping", port), nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		App string `json:"app"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&v) == nil && v.App == appName
}

func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	_ = exec.Command(name, url).Start()
}
