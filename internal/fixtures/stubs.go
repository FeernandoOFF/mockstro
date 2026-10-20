// Package fixtures reads and validates the .maestro tree: stubs (with `extends`), profiles,
// flows and scenarios.
package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
)

const RemoveMarker = "__remove__"

type Store struct {
	P *project.Project
}

func New(p *project.Project) *Store { return &Store{P: p} }

func (s *Store) StubPath(name string) string {
	return filepath.Join(s.P.StubsRoot(), name+".json")
}

func (s *Store) StubExists(name string) bool {
	st, err := os.Stat(s.StubPath(name))
	return err == nil && !st.IsDir()
}

func (s *Store) extendsSourcePath(name string) string {
	if rest, ok := strings.CutPrefix(name, "bases/"); ok {
		return filepath.Join(s.P.BasesRoot(), rest+".json")
	}
	return s.StubPath(name)
}

// StubNames lists every stub under stubs/, sorted, without the .json suffix.
func (s *Store) StubNames() ([]string, error) {
	return listNames(s.P.StubsRoot(), ".json")
}

func listNames(root, ext string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ext) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		names = append(names, strings.TrimSuffix(filepath.ToSlash(rel), ext))
		return nil
	})
	sort.Strings(names)
	return names, err
}

func listFiles(root, ext string) ([]string, error) {
	var files []string
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ext) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// ExtendsChain returns the stub followed by each ancestor it inherits from.
func (s *Store) ExtendsChain(name string) ([]string, error) {
	chain := []string{name}
	seen := map[string]bool{name: true}
	cur := name
	for {
		obj, err := s.readSource(cur)
		if err != nil {
			return chain, err
		}
		parent := obj.String("extends")
		if parent == "" {
			return chain, nil
		}
		if seen[parent] {
			return chain, fmt.Errorf("circular extends chain: %s -> %s", strings.Join(chain, " -> "), parent)
		}
		seen[parent] = true
		chain = append(chain, parent)
		cur = parent
	}
}

func (s *Store) readSource(name string) (*ojson.Object, error) {
	file := s.extendsSourcePath(name)
	if _, err := os.Stat(file); err != nil {
		return nil, fmt.Errorf("extends target '%s' does not exist", name)
	}
	obj, err := ojson.ParseObjectFile(file)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", s.P.Rel(file), err)
	}
	return obj, nil
}

func (s *Store) resolvedResponse(name string, chain []string) (any, error) {
	for _, c := range chain {
		if c == name {
			return nil, fmt.Errorf("circular extends chain: %s -> %s", strings.Join(chain, " -> "), name)
		}
	}
	obj, err := s.readSource(name)
	if err != nil {
		return nil, err
	}
	var response any = ojson.NewObject()
	if r, ok := obj.Get("response"); ok && r != nil {
		response = r
	}
	if parent := obj.String("extends"); parent != "" {
		inherited, err := s.resolvedResponse(parent, append(chain, name))
		if err != nil {
			return nil, err
		}
		response = ojson.Merge(inherited, response)
	}
	return response, nil
}

// ResolvedStub is what WireMock receives: `extends` applied, `__remove__` markers pruned.
// Only .response is inherited; request, name, priority and scenario fields stay the stub's own.
func (s *Store) ResolvedStub(name string) (*ojson.Object, error) {
	file := s.StubPath(name)
	if _, err := os.Stat(file); err != nil {
		return nil, fmt.Errorf("unknown stub '%s'", name)
	}
	obj, err := ojson.ParseObjectFile(file)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON in %s: %w", s.P.Rel(file), err)
	}
	if IsMessage(obj) && obj.Has("extends") {
		return nil, fmt.Errorf("message stub %s does not support extends", name)
	}
	if obj.String("extends") == "" {
		return obj, nil
	}
	response, err := s.resolvedResponse(name, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve the extends chain for stub '%s': %w", name, err)
	}
	obj = obj.Clone()
	obj.Delete("extends")
	obj.Set("response", response)
	return prune(obj).(*ojson.Object), nil
}

func prune(v any) any {
	switch t := v.(type) {
	case *ojson.Object:
		out := ojson.NewObject()
		for _, k := range t.Keys() {
			val, _ := t.Get(k)
			if sv, ok := val.(string); ok && sv == RemoveMarker {
				continue
			}
			out.Set(k, prune(val))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = prune(t[i])
		}
		return out
	}
	return v
}

