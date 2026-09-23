package fixtures

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/trun/internal/ojson"
)

// Verification asserts how often a named stub was matched during one flow.
type Verification struct {
	Stub    string
	Exactly *int
	AtLeast *int
	AtMost  *int
}

func (v Verification) Bounds() (lo, hi int) {
	if v.Exactly != nil {
		return *v.Exactly, *v.Exactly
	}
	lo, hi = 0, 2147483647
	if v.AtLeast != nil {
		lo = *v.AtLeast
	}
	if v.AtMost != nil {
		hi = *v.AtMost
	}
	return
}

func (v Verification) Expectation() string {
	switch {
	case v.Exactly != nil:
		return fmt.Sprintf("exactly %d", *v.Exactly)
	case v.AtLeast != nil && v.AtMost != nil:
		return fmt.Sprintf("between %d and %d", *v.AtLeast, *v.AtMost)
	case v.AtLeast != nil:
		return fmt.Sprintf("at least %d", *v.AtLeast)
	default:
		lo, hi := v.Bounds()
		_ = lo
		return fmt.Sprintf("at most %d", hi)
	}
}

// Header is the first YAML document of a flow or scenario.
type Header struct {
	Path          string
	Name          string
	Description   string
	Tags          []string
	Profile       string
	Overrides     []string
	Verifications []Verification
	FBRC          map[string]string
	HasSteps      bool

	raw map[string]any
}

// FBRCKeys returns the FBRC keys sorted for stable output.
func (h *Header) FBRCKeys() []string {
	keys := make([]string, 0, len(h.FBRC))
	for k := range h.FBRC {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var docSep = regexp.MustCompile(`(?m)^---[ \t]*$`)

// splitDocuments returns the header text and the steps text (raw, untouched).
func splitDocuments(data []byte) (header, body []byte, hasSep bool) {
	text := data
	// A leading document marker belongs to the header, not the split.
	if loc := docSep.FindIndex(text); loc != nil && len(bytes.TrimSpace(text[:loc[0]])) == 0 {
		text = text[loc[1]:]
	}
	loc := docSep.FindIndex(text)
	if loc == nil {
		return text, nil, false
	}
	body = text[loc[1]:]
	body = bytes.TrimPrefix(body, []byte("\n"))
	return text[:loc[0]], body, true
}

func ParseHeader(path string) (*Header, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	headerText, body, _ := splitDocuments(data)
	raw := map[string]any{}
	if err := yaml.Unmarshal(headerText, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	h := &Header{Path: path, raw: raw}
	if len(bytes.TrimSpace(body)) > 0 {
		var steps any
		if err := yaml.Unmarshal(body, &steps); err != nil {
			return nil, fmt.Errorf("invalid YAML steps: %w", err)
		}
		h.HasSteps = steps != nil
	}
	h.Name, _ = raw["name"].(string)
	h.Description, _ = raw["description"].(string)
	switch t := raw["tags"].(type) {
	case []any:
		for _, v := range t {
			h.Tags = append(h.Tags, fmt.Sprint(v))
		}
	case string:
		h.Tags = []string{t}
	}
	h.Profile, _ = h.prop("apiProfile").(string)
	if list, ok := h.prop("apiOverrides").([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				h.Overrides = append(h.Overrides, s)
			}
		}
	}
	if m, ok := h.prop("fbrcOverrides").(map[string]any); ok {
		h.FBRC = map[string]string{}
		for k, v := range m {
			if s, ok := v.(string); ok {
				h.FBRC[k] = s
			}
		}
	}
	if list, ok := h.prop("apiVerifications").([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			v := Verification{}
			v.Stub, _ = m["stub"].(string)
			v.Exactly = intPtr(m, "exactly")
			v.AtLeast = intPtr(m, "atLeast")
			v.AtMost = intPtr(m, "atMost")
			h.Verifications = append(h.Verifications, v)
		}
	}
	return h, nil
}

func intPtr(m map[string]any, k string) *int {
	v, ok := m[k]
	if !ok {
		return nil
	}
	if i, ok := v.(int); ok {
		return &i
	}
	return nil
}

// prop reads properties.<key>, also accepting the flattened "properties.<key>" spelling.
func (h *Header) prop(key string) any {
	if props, ok := h.raw["properties"].(map[string]any); ok {
		if v, ok := props[key]; ok && v != nil {
			return v
		}
	}
	return h.raw["properties."+key]
}

// Validate applies the header rules test.sh enforced in validate_flow / validate_scenario.
func (h *Header) validateProperties(rel string) error {
	if h.Profile == "" {
		return fmt.Errorf("missing properties.apiProfile in %s", rel)
	}
	if v := h.prop("apiOverrides"); v != nil {
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("properties.apiOverrides must be a string array in %s", rel)
		}
		for _, item := range list {
			if s, ok := item.(string); !ok || s == "" {
				return fmt.Errorf("properties.apiOverrides must be a string array in %s", rel)
			}
		}
	}
	if v := h.prop("fbrcOverrides"); v != nil {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("properties.fbrcOverrides must contain non-empty keys and string values in %s", rel)
		}
		for k, val := range m {
			if _, ok := val.(string); !ok || k == "" {
				return fmt.Errorf("properties.fbrcOverrides must contain non-empty keys and string values in %s (values are always strings; quote %q)", rel, k)
			}
		}
	}
	return nil
}

