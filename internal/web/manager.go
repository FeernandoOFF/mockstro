package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// State is what the page shows in its status pill.
type State string

const (
	StateIdle     State = "idle"
	StateStarting State = "starting"
	StateServing  State = "serving"
	StateStopping State = "stopping"
)

const maxLogLines = 200

var (
	errBusy       = errors.New("another start, switch or stop is still in progress")
	errNotServing = errors.New("no mock is running; start one first")
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
	Steps       []Step            `json:"steps,omitempty"`  // a flow's commands as a tester would follow them
	Checks      []LiveCheck       `json:"checks,omitempty"` // a flow's apiVerifications, counted by the page
}

// StateView is the JSON the page polls.
type StateView struct {
	State     State                   `json:"state"`
	Error     string                  `json:"error,omitempty"`
	Serving   *Serving                `json:"serving,omitempty"`
	Custom    []engine.CustomResponse `json:"custom"`
	NoProject bool                    `json:"noProject,omitempty"`
	Log       []LogLine               `json:"log"`
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
	p    *project.Project
	opts engine.Options

	mu    sync.Mutex
	state State
	err   string
	busy  bool
	since time.Time // when the current session started serving
	eng   *engine.Engine
	sess  *engine.MockSession
	log   []LogLine

	// guide is the served flow's steps and checks, read once per served target rather than on every poll.
	guideOf     *engine.MockTarget
	guideSteps  []Step
	guideChecks []LiveCheck
}

func NewManager(p *project.Project, opts engine.Options) *Manager {
	return &Manager{p: p, opts: opts, state: StateIdle}
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
	return engine.New(m.p, m.opts, execx.Real{}, func(ev engine.Event) {
		if ev.Kind == engine.EvLog {
			m.addLog(ev.Level, ev.Text)
		}
	})
}

// State returns a copy safe to marshal while the session changes.
func (m *Manager) State() StateView {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := StateView{State: m.state, Error: m.err, Custom: []engine.CustomResponse{}, Log: append([]LogLine(nil), m.log...)}
	if m.sess != nil {
		v.Custom = m.sess.CustomResponses()
		snap := m.sess.Snapshot()
		s := &Serving{Kind: snap.Kind, Name: snap.Name, Profile: snap.Profile, Overrides: snap.Overrides, FBRC: snap.FBRC, DefaultFBRC: snap.Defaults, Port: snap.Port}
		t := m.sess.Target()
		if t.Header != nil {
			s.Description = t.Header.Description
		}
		s.Steps, s.Checks = m.guideFor(t)
		v.Serving = s
	}
	return v
}

// guideFor returns the served flow's steps and checks, reading the flow once per served target.
// The caller holds m.mu.
func (m *Manager) guideFor(t *engine.MockTarget) ([]Step, []LiveCheck) {
	if t.Kind != "flow" || t.Header == nil {
		return nil, nil
	}
	if m.guideOf != t {
		m.guideOf, m.guideSteps, m.guideChecks = t, FlowSteps(t.Header.Path), ChecksFor(t.Header.Verifications)
	}
	return m.guideSteps, m.guideChecks
}

// Serve starts the mock on ref, or switches the running server to it. It returns once the work
// is under way; callers poll State for the outcome.
func (m *Manager) Serve(ref string, fbrc map[string]string) error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return errBusy
	}
	m.busy, m.err = true, ""
	sess, eng := m.sess, m.eng
	if sess == nil {
		m.state = StateStarting
	}
	m.mu.Unlock()

	go func() {
		var err error
		if sess == nil {
			err = m.start(ref, fbrc)
		} else {
			err = m.switchTo(sess, eng, ref, fbrc)
		}
		m.mu.Lock()
		m.busy = false
		if err != nil {
			m.err = err.Error()
			if m.sess == nil {
				m.state = StateIdle
			}
		}
		m.mu.Unlock()
		if err != nil {
			m.addLog("error", err.Error())
		}
	}()
	return nil
}