// Route is "<METHOD> <matcher>=<value>".
func IsMessage(stub *ojson.Object) bool { return stub.Has("trigger") }

func Route(stub *ojson.Object) string {
	if IsMessage(stub) {
		trigger, _ := stub.Get("trigger")
		b, _ := ojson.Marshal(trigger)
		return "MESSAGE " + string(b)
	}
	req := stub.Object("request")
	if req == nil {
		return ""
	}
	method := strings.ToUpper(req.String("method"))
	if method == "" {
		method = "ANY"
	}
	for _, k := range []string{"url", "urlPath", "urlPattern", "urlPathPattern"} {
		if v, ok := req.Get(k); ok && v != nil {
			if sv, ok := v.(string); ok {
				return method + " " + k + "=" + sv
			}
		}
	}
	return ""
}

// Identity is what makes two stubs compete for the same request.
func Identity(stub *ojson.Object) string {
	if IsMessage(stub) {
		b, _ := ojson.Marshal(stub.Object("trigger"))
		var canonical any
		_ = json.Unmarshal(b, &canonical)
		b, _ = json.Marshal(canonical)
		return "MESSAGE " + string(b)
	}
	return Route(stub) + "\t" + stub.String("scenarioName") + "\t" + stub.String("requiredScenarioState")
}

func (s *Store) rawIdentity(name string) (string, error) {
	obj, err := ojson.ParseObjectFile(s.StubPath(name))
	if err != nil {
		return "", fmt.Errorf("invalid JSON in stub '%s': %w", name, err)
	}
	return Identity(obj), nil
}

var allowedFaults = map[string]bool{
	"CONNECTION_RESET_BY_PEER": true, "EMPTY_RESPONSE": true,
	"MALFORMED_RESPONSE_CHUNK": true, "RANDOM_DATA_THEN_CLOSE": true,
}

// ValidateStub checks a resolved stub before WireMock receives it.
func (s *Store) ValidateStub(stub *ojson.Object, expectedName, sourceFile string) error {
	rel := s.P.Rel(sourceFile)
	if IsMessage(stub) {
		return s.validateMessage(stub, expectedName, rel)
	}
	if stub.Has("actions") {
		return fmt.Errorf("message stub requires trigger in %s", rel)
	}
	if resp := stub.Object("response"); resp != nil && resp.Has("openWebsocketChannel") && s.P.WireMockVersion != "4.0.0-beta.40" {
		return fmt.Errorf("WebSocket upgrades require wiremockVersion: 4.0.0-beta.40 in %s", rel)
	}
	req := stub.Object("request")
	if req == nil {
		return fmt.Errorf("missing request object in %s", rel)
	}
	if req.String("method") == "" {
		return fmt.Errorf("missing request.method in %s", rel)
	}
	resp := stub.Object("response")
	if resp == nil {
		return fmt.Errorf("missing response object in %s", rel)
	}
	if stub.String("name") != expectedName {
		return fmt.Errorf("stub name must be '%s' in %s", expectedName, rel)
	}
	matchers := 0
	for _, k := range []string{"url", "urlPath", "urlPattern", "urlPathPattern"} {
		if v, ok := req.Get(k); ok && v != nil {
			matchers++
		}
	}
	if matchers != 1 {
		return fmt.Errorf("exactly one URL matcher is required in %s", rel)
	}
	if Route(stub) == "" {
		return fmt.Errorf("invalid request route in %s", rel)
	}
	if body := resp.String("bodyFileName"); body != "" {
		if strings.HasPrefix(body, "/") || strings.HasPrefix(body, "../") || strings.Contains(body, "/../") || strings.HasSuffix(body, "/..") {
			return fmt.Errorf("unsafe response.bodyFileName in %s: %s", rel, body)
		}
		if _, err := os.Stat(filepath.Join(s.P.BodiesRoot(), body)); err != nil {
			return fmt.Errorf("missing body '%s' referenced by %s", body, rel)
		}
	}
	if err := validateFaultDelay(resp); err != nil {
		return fmt.Errorf("invalid native WireMock fault/delay response in %s: %v", rel, err)
	}
	return nil
}

