package engine

import (
	"fmt"
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
	// Scenario and State make the response apply only while that WireMock scenario is in that
	// state, and the state machine keep running: editing the answer before a payment leaves the
	// answer after it to change as before. Both empty means every state.
	Scenario string `json:"scenario,omitempty"`
	State    string `json:"state,omitempty"`
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
	if (c.Scenario == "") != (c.State == "") {
		return fmt.Errorf("a scenario and its state go together")
	}
	if len(c.Scenario) > 200 || len(c.State) > 200 || strings.ContainsAny(c.Scenario+c.State, "\r\n\t") {
		return fmt.Errorf("scenario and state must be short single-line names")
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

func (c *CustomResponse) key() string {
	if c.Scenario != "" {
		return c.Method + " " + c.Path + " [" + c.Scenario + ": " + c.State + "]"
	}
	return c.Method + " " + c.Path
}

func (c *CustomResponse) mapping() map[string]any {
	headers := map[string]any{}
	for k, v := range c.Headers {
		headers[k] = v
	}
	response := map[string]any{"status": c.Status, "headers": headers, "body": c.Body}
	if c.DelayMs > 0 {
		response["fixedDelayMilliseconds"] = c.DelayMs
	}
	m := map[string]any{
		"name":     "custom/" + c.key(),
		"priority": 1,
		"request":  map[string]any{"method": c.Method, "urlPath": c.Path},
		"response": response,
	}
	if c.Scenario != "" {
		m["scenarioName"], m["requiredScenarioState"] = c.Scenario, c.State
	}
	return m
}

// SetCustomResponse serves c for its method and path, replacing an earlier custom response for
// the same route. The returned copy carries WireMock's id.
func (m *MockSession) SetCustomResponse(c CustomResponse) (CustomResponse, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	m.ovMu.Lock()
	defer m.ovMu.Unlock()
	id, err := m.Run.WM.AddMapping(c.mapping())
	if err != nil {
		return c, err
	}
	for i, old := range m.custom {
		if old.key() == c.key() {
			if err := m.Run.WM.RemoveMapping(old.ID); err != nil {
				_ = m.Run.WM.RemoveMapping(id)
				return c, err
			}
			m.custom = slices.Delete(m.custom, i, i+1)
			break
		}
	}
	c.ID = id
	m.custom = append(m.custom, c)
	return c, nil
}

// RemoveCustomResponse restores the served target's own answer for that route.
func (m *MockSession) RemoveCustomResponse(id string) error {
	m.ovMu.Lock()
	defer m.ovMu.Unlock()
	i := slices.IndexFunc(m.custom, func(c CustomResponse) bool { return c.ID == id })
	if i < 0 {
		return fmt.Errorf("no custom response with id %q", id)
	}
	if err := m.Run.WM.RemoveMapping(id); err != nil {
		return err
	}
	m.custom = slices.Delete(m.custom, i, i+1)
	return nil
}

// CustomResponses lists what is currently layered over the target, oldest first.
func (m *MockSession) CustomResponses() []CustomResponse {
	m.ovMu.Lock()
	defer m.ovMu.Unlock()
	return append([]CustomResponse{}, m.custom...)
}
