package mockapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/mockruntime"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

type runtime struct {
	done   chan struct{}
	once   sync.Once
	onStop func()
}

func (*runtime) Activate(context.Context, string, map[string]string, []string) (bool, error) {
	return true, nil
}
func (*runtime) DeliverFBRC(context.Context, map[string]string) error { return nil }
func (*runtime) AddMapping(map[string]any) (string, error)            { return "", nil }
func (*runtime) RemoveMapping(string) error                           { return nil }
func (*runtime) Traffic() ([]wiremock.LoggedRequest, error)           { return nil, nil }
func (*runtime) Request(string) (*wiremock.LoggedRequest, error)      { return nil, nil }
func (r *runtime) Done() <-chan struct{}                              { return r.done }
func (r *runtime) Stop() {
	r.once.Do(func() {
		if r.onStop != nil {
			r.onStop()
		}
		close(r.done)
	})
}

type backend struct {
	start func(context.Context, *mockruntime.Target) (*mockruntime.Session, error)
}

func (*backend) ResolveMockTarget(args []string) (*mockruntime.Target, error) {
	return &mockruntime.Target{Name: args[0]}, nil
}
func (b *backend) StartMock(ctx context.Context, t *mockruntime.Target) (*mockruntime.Session, error) {
	return b.start(ctx, t)
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}
func TestShutdownCancelsAndWaitsStartupAndRejectsLatePublication(t *testing.T) {
	entered, cancelled, release, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := &runtime{done: make(chan struct{}), onStop: func() { close(cleaned) }}
	m := New(func(context.Context) (Backend, error) {
		return &backend{start: func(ctx context.Context, target *mockruntime.Target) (*mockruntime.Session, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return mockruntime.New(r, target, 1, ""), nil
		}}, nil
	})
	if err := m.Serve("one"); err != nil {
		t.Fatal(err)
	}
	if m.Idle() || m.View().State != Starting {
		t.Fatal("startup was not reserved synchronously")
	}
	if !errors.Is(m.Serve("two"), ErrBusy) {
		t.Fatal("concurrent serve admitted")
	}
	await(t, entered)
	shut := make(chan struct{})
	go func() { m.Shutdown(); close(shut) }()
	await(t, cancelled)
	select {
	case <-shut:
		t.Fatal("shutdown returned before startup finished")
	default:
	}
	close(release)
	await(t, shut)
	await(t, cleaned)
	if !m.Idle() || m.View().Session != nil {
		t.Fatal("late session published")
	}
	if !errors.Is(m.Serve("three"), ErrClosed) {
		t.Fatal("restart after shutdown admitted")
	}
}
func TestStartupFailureReleasesAdmission(t *testing.T) {
	m := New(func(context.Context) (Backend, error) { return nil, errors.New("cannot start") })
	if err := m.Serve("one"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if !m.Idle() || m.View().Error != "cannot start" {
		t.Fatalf("state=%+v", m.View())
	}
	m.Shutdown()
}
func TestStopKeepsBusyThroughCleanupAndAllowsReentrantSnapshots(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var m *Lifecycle
	r := &runtime{done: make(chan struct{}), onStop: func() { _ = m.View(); close(entered); <-release }}
	m = New(func(context.Context) (Backend, error) {
		return &backend{start: func(_ context.Context, target *mockruntime.Target) (*mockruntime.Session, error) {
			return mockruntime.New(r, target, 1, ""), nil
		}}, nil
	})
	_ = m.Serve("one")
	m.Wait()
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	await(t, entered)
	if m.Idle() || m.View().State != Stopping {
		t.Fatal("cleanup released admission early")
	}
	if !errors.Is(m.Serve("two"), ErrBusy) {
		t.Fatal("serve admitted during cleanup")
	}
	close(release)
	m.Wait()
	if !m.Idle() {
		t.Fatal("stop failed to idle")
	}
	m.Shutdown()
}