func nonNegNumber(v any) (float64, bool) {
	f, ok := ojson.AsFloat(v)
	return f, ok && f >= 0
}

func validateFaultDelay(resp *ojson.Object) error {
	if v, ok := resp.Get("fault"); ok && v != nil {
		sv, _ := v.(string)
		if !allowedFaults[sv] {
			return fmt.Errorf("fault %v", v)
		}
	}
	if v, ok := resp.Get("fixedDelayMilliseconds"); ok && v != nil {
		if _, ok := nonNegNumber(v); !ok || !ojson.IsInt(v) {
			return fmt.Errorf("fixedDelayMilliseconds")
		}
	}
	if v, ok := resp.Get("delayDistribution"); ok && v != nil {
		d, ok := v.(*ojson.Object)
		if !ok {
			return fmt.Errorf("delayDistribution")
		}
		switch d.String("type") {
		case "lognormal":
			median, ok1 := nonNegNumber(mustGet(d, "median"))
			_, ok2 := nonNegNumber(mustGet(d, "sigma"))
			if !ok1 || !ok2 {
				return fmt.Errorf("delayDistribution lognormal")
			}
			if mv, ok := d.Get("maxValue"); ok && mv != nil {
				if f, ok := ojson.AsFloat(mv); !ok || f < median {
					return fmt.Errorf("delayDistribution maxValue")
				}
			}
		case "uniform":
			lower, ok1 := nonNegNumber(mustGet(d, "lower"))
			upper, ok2 := ojson.AsFloat(mustGet(d, "upper"))
			if !ok1 || !ok2 || upper < lower {
				return fmt.Errorf("delayDistribution uniform")
			}
		default:
			return fmt.Errorf("delayDistribution type")
		}
	}
	if v, ok := resp.Get("chunkedDribbleDelay"); ok && v != nil {
		d, ok := v.(*ojson.Object)
		if !ok {
			return fmt.Errorf("chunkedDribbleDelay")
		}
		n, ok1 := ojson.AsFloat(mustGet(d, "numberOfChunks"))
		_, ok2 := nonNegNumber(mustGet(d, "totalDuration"))
		if !ok1 || n <= 0 || !ojson.IsInt(mustGet(d, "numberOfChunks")) || !ok2 || !ojson.IsInt(mustGet(d, "totalDuration")) {
			return fmt.Errorf("chunkedDribbleDelay values")
		}
	}
	return nil
}

func mustGet(o *ojson.Object, k string) any {
	v, _ := o.Get(k)
	return v
}

// MergeOverridesByRoute keeps the last stub for each identity, preserving order otherwise.
func (s *Store) MergeOverridesByRoute(names []string) ([]string, error) {
	var merged []string
	ids := map[string]string{}
	for _, name := range names {
		id, err := s.rawIdentity(name)
		if err != nil {
			return nil, err
		}
		next := merged[:0:0]
		for _, existing := range merged {
			if ids[existing] != id {
				next = append(next, existing)
			}
		}
		ids[name] = id
		merged = append(next, name)
	}
	return merged, nil
}

// ValidateLayerDuplicates rejects two stubs in one layer that would match the same request.
func (s *Store) ValidateLayerDuplicates(label string, names []string) error {
	seen := map[string]string{}
	for _, name := range names {
		id, err := s.rawIdentity(name)
		if err != nil {
			return err
		}
		if existing, ok := seen[id]; ok {
			return fmt.Errorf("non-deterministic duplicate route in %s: '%s' and '%s' both match %s", label, existing, name, strings.ReplaceAll(id, "\t", " "))
		}
		seen[id] = name
	}
	return nil
}

// Source returns a stub's or base's own JSON (no extends applied) and its path.
func (s *Store) Source(name string) (*ojson.Object, string, error) {
	path := s.extendsSourcePath(name)
	obj, err := s.readSource(name)
	return obj, path, err
}

// BodyFile reads a response.bodyFileName from __files.
func (s *Store) BodyFile(name string) ([]byte, string, error) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return nil, "", fmt.Errorf("unsafe bodyFileName %q", name)
	}
	path := filepath.Join(s.P.BodiesRoot(), name)
	b, err := os.ReadFile(path)
	return b, path, err
}
