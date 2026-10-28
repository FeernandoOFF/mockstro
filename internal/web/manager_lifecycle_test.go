package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/mockapp"
	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

type managerRuntime struct {
	done       chan struct{}
	once       sync.Once
	event      func()
	deliver    func(map[string]string) error
	addMapping func(map[string]any) (string, error)
}

func (r *managerRuntime) Activate(context.Context, string, map[string]string, []string) (bool, error) {
	r.event()
	return true, nil
}
func (r *managerRuntime) DeliverFBRC(_ context.Context, values map[string]string) error {
	if r.deliver != nil {
		return r.deliver(values)
	}
	return nil
}
func (r *managerRuntime) AddMapping(mapping map[string]any) (string, error) {
	if r.addMapping != nil {
		return r.addMapping(mapping)
	}
	return "custom", nil
}
func (*managerRuntime) RemoveMapping(string) error                      { return nil }
func (*managerRuntime) Traffic() ([]wiremock.LoggedRequest, error)      { return nil, nil }
func (*managerRuntime) Request(string) (*wiremock.LoggedRequest, error) { return nil, nil }
func (r *managerRuntime) Done() <-chan struct{}                         { return r.done }
func (r *managerRuntime) Stop()                                         { r.event(); r.once.Do(func() { close(r.done) }) }

type managerBackend struct {
	runtime *managerRuntime
	kind    string
}

func (b *managerBackend) ResolveMockTarget(args []string) (*mockruntime.Target, error) {
	return &mockruntime.Target{Kind: b.kind, Name: args[0], Description: "description", FBRC: map[string]string{"K": "default"}}, nil
}
func (b *managerBackend) StartMock(_ context.Context, t *mockruntime.Target) (*mockruntime.Session, error) {
	return mockruntime.New(b.runtime, t, 18080, ""), nil
}
func TestManagerLifecycleEventsCanLogAndReadState(t *testing.T) {
	m := &Manager{}
	r := &managerRuntime{done: make(chan struct{}), event: func() { m.addLog("info", "event"); _ = m.State() }}
	m.life = mockapp.New(func(context.Context) (mockapp.Backend, error) { return &managerBackend{runtime: r}, nil })
	if err := m.Serve("first", nil, nil); err != nil {
		t.Fatal(err)
	}
	m.life.Wait()
	if got := m.State(); got.Serving == nil || got.Serving.Description != "description" || got.Serving.DefaultFBRC["K"] != "default" {
		t.Fatalf("state=%+v", got)
	}
	if err := m.Serve("second", nil, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.life.Wait(); m.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle event deadlocked State or logging")
	}
	if !m.Idle() {
		t.Fatal("shutdown did not idle")
	}
}

func TestManagerConfigReadsSelectedProjectWithoutEngine(t *testing.T) {
	android := &project.Project{Platform: "android", Integration: "full", ProxyBaseURL: "https://android.test", FBRCURL: "https://firebase.android.test"}
	ios := &project.Project{Platform: "ios", Integration: "lite", ProxyBaseURL: "https://ios.test", FBRCURL: "https://firebase.ios.test"}
	p := &project.Project{Platform: "android", Targets: map[string]*project.Project{"android": android, "ios": ios}}
	m := NewManager(p, engine.Options{Platform: "ios", ServerPort: 18080}, nil)
	defer m.Shutdown()
	if m.platform() != "ios" {
		t.Fatal("wrong selected platform")
	}
	proxy := m.Proxy()
	if !proxy.Needed || len(proxy.Rules) != 2 || proxy.Rules[0].Match != "https://ios.test/*" {
		t.Fatalf("wrong proxy rules: %+v", proxy)
	}
	targets := m.proxyTargets()
	if targets.api != ios.ProxyBaseURL || targets.fbrc != ios.FBRCURL || !targets.lite {
		t.Fatalf("wrong health targets: %+v", targets)
	}
}

func TestServeAppliesPreviewDraftsThroughWorkspace(t *testing.T) {
	s, _ := testServer(t)
	m := mgr(s)
	var delivered map[string]string
	var mapping map[string]any
	activations := 0
	r := &managerRuntime{
		done:       make(chan struct{}),
		event:      func() { activations++ },
		deliver:    func(values map[string]string) error { delivered = values; return nil },
		addMapping: func(value map[string]any) (string, error) { mapping = value; return "edited", nil },
	}
	m.life = mockapp.New(func(context.Context) (mockapp.Backend, error) { return &managerBackend{runtime: r}, nil })
	t.Cleanup(s.ws.Shutdown)
	h := map[string]string{tokenHeader: s.token, "Content-Type": "application/json"}
	for _, value := range []string{"before", "after"} {
		body, err := json.Marshal(map[string]any{
			"kind": "scenario", "ref": "member-survey", "fbrc": map[string]string{"K": value},
			"custom": []mockruntime.CustomResponse{{Method: "GET", Path: "/bill", Status: 201, Body: value, Scenario: "payment", State: "Started"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if rec := do(s, "POST", "/api/serve", goodHost, h, string(body)); rec.Code != http.StatusAccepted {
			t.Fatalf("serve: %d %s", rec.Code, rec.Body)
		}
		m.life.Wait()
		state := m.State()
		if state.Error != "" || state.Serving == nil || state.Serving.FBRC["K"] != value || state.Serving.DefaultFBRC["K"] != "default" {
			t.Fatalf("preview values or defaults lost: %+v", state)
		}
		if delivered["K"] != value || mapping["scenarioName"] != "payment" || mapping["requiredScenarioState"] != "Started" || mapping["response"].(map[string]any)["body"] != value {
			t.Fatalf("preview did not reach runtime: values=%v mapping=%v", delivered, mapping)
		}
		if len(state.Custom) != 1 || state.Custom[0].Body != value {
			t.Fatalf("preview response not published: %+v", state.Custom)
		}
	}
	if activations != 1 {
		t.Fatalf("expected one switch on the same runtime, got %d", activations)
	}
}

func TestServingFlowPublishesItsNameAndProjectRelativePath(t *testing.T) {
	s, p := testServer(t)
	m := mgr(s)
	r := &managerRuntime{done: make(chan struct{}), event: func() {}}
	m.life = mockapp.New(func(context.Context) (mockapp.Backend, error) { return &managerBackend{runtime: r, kind: "flow"}, nil })
	t.Cleanup(s.ws.Shutdown)
	ref := p.MaestroRoot + "/flows/survey/shown.yaml"
	if err := s.ws.Serve(ref, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.life.Wait()
	v := m.State()
	if v.Serving == nil || v.Serving.Title != "Survey shown" || v.Serving.Path != ".maestro/flows/survey/shown.yaml" || v.Serving.Name != ref {
		t.Fatalf("flow metadata: %+v", v)
	}
}
