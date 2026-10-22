// Package mockapp coordinates the interactive mock lifecycle shared by frontends.
package mockapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/feernandooff/mockstro/internal/mockruntime"
)

type State string

const (
	Idle     State = "idle"
	Starting State = "starting"
	Serving  State = "serving"
	Stopping State = "stopping"
)

var (
	ErrBusy       = errors.New("another start, switch or stop is still in progress")
	ErrNotServing = errors.New("no mock is running; start one first")
	ErrClosed     = errors.New("mock lifecycle is shut down")
)

// Backend resolves targets and creates sessions using frontend-supplied dependencies.
type Backend interface {
	ResolveMockTarget([]string) (*mockruntime.Target, error)
	StartMock(context.Context, *mockruntime.Target) (*mockruntime.Session, error)
}
type Factory func(context.Context) (Backend, error)
type View struct {
	State   State
	Error   string
	Session *mockruntime.Session
	Since   time.Time
	Busy    bool
}
type Lifecycle struct {
	mu           sync.Mutex
	factory      Factory
	state        State
	err          string
	session      *mockruntime.Session
	backend      Backend
	since        time.Time
	pending      chan struct{}
	cancel       context.CancelFunc
	closed       bool
	shutdownDone chan struct{}
}

func New(factory Factory) *Lifecycle { return &Lifecycle{factory: factory, state: Idle} }
func (m *Lifecycle) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return View{m.state, m.err, m.session, m.since, m.pending != nil}
}
func (m *Lifecycle) Idle() bool { v := m.View(); return v.State == Idle && !v.Busy && v.Session == nil }
func (m *Lifecycle) begin() (context.Context, chan struct{}, error) {
	if m.closed {
		return nil, nil, ErrClosed
	}
	if m.pending != nil {
		return nil, nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	done := make(chan struct{})
	m.pending = done
	m.cancel = cancel
	m.err = ""
	return ctx, done, nil
}

// Serve reserves admission before returning, including target resolution and startup.
func (m *Lifecycle) Serve(ref string) error {
	m.mu.Lock()
	ctx, done, err := m.begin()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	session, backend := m.session, m.backend
	starting := session == nil
	if session == nil {
		m.state = Starting
	}
	m.mu.Unlock()
	go func() {
		var err error
		if session == nil {
			backend, err = m.factory(ctx)
		}
		var target *mockruntime.Target
		if err == nil {
			target, err = backend.ResolveMockTarget([]string{ref})
		}
		if err == nil {
			if session == nil {
				session, err = backend.StartMock(ctx, target)
			} else {
				err = session.Switch(ctx, target)
			}
		}
		m.mu.Lock()
		closing := m.closed || ctx.Err() != nil
		if closing {
			m.session = nil
			m.backend = nil
			m.state = Idle
			if err == nil && !m.closed {
				err = ctx.Err()
			}
		}
		// Never publish a session after cancellation. Shutdown waits through its cleanup.
		if !closing && err == nil {
			m.session, m.backend = session, backend
			if m.state == Starting {
				m.since = time.Now()
			}
			m.state = Serving
		}
		if err != nil {
			m.err = err.Error()
			if m.session == nil {
				m.state = Idle
			}
		}
		m.mu.Unlock()
		if closing && session != nil {
			session.Stop()
		}
		m.mu.Lock()
		m.cancel()
		m.pending = nil
		m.cancel = nil
		close(done)
		m.mu.Unlock()
		if starting && !closing && err == nil {
			go m.watch(session)
		}
	}()
	return nil
}
func (m *Lifecycle) watch(session *mockruntime.Session) {
	<-session.Done()
	m.mu.Lock()
	if m.session != session || m.closed {
		m.mu.Unlock()
		return
	}
	// Reserve cleanup so workspace admission stays closed until resources are released.
	if m.pending != nil {
		done := m.pending
		m.mu.Unlock()
		<-done
		go m.watch(session)
		return
	}
	done := make(chan struct{})
	m.pending = done
	m.session = nil
	m.backend = nil
	m.state = Stopping
	m.err = "The mock server stopped unexpectedly. See the activity log."
	m.mu.Unlock()
	session.Stop()
	m.mu.Lock()
	m.state = Idle
	m.pending = nil
	close(done)
	m.mu.Unlock()
}
func (m *Lifecycle) Stop() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	if m.pending != nil {
		m.mu.Unlock()
		return ErrBusy
	}
	session := m.session
	if session == nil {
		m.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	m.pending = done
	m.state = Stopping
	m.err = ""
	m.session = nil
	m.backend = nil
	m.mu.Unlock()
	go func() {
		session.Stop()
		m.mu.Lock()
		m.state = Idle
		m.pending = nil
		close(done)
		m.mu.Unlock()
	}()
	return nil
}

// Wait waits for the operation admitted before this call to finish.
func (m *Lifecycle) Wait() {
	m.mu.Lock()
	done := m.pending
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Shutdown permanently closes admission, cancels startup, waits for in-flight work and cleanup.
func (m *Lifecycle) Shutdown() {
	m.mu.Lock()
	if m.closed {
		done := m.shutdownDone
		m.mu.Unlock()
		<-done
		return
	}
	m.closed = true
	m.shutdownDone = make(chan struct{})
	done := m.pending
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	if done != nil {
		<-done
	}
	m.mu.Lock()
	session := m.session
	m.session = nil
	m.backend = nil
	m.mu.Unlock()
	if session != nil {
		session.Stop()
	}
	m.mu.Lock()
	m.state = Idle
	close(m.shutdownDone)
	m.mu.Unlock()
}
func (m *Lifecycle) Session() (*mockruntime.Session, error) {
	v := m.View()
	if v.Session == nil {
		return nil, ErrNotServing
	}
	return v.Session, nil
}
