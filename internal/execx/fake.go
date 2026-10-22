package execx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Fake answers commands from a handler and records every call.
type Fake struct {
	mu           sync.Mutex
	Calls        []string
	Handler      func(c Cmd) (string, error)
	Paths        map[string]string
	StartHandler func(Cmd, bool) (Process, error)
}

func (f *Fake) record(c Cmd) {
	f.mu.Lock()
	f.Calls = append(f.Calls, c.String())
	f.mu.Unlock()
}

func (f *Fake) Output(_ context.Context, c Cmd) (string, error) {
	f.record(c)
	if f.Handler == nil {
		return "", fmt.Errorf("fake: unexpected %s", c)
	}
	out, err := f.Handler(c)
	return strings.TrimSpace(out), err
}

func (f *Fake) Run(ctx context.Context, c Cmd) error {
	out, err := f.Output(ctx, c)
	if c.Stdout != nil && out != "" {
		_, _ = c.Stdout.Write([]byte(out + "\n"))
	}
	return err
}

func (f *Fake) LookPath(name string) (string, error) {
	if p, ok := f.Paths[name]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

func (f *Fake) Start(c Cmd, detach bool) (Process, error) {
	f.record(c)
	if f.StartHandler == nil {
		return nil, fmt.Errorf("fake: unexpected process %s", c)
	}
	return f.StartHandler(c, detach)
}
