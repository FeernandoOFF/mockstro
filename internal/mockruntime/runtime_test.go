package mockruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

type testRuntime struct {
	wm       *wiremock.Server
	activate func(context.Context, string, map[string]string, []string) error
	deliver  func(context.Context, map[string]string) error
	stop     func()
	done     chan struct{}
}

func (r *testRuntime) Activate(ctx context.Context, p string, v map[string]string, o []string) (bool, error) {
	if r.activate != nil {
		err := r.activate(ctx, p, v, o)
		return err == nil, err
	}
	return true, nil
}
func (r *testRuntime) DeliverFBRC(ctx context.Context, v map[string]string) error {
	if r.deliver != nil {
		return r.deliver(ctx, v)
	}
	return (remoteconfig.HTTP{Writer: r.wm}).Deliver(ctx, v)
}
func (r *testRuntime) AddMapping(v map[string]any) (string, error)        { return r.wm.AddMapping(v) }
func (r *testRuntime) RemoveMapping(id string) error                      { return r.wm.RemoveMapping(id) }
func (r *testRuntime) Traffic() ([]wiremock.LoggedRequest, error)         { return r.wm.Traffic(false) }
func (r *testRuntime) Request(id string) (*wiremock.LoggedRequest, error) { return r.wm.Request(id) }
func (r *testRuntime) Done() <-chan struct{}                              { return r.done }
func (r *testRuntime) Stop() {
	if r.stop != nil {
		r.stop()
	}
}

func TestDeliveryFailureDoesNotPublishValuesOrTarget(t *testing.T) {
	fail := errors.New("delivery failed")
	r := &testRuntime{deliver: func(context.Context, map[string]string) error { return fail }, activate: func(context.Context, string, map[string]string, []string) error { return fail }}
	s := New(r, &Target{Name: "old", FBRC: map[string]string{"K": "old"}}, 1, "")
	if !errors.Is(s.SetFBRC(context.Background(), map[string]string{"K": "new"}), fail) {
		t.Fatal("lost delivery error")
	}
	if !errors.Is(s.Switch(context.Background(), &Target{Name: "new", FBRC: map[string]string{"K": "new"}}), fail) {
		t.Fatal("lost activation error")
	}
	if got := s.Snapshot(); got.Name != "old" || got.FBRC["K"] != "old" || got.Defaults["K"] != "old" {
		t.Fatalf("published failed update: %+v", got)
	}
}
func TestSwitchResetAndStopAreSerialized(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var delivered string
	var stopped int
	r := &testRuntime{activate: func(context.Context, string, map[string]string, []string) error {
		close(entered)
		<-release
		return nil
	}, deliver: func(_ context.Context, v map[string]string) error { delivered = v["K"]; return nil }, stop: func() { stopped++ }}
	s := New(r, &Target{Name: "old", FBRC: map[string]string{"K": "old"}}, 1, "")
	switched := make(chan error, 1)
	go func() {
		switched <- s.Switch(context.Background(), &Target{Name: "new", FBRC: map[string]string{"K": "new"}})
	}()
	<-entered
	reset := make(chan error, 1)
	go func() { reset <- s.ResetFBRC(context.Background()) }()
	if got := s.Snapshot(); got.Name != "old" {
		t.Fatal("published before delivery")
	}
	close(release)
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if err := <-reset; err != nil {
		t.Fatal(err)
	}
	if delivered != "new" {
		t.Fatalf("reset used stale defaults: %s", delivered)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(s.Stop)
	}
	wg.Wait()
	if stopped != 1 {
		t.Fatalf("cleanup ran %d times", stopped)
	}
	if err := s.SetFBRC(context.Background(), nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("edit after stop: %v", err)
	}
}
func TestSnapshotsAndCustomCopiesDoNotDeadlockEventCallbacks(t *testing.T) {
	s, _ := newFakeSession(t)
	custom, err := s.SetCustomResponse(CustomResponse{Method: "GET", Path: "/x", Status: 200, Headers: map[string]string{"X-Key": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	custom.Headers["X-Key"] = "caller"
	list := s.CustomResponses()
	list[0].Headers["X-Key"] = "reader"
	if s.CustomResponses()[0].Headers["X-Key"] != "old" {
		t.Fatal("custom headers alias caller")
	}
	r := s.runtime.(*testRuntime)
	r.activate = func(context.Context, string, map[string]string, []string) error {
		_ = s.Snapshot()
		_ = s.CustomResponses()
		return nil
	}
	r.stop = func() { _ = s.Snapshot(); _ = s.CustomResponses() }
	done := make(chan struct{})
	go func() { _ = s.Switch(context.Background(), &Target{Name: "next"}); s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event snapshot deadlocked")
	}
}
