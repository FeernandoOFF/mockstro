package web

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/mockapp"
	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// State is what the page shows in its status pill.
type State = mockapp.State

const (
	StateIdle     = mockapp.Idle
	StateStarting = mockapp.Starting
	StateServing  = mockapp.Serving
	StateStopping = mockapp.Stopping
)
const maxLogLines = 200

var (
	errBusy       = mockapp.ErrBusy
	errNotServing = mockapp.ErrNotServing
)

// LogLine is one activity message from the engine, kept for the page's activity panel.
type LogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// Serving describes the target currently answering requests.
type Serving struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Profile     string            `json:"profile"`
	Overrides   []string          `json:"overrides"`
	FBRC        map[string]string `json:"fbrc"`        // what the app is served now
	DefaultFBRC map[string]string `json:"defaultFbrc"` // what the target itself defines
	Port        int               `json:"port"`
}

// StateView is the JSON the page polls.
type StateView struct {
	State     State                        `json:"state"`
	Error     string                       `json:"error,omitempty"`
	Serving   *Serving                     `json:"serving,omitempty"`
	Custom    []mockruntime.CustomResponse `json:"custom"`
	NoProject bool                         `json:"noProject,omitempty"`
	Log       []LogLine                    `json:"log"`
}

// JournalRow is one request WireMock received.
type JournalRow struct {
	ID        string `json:"id"`
	Time      int64  `json:"time"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Stub      string `json:"stub"`
	Unmatched bool   `json:"unmatched"`
}

// Manager owns at most one mock session for the project, the same way the TUI's Mock tab does.
type Manager struct {
	p      *project.Project
	opts   engine.Options
	runner execx.Runner
	life   *mockapp.Lifecycle
	mu     sync.Mutex
	log    []LogLine
}

func NewManager(p *project.Project, opts engine.Options, runner execx.Runner) *Manager {
	m := &Manager{p: p, opts: opts, runner: runner}
	m.life = mockapp.New(func(ctx context.Context) (mockapp.Backend, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e := m.newEngine()
		if err := e.CheckEnvironment("mock"); err != nil {
			return nil, err
		}
		if _, err := e.Store.ValidateAll(false); err != nil {
			return nil, err
		}
		return e, nil
	})
	return m
}

func (m *Manager) addLog(level, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, LogLine{Time: time.Now(), Level: level, Text: text})
	if len(m.log) > maxLogLines {
		m.log = m.log[len(m.log)-maxLogLines:]
	}
}

func (m *Manager) newEngine() *engine.Engine {
	return engine.New(m.p, m.opts, m.runner, func(ev engine.Event) {
		if ev.Kind == engine.EvLog {
			m.addLog(ev.Level, ev.Text)
		}
	})
}

// State returns a copy safe to marshal while the session changes.
func (m *Manager) State() StateView {
	state := m.life.View()
	m.mu.Lock()
	logs := append([]LogLine(nil), m.log...)
	m.mu.Unlock()
	v := StateView{State: state.State, Error: state.Error, Custom: []mockruntime.CustomResponse{}, Log: logs}
	if sess := state.Session; sess != nil {
		v.Custom = sess.CustomResponses()
		snap := sess.Snapshot()
		v.Serving = &Serving{Kind: snap.Kind, Name: snap.Name, Description: snap.Description, Profile: snap.Profile, Overrides: snap.Overrides, FBRC: snap.FBRC, DefaultFBRC: snap.Defaults, Port: snap.Port}
	}
	return v
}
func (m *Manager) Serve(ref string) error { return m.life.Serve(ref) }
func (m *Manager) Stop() error            { return m.life.Stop() }
func (m *Manager) Shutdown()              { m.life.Shutdown() }
func (m *Manager) Idle() bool             { return m.life.Idle() }

// Journal returns the request journal, newest first.
func (m *Manager) Journal() ([]JournalRow, error) {
	sess, _ := m.life.Session()
	if sess == nil {
		return []JournalRow{}, nil
	}
	reqs, err := sess.Traffic()
	if err != nil {
		return nil, fmt.Errorf("read request journal: %w", err)
	}
	rows := make([]JournalRow, 0, len(reqs))
	for _, r := range reqs {
		if r.Path() == wiremock.NoncePath {
			continue // mockstro's own ownership check, not the app
		}
		rows = append(rows, JournalRow{ID: r.ID, Time: r.Request.LoggedDate, Method: r.Request.Method, URL: r.Request.URL, Stub: r.StubName(), Unmatched: r.Unmatched()})
	}
	return rows, nil
}

func (m *Manager) selectedProject() *project.Project {
	filter := m.opts.Platform
	if filter == "" {
		filter = m.p.Platform
	}
	selected, err := m.p.Selected(filter)
	if err == nil && len(selected) > 0 {
		return selected[0]
	}
	return m.p
}
func (m *Manager) platform() string { return m.selectedProject().Platform }

// Proxy describes the Map Remote rules a lite project needs; full projects need none.
func (m *Manager) Proxy() ProxyView {
	p := m.selectedProject()
	v := ProxyView{Needed: p.Lite(), Rules: []ProxyRule{}, Notes: []string{}}
	if !v.Needed {
		return v
	}
	api := p.ProxyBaseURL
	if api == "" {
		api = "<your API base URL>"
	}
	mapTo := fmt.Sprintf("http://127.0.0.1:%d", m.opts.ServerPort)
	for _, u := range []string{api, p.FBRCURL} {
		v.Rules = append(v.Rules, ProxyRule{Match: u + "/*", MapTo: mapTo})
	}
	v.Notes = append(v.Notes,
		"Leave the destination path and query empty.",
		"Turn off Preserve Host Header if your proxy has it.",
		"The Firebase rule is what lets Remote Config values reach the app.")
	return v
}

// maxDetailBody caps what the page is sent for one body; larger ones are flagged, not truncated silently.
const maxDetailBody = 2 << 20

// RequestDetail is one journal entry in full, for the page's detail drawer and response editor.
type RequestDetail struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Stub      string            `json:"stub"`
	Unmatched bool              `json:"unmatched"`
	Response  ResponseDetail    `json:"response"`
}

type ResponseDetail struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Binary    bool              `json:"binary"`
	Truncated bool              `json:"truncated"`
}

func flattenHeaders(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = t
		case []any:
			parts := make([]string, 0, len(t))
			for _, x := range t {
				parts = append(parts, fmt.Sprint(x))
			}
			out[k] = strings.Join(parts, ", ")
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	return out
}

func clip(s string) (string, bool) {
	if len(s) > maxDetailBody {
		return s[:maxDetailBody], true
	}
	return s, false
}

// Request returns one journal entry in full.
func (m *Manager) Request(id string) (*RequestDetail, error) {
	sess, _ := m.life.Session()
	if sess == nil {
		return nil, errNotServing
	}
	r, err := sess.Request(id)
	if err != nil {
		return nil, err
	}
	d := &RequestDetail{Method: r.Request.Method, URL: r.Request.URL, Path: r.Path(), Headers: flattenHeaders(r.Request.Headers),
		Stub: r.StubName(), Unmatched: r.Unmatched(), Response: ResponseDetail{Headers: map[string]string{}}}
	d.Body, _ = clip(r.Request.Body)
	if r.Response != nil {
		d.Response.Status = r.Response.Status
		d.Response.Headers = flattenHeaders(r.Response.Headers)
		d.Response.Binary = r.Response.Body == "" && r.Response.BodyAsBase64 != ""
		d.Response.Body, d.Response.Truncated = clip(r.Response.Body)
	}
	return d, nil
}

// SetCustom layers a custom response over the served target.
func (m *Manager) SetCustom(c mockruntime.CustomResponse) (mockruntime.CustomResponse, error) {
	sess, _ := m.life.Session()
	if sess == nil {
		return c, errNotServing
	}
	return sess.SetCustomResponse(c)
}

// RemoveCustom puts the target's own answer back for that route.
func (m *Manager) RemoveCustom(id string) error {
	sess, _ := m.life.Session()
	if sess == nil {
		return errNotServing
	}
	return sess.RemoveCustomResponse(id)
}

// proxyTargets are the URLs a lite project's proxy must send to the mock.
type proxyTargets struct {
	api, fbrc string
	port      int
	lite      bool
}

func (m *Manager) proxyTargets() proxyTargets {
	p := m.selectedProject()
	return proxyTargets{api: p.ProxyBaseURL, fbrc: p.FBRCURL, port: m.opts.ServerPort, lite: p.Lite()}
}

// trafficSummary counts the app's requests so far, how many found no stub, and how long the
// current session has been serving.
func (m *Manager) trafficSummary() (count, unmatched int, age time.Duration) {
	since := m.life.View().Since
	rows, err := m.Journal()
	if err != nil {
		return 0, 0, 0
	}
	for _, r := range rows {
		count++
		if r.Unmatched {
			unmatched++
		}
	}
	return count, unmatched, time.Since(since)
}

// SetFBRC changes the Remote Config the app is served, without touching anything else.
func (m *Manager) SetFBRC(values map[string]string) error {
	sess, _ := m.life.Session()
	if sess == nil {
		return errNotServing
	}
	return sess.SetFBRC(context.Background(), values)
}

// ResetFBRC puts back the Remote Config the running target defines.
func (m *Manager) ResetFBRC() error {
	sess, _ := m.life.Session()
	if sess == nil {
		return errNotServing
	}
	return sess.ResetFBRC(context.Background())
}