// start serves ref. A non-nil fbrc is the Remote Config the tester chose in the preview, served from the first fetch.
func (m *Manager) start(ref string, fbrc map[string]string) error {
	e := m.newEngine()
	if err := e.CheckEnvironment("mock"); err != nil {
		return err
	}
	if _, err := e.Store.ValidateAll(false); err != nil {
		return err
	}
	target, err := e.ResolveMockTarget([]string{ref})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sess, err := e.StartMock(ctx, target)
	if err != nil {
		return err
	}
	if fbrc != nil {
		if err := sess.SetFBRC(ctx, fbrc); err != nil {
			sess.Stop()
			return err
		}
	}
	m.mu.Lock()
	m.sess, m.eng, m.state, m.since = sess, e, StateServing, time.Now()
	m.mu.Unlock()
	go m.watch(sess)
	return nil
}

func (m *Manager) switchTo(sess *engine.MockSession, e *engine.Engine, ref string, fbrc map[string]string) error {
	target, err := e.ResolveMockTarget([]string{ref})
	if err != nil {
		return err
	}
	if err := sess.Switch(context.Background(), target); err != nil {
		return err
	}
	if fbrc != nil {
		return sess.SetFBRC(context.Background(), fbrc)
	}
	return nil
}

// watch notices WireMock dying on its own (killed, crashed) and returns the page to idle.
func (m *Manager) watch(sess *engine.MockSession) {
	<-sess.Done()
	m.mu.Lock()
	if m.sess != sess {
		m.mu.Unlock()
		return
	}
	m.sess, m.eng, m.state = nil, nil, StateIdle
	m.err = "The mock server stopped unexpectedly. See the activity log."
	m.mu.Unlock()
	sess.Stop()
}

// Stop shuts the mock down; it returns once the shutdown is under way.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return errBusy
	}
	sess := m.sess
	if sess == nil {
		m.mu.Unlock()
		return nil
	}
	m.busy, m.state, m.err = true, StateStopping, ""
	m.sess, m.eng = nil, nil
	m.mu.Unlock()

	go func() {
		sess.Stop()
		m.mu.Lock()
		m.busy, m.state = false, StateIdle
		m.mu.Unlock()
		m.addLog("info", "mock stopped")
	}()
	return nil
}

// Shutdown stops the mock and waits for it, for use when the UI process exits.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	sess := m.sess
	m.sess, m.eng, m.state = nil, nil, StateIdle
	m.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
}

// Idle reports whether nothing is running and nothing is starting, switching or stopping.
func (m *Manager) Idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == StateIdle && !m.busy && m.sess == nil
}

// Journal returns the request journal, newest first.
func (m *Manager) Journal() ([]JournalRow, error) {
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
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

func (m *Manager) platform() string { return m.newEngine().P.Platform }

// Proxy describes the Map Remote rules a lite project needs; full projects need none.
func (m *Manager) Proxy() ProxyView {
	p := m.newEngine().P
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
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
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
func (m *Manager) SetCustom(c engine.CustomResponse) (engine.CustomResponse, error) {
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		return c, errNotServing
	}
	return sess.SetCustomResponse(c)
}

// RemoveCustom puts the target's own answer back for that route.
func (m *Manager) RemoveCustom(id string) error {
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
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
	p := m.newEngine().P
	return proxyTargets{api: p.ProxyBaseURL, fbrc: p.FBRCURL, port: m.opts.ServerPort, lite: p.Lite()}
}

// trafficSummary counts the app's requests so far, how many found no stub, and how long the
// current session has been serving.
func (m *Manager) trafficSummary() (count, unmatched int, age time.Duration) {
	m.mu.Lock()
	since := m.since
	m.mu.Unlock()
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
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		return errNotServing
	}
	return sess.SetFBRC(context.Background(), values)
}

// ResetFBRC puts back the Remote Config the running target defines.
func (m *Manager) ResetFBRC() error {
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		return errNotServing
	}
	return sess.ResetFBRC(context.Background())
}
