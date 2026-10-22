package web

import (
	"context"
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
	done  chan struct{}
	once  sync.Once
	event func()
}

func (r *managerRuntime) Activate(context.Context, string, map[string]string, []string) (bool, error) {
	r.event()
	return true, nil
}
func (*managerRuntime) DeliverFBRC(context.Context, map[string]string) error { return nil }
func (*managerRuntime) AddMapping(map[string]any) (string, error)            { return "custom", nil }
func (*managerRuntime) RemoveMapping(string) error                           { return nil }
func (*managerRuntime) Traffic() ([]wiremock.LoggedRequest, error)           { return nil, nil }
func (*managerRuntime) Request(string) (*wiremock.LoggedRequest, error)      { return nil, nil }
func (r *managerRuntime) Done() <-chan struct{}                              { return r.done }
func (r *managerRuntime) Stop()                                              { r.event(); r.once.Do(func() { close(r.done) }) }

type managerBackend struct{ runtime *managerRuntime }

func (*managerBackend) ResolveMockTarget(args []string) (*mockruntime.Target, error) {
	return &mockruntime.Target{Name: args[0], Description: "description", FBRC: map[string]string{"K": "default"}}, nil
}
func (b *managerBackend) StartMock(_ context.Context, t *mockruntime.Target) (*mockruntime.Session, error) {
	return mockruntime.New(b.runtime, t, 18080, ""), nil
}
func TestManagerLifecycleEventsCanLogAndReadState(t *testing.T) {
	m := &Manager{}
	r := &managerRuntime{done: make(chan struct{}), event: func() { m.addLog("info", "event"); _ = m.State() }}
	m.life = mockapp.New(func(context.Context) (mockapp.Backend, error) { return &managerBackend{runtime: r}, nil })
	if err := m.Serve("first"); err != nil {
		t.Fatal(err)
	}
	m.life.Wait()
	if got := m.State(); got.Serving == nil || got.Serving.Description != "description" || got.Serving.DefaultFBRC["K"] != "default" {
		t.Fatalf("state=%+v", got)
	}
	if err := m.Serve("second"); err != nil {
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
