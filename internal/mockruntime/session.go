// Package mockruntime owns an interactive session independently of execution orchestration.
package mockruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

var ErrStopped = errors.New("mock session is stopped")

type Target struct {
	Kind, Name, Description, Profile string
	Overrides                        []string
	FBRC                             map[string]string
}

func cloneTarget(t *Target) *Target {
	c := *t
	c.Overrides = append([]string(nil), t.Overrides...)
	c.FBRC = remoteconfig.Copy(t.FBRC)
	return &c
}

// Runtime supplies only the operations an interactive mock needs. Implementations own
// execution resources; the session serializes all mutations and cleanup.
type Runtime interface {
	Activate(context.Context, string, map[string]string, []string) (mappingsReset bool, err error)
	DeliverFBRC(context.Context, map[string]string) error
	AddMapping(map[string]any) (string, error)
	RemoveMapping(string) error
	Traffic() ([]wiremock.LoggedRequest, error)
	Request(string) (*wiremock.LoggedRequest, error)
	Done() <-chan struct{}
	Stop()
}
type Snapshot struct {
	Kind, Name, Description, Profile string
	Overrides                        []string
	FBRC, Defaults                   map[string]string
	Port                             int
	Device                           string
}
type Session struct {
	runtime Runtime
	port    int
	device  string
	opMu    sync.Mutex
	mu      sync.RWMutex
	target  *Target
	fbrc    map[string]string
	custom  []CustomResponse
	stopped bool
}

// New publishes an already activated runtime and copies the supplied target.
func New(runtime Runtime, target *Target, port int, device string) *Session {
	return &Session{runtime: runtime, target: cloneTarget(target), fbrc: remoteconfig.Copy(target.FBRC), port: port, device: device}
}
func (s *Session) Target() *Target { s.mu.RLock(); defer s.mu.RUnlock(); return cloneTarget(s.target) }
func (s *Session) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.target
	return Snapshot{Kind: t.Kind, Name: t.Name, Description: t.Description, Profile: t.Profile, Overrides: append([]string(nil), t.Overrides...), FBRC: remoteconfig.Copy(s.fbrc), Defaults: remoteconfig.Copy(t.FBRC), Port: s.port, Device: s.device}
}
func (s *Session) FBRC() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return remoteconfig.Copy(s.fbrc)
}
func (s *Session) Switch(ctx context.Context, target *Target) error {
	next := cloneTarget(target)
	clean, err := remoteconfig.Validate(next.FBRC)
	if err != nil {
		return err
	}
	next.FBRC = clean
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.stopped {
		return ErrStopped
	}
	reset, err := s.runtime.Activate(ctx, next.Profile, remoteconfig.Copy(clean), next.Overrides)
	if reset {
		s.mu.Lock()
		s.custom = nil
		s.mu.Unlock()
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.target = next
	s.fbrc = remoteconfig.Copy(clean)
	s.custom = nil
	s.mu.Unlock()
	return nil
}
func (s *Session) setFBRC(ctx context.Context, values map[string]string) error {
	if s.stopped {
		return ErrStopped
	}
	clean, err := remoteconfig.Validate(values)
	if err != nil {
		return err
	}
	if err := s.runtime.DeliverFBRC(ctx, remoteconfig.Copy(clean)); err != nil {
		return err
	}
	s.mu.Lock()
	s.fbrc = clean
	s.mu.Unlock()
	return nil
}
func (s *Session) SetFBRC(ctx context.Context, values map[string]string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.setFBRC(ctx, values)
}
func (s *Session) ResetFBRC(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.setFBRC(ctx, s.Target().FBRC)
}
func (s *Session) Stop() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !s.stopped {
		s.stopped = true
		s.runtime.Stop()
	}
}
func (s *Session) Done() <-chan struct{} { return s.runtime.Done() }
func (s *Session) Traffic() ([]wiremock.LoggedRequest, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.stopped {
		return nil, ErrStopped
	}
	return s.runtime.Traffic()
}
func (s *Session) Request(id string) (*wiremock.LoggedRequest, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.stopped {
		return nil, ErrStopped
	}
	return s.runtime.Request(id)
}
func (s *Session) Serve(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-s.Done():
		return fmt.Errorf("WireMock exited; see the activity log")
	}
}
