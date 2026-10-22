// Package workspacecoord serializes admission to operations that use a checkout.
package workspacecoord

import (
	"errors"
	"sync"
)

var (
	ErrBusy   = errors.New("another workspace operation is still running")
	ErrClosed = errors.New("the workspace is shutting down")
)

// Coordinator reserves a workspace for one operation at a time. Its zero value
// is ready for use. Git mutations hold the reservation until the command and its
// final status read finish; Open holds it through loading and manager replacement.
//
// Lifecycle contract: call Serve and Stop under a reservation. They must mark
// themselves busy synchronously before returning, and Idle must remain false
// throughout async startup, switching, stopping, and the running session. Under
// the next reservation, checking Idle is therefore atomic with Git admission.
// Shutdown must wait for any accepted async work before returning. Callers must
// route every lifecycle admission through the same Coordinator.
type Coordinator struct {
	mu     sync.Mutex
	closed bool
}

// Reserve fails immediately when an operation is already in progress. The caller
// must release its reservation exactly once after all protected work finishes.
func (c *Coordinator) Reserve() (release func(), err error) {
	if !c.mu.TryLock() {
		return nil, ErrBusy
	}
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	return c.mu.Unlock, nil
}

// Shutdown waits for the current reservation, then permanently closes admission.
// cleanup runs at most once, while admission is still locked.
func (c *Coordinator) Shutdown(cleanup func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	cleanup()
}