func (h *Header) validateVerifications(rel string) error {
	v := h.prop("apiVerifications")
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	bad := fmt.Errorf("properties.apiVerifications has invalid entries in %s", rel)
	if !ok {
		return bad
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return bad
		}
		if s, ok := m["stub"].(string); !ok || s == "" {
			return bad
		}
		for k := range m {
			switch k {
			case "stub", "exactly", "atLeast", "atMost":
			default:
				return bad
			}
		}
		isCount := func(k string) bool {
			x, present := m[k]
			if !present {
				return true
			}
			i, ok := x.(int)
			return ok && i >= 0
		}
		_, hasE := m["exactly"]
		_, hasL := m["atLeast"]
		_, hasM := m["atMost"]
		if hasE {
			if hasL || hasM || !isCount("exactly") {
				return bad
			}
			continue
		}
		if !hasL && !hasM || !isCount("atLeast") || !isCount("atMost") {
			return bad
		}
		lo, _ := m["atLeast"].(int)
		hi := 2147483647
		if hasM {
			hi, _ = m["atMost"].(int)
		}
		if lo > hi {
			return bad
		}
	}
	return nil
}

// SanitizedFlow strips runner-only properties so Maestro sees a plain flow. The steps are kept
// byte-for-byte; only the header is re-emitted.
func SanitizedFlow(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	headerText, body, _ := splitDocuments(data)
	var node yaml.Node
	if err := yaml.Unmarshal(headerText, &node); err != nil {
		return nil, err
	}
	if len(node.Content) > 0 && node.Content[0].Kind == yaml.MappingNode {
		m := node.Content[0]
		kept := m.Content[:0:0]
		for i := 0; i+1 < len(m.Content); i += 2 {
			switch m.Content[i].Value {
			case "properties", "properties.apiProfile", "properties.apiOverrides", "properties.apiVerifications", "properties.fbrcOverrides":
				continue
			}
			kept = append(kept, m.Content[i], m.Content[i+1])
		}
		m.Content = kept
	}
	var out bytes.Buffer
	if len(node.Content) > 0 {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&node); err != nil {
			return nil, err
		}
		_ = enc.Close()
	}
	out.WriteString("---\n")
	out.Write(body)
	return out.Bytes(), nil
}

// Flow is an executable Maestro flow under flows/.
type Flow struct {
	Path    string
	Rel     string // relative to flows/
	Feature string // first path segment
	File    string // base name
	Header  *Header
}

