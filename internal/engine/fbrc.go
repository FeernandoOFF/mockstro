package engine

import (
	"context"
	"sync"

	"github.com/feernandooff/mockstro/internal/remoteconfig"
	"github.com/feernandooff/mockstro/internal/wiremock"
)

// RemoteConfigDelivery is the single transport decision for full activation and live edits.
// Call Mappings when constructing the complete mapping set; call Deliver after replacement
// only when UsesDeviceOverride is true (HTTP values are already in the complete set).
func (r *Run) RemoteConfigDelivery() remoteconfig.Delivery {
	if r.Device != "" && r.overrideAuto {
		return remoteconfig.Device{Write: func(ctx context.Context, values map[string]string) error {
			if err := r.e.Plat.WriteOverride(ctx, r.Device, r.override(values)); err != nil {
				return err
			}
			r.overrideApplied = true
			return nil
		}}
	}
	return remoteconfig.HTTP{Writer: r.WM}
}

type mockRuntime struct {
	run  *Run
	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

func newMockRuntime(run *Run) *mockRuntime {
	r := &mockRuntime{run: run, done: make(chan struct{})}
	r.watch(run.WM)
	return r
}
func (r *mockRuntime) watch(wm *wiremock.Server) {
	if wm == nil {
		return
	}
	go func() {
		<-wm.Done()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.run.WM == wm {
			r.once.Do(func() { close(r.done) })
		}
	}()
}

func (r *mockRuntime) Activate(ctx context.Context, p string, v map[string]string, o []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.run.WM
	reset, err := r.run.activate(ctx, p, v, o)
	if r.run.WM == nil {
		r.once.Do(func() { close(r.done) })
	}
	if r.run.WM != old {
		r.watch(r.run.WM)
	}
	return reset, err
}
func (r *mockRuntime) DeliverFBRC(ctx context.Context, v map[string]string) error {
	return r.run.RemoteConfigDelivery().Deliver(ctx, v)
}
func (r *mockRuntime) AddMapping(v map[string]any) (string, error) { return r.run.WM.AddMapping(v) }
func (r *mockRuntime) RemoveMapping(id string) error               { return r.run.WM.RemoveMapping(id) }
func (r *mockRuntime) Traffic() ([]wiremock.LoggedRequest, error)  { return r.run.WM.Traffic(false) }
func (r *mockRuntime) Request(id string) (*wiremock.LoggedRequest, error) {
	return r.run.WM.Request(id)
}
func (r *mockRuntime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run.Cleanup()
	r.once.Do(func() { close(r.done) })
}
func (r *mockRuntime) Done() <-chan struct{} { return r.done }
