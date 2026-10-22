package mockruntime

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const (
	maxCustomBody    = 5 << 20
	maxCustomDelayMs = 60_000
)

var (
	customMethods    = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	headerNamePolicy = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
)

// CustomResponse is a one-off answer for a single route, added on top of the served target.
// It lives only in the running WireMock: switching target or stopping discards it.
type CustomResponse struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	DelayMs int               `json:"delayMs"`
}

// Validate reports the first problem in terms a tester can act on.
func (c *CustomResponse) Validate() error {
	if !slices.Contains(customMethods, c.Method) {
		return fmt.Errorf("method must be one of %s", strings.Join(customMethods, ", "))
	}
	if !strings.HasPrefix(c.Path, "/") || len(c.Path) > 2048 || strings.ContainsAny(c.Path, "?# \t\r\n") {
		return fmt.Errorf("path must start with / and contain no query string or spaces (got %q)", c.Path)
	}
	if c.Status < 100 || c.Status > 599 {
		return fmt.Errorf("status must be between 100 and 599")
	}
	if c.DelayMs < 0 || c.DelayMs > maxCustomDelayMs {
		return fmt.Errorf("delay must be between 0 and %d ms", maxCustomDelayMs)
	}
	if len(c.Body) > maxCustomBody {
		return fmt.Errorf("body is larger than %d MB", maxCustomBody>>20)
	}
	for k, v := range c.Headers {
		if !headerNamePolicy.MatchString(k) {
			return fmt.Errorf("header name %q is not valid", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("header %q must be a single line", k)
		}
	}
	return nil
}

func (c *CustomResponse) key() string { return c.Method + " " + c.Path }

func (c *CustomResponse) mapping() map[string]any {
	headers := map[string]any{}
	for k, v := range c.Headers {
		headers[k] = v
	}
	response := map[string]any{"status": c.Status, "headers": headers, "body": c.Body}
	if c.DelayMs > 0 {
		response["fixedDelayMilliseconds"] = c.DelayMs
	}
	return map[string]any{
		"name":     "custom/" + c.key(),
		"priority": 1,
		"request":  map[string]any{"method": c.Method, "urlPath": c.Path},
		"response": response,
	}
}

// SetCustomResponse serves c for its method and path, replacing an earlier custom response for
// the same route. The returned copy carries WireMock's id.
func (m *Session) SetCustomResponse(c CustomResponse) (CustomResponse, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.stopped {
		return c, ErrStopped
	}
	c.Headers = maps.Clone(c.Headers)
	id, err := m.runtime.AddMapping(c.mapping())
	if err != nil {
		return c, err
	}
	for i, old := range m.custom {
		if old.key() == c.key() {
			if err := m.runtime.RemoveMapping(old.ID); err != nil {
				_ = m.runtime.RemoveMapping(id)
				return c, err
			}
			m.mu.Lock()
			m.custom = slices.Delete(m.custom, i, i+1)
			m.mu.Unlock()
			break
		}
	}
	c.ID = id
	m.mu.Lock()
	m.custom = append(m.custom, c)
	m.mu.Unlock()
	c.Headers = maps.Clone(c.Headers)
	return c, nil
}

// RemoveCustomResponse restores the served target's own answer for that route.
func (m *Session) RemoveCustomResponse(id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.stopped {
		return ErrStopped
	}
	i := slices.IndexFunc(m.custom, func(c CustomResponse) bool { return c.ID == id })
	if i < 0 {
		return fmt.Errorf("no custom response with id %q", id)
	}
	if err := m.runtime.RemoveMapping(id); err != nil {
		return err
	}
	m.mu.Lock()
	m.custom = slices.Delete(m.custom, i, i+1)
	m.mu.Unlock()
	return nil
}

// CustomResponses lists what is currently layered over the target, oldest first.
func (m *Session) CustomResponses() []CustomResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]CustomResponse{}, m.custom...)
	for i := range out {
		out[i].Headers = maps.Clone(out[i].Headers)
	}
	return out
}