func (s *Store) newFlow(path string) (*Flow, error) {
	rel, _ := filepath.Rel(s.P.FlowsRoot(), path)
	rel = filepath.ToSlash(rel)
	feature, _, _ := strings.Cut(rel, "/")
	h, err := ParseHeader(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.P.Rel(path), err)
	}
	return &Flow{Path: path, Rel: rel, Feature: feature, File: filepath.Base(path), Header: h}, nil
}

func (s *Store) Flows() ([]*Flow, error) {
	files, err := listFiles(s.P.FlowsRoot(), ".yaml")
	if err != nil {
		return nil, err
	}
	flows := make([]*Flow, 0, len(files))
	for _, f := range files {
		fl, err := s.newFlow(f)
		if err != nil {
			return nil, err
		}
		flows = append(flows, fl)
	}
	return flows, nil
}

func (s *Store) Flow(path string) (*Flow, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.P.Root, path)
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return nil, fmt.Errorf("flow file '%s' does not exist", path)
	}
	return s.newFlow(path)
}

func (f *Flow) HasTag(tag string) bool {
	for _, t := range f.Header.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (f *Flow) MatchesTags(tags []string) bool {
	for _, want := range tags {
		want = strings.TrimSpace(want)
		if want != "" && f.HasTag(want) {
			return true
		}
	}
	return false
}

// MatchesPlatform: iOS runs only flows tagged ios; Android runs everything else.
func (f *Flow) MatchesPlatform(platform string) bool {
	if platform == "ios" {
		return f.HasTag("ios")
	}
	return !f.HasTag("ios")
}

// Profiles.

func (s *Store) ProfileNames() ([]string, error) {
	names, err := listNames(s.P.ProfilesRoot(), ".json")
	if err != nil {
		return nil, err
	}
	out := names[:0]
	for _, n := range names {
		if n != "shared" {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Store) ProfileExists(name string) bool {
	_, err := os.Stat(filepath.Join(s.P.ProfilesRoot(), name+".json"))
	return err == nil
}

func (s *Store) ProfileMappings(name string) ([]string, error) {
	path := filepath.Join(s.P.ProfilesRoot(), name+".json")
	obj, err := ojson.ParseObjectFile(path)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", s.P.Rel(path), err)
	}
	list, ok := mustGet(obj, "mappings").([]any)
	if !ok {
		return nil, fmt.Errorf("profile %s must contain a mappings string array", s.P.Rel(path))
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		sv, ok := v.(string)
		if !ok || sv == "" {
			return nil, fmt.Errorf("profile %s must contain a mappings string array", s.P.Rel(path))
		}
		out = append(out, sv)
	}
	return out, nil
}

// Scenarios.

type Scenario struct {
	Path   string
	Name   string
	Header *Header
}

func (s *Store) Scenarios() ([]*Scenario, error) {
	files, err := listFiles(s.P.ScenariosRoot(), ".yaml")
	if err != nil {
		return nil, err
	}
	var out []*Scenario
	for _, f := range files {
		h, err := ParseHeader(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.P.Rel(f), err)
		}
		rel, _ := filepath.Rel(s.P.ScenariosRoot(), f)
		out = append(out, &Scenario{Path: f, Name: strings.TrimSuffix(filepath.ToSlash(rel), ".yaml"), Header: h})
	}
	return out, nil
}

// FindScenario accepts a path relative to scenarios/ or a unique base name.
func (s *Store) FindScenario(name string) (*Scenario, error) {
	all, err := s.Scenarios()
	if err != nil {
		return nil, err
	}
	var match *Scenario
	for _, sc := range all {
		if sc.Name == name {
			return sc, nil
		}
	}
	for _, sc := range all {
		if filepath.Base(sc.Name) == name {
			if match != nil {
				return nil, fmt.Errorf("ambiguous scenario '%s'; use its path relative to scenarios/", name)
			}
			match = sc
		}
	}
	return match, nil
}
