package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/ojson"
	"github.com/feernandooff/mockstro/internal/project"
)

// CatalogItem is one thing QA can start. Ref is what ResolveMockTarget accepts.
type CatalogItem struct {
	Kind         string            `json:"kind"` // scenario | profile | flow
	Ref          string            `json:"ref"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Group        string            `json:"group,omitempty"`
	ProfileGroup string            `json:"profileGroup"`
	Profile      string            `json:"profile,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	FBRC         map[string]string `json:"fbrc,omitempty"`
}

// Unmapped profiles keep their own group; items without a profile remain visible too.
func profileGroup(profile string, labels map[string]string) string {
	if label := labels[profile]; label != "" {
		return label
	}
	if profile != "" {
		return profile
	}
	return "Other"
}

// featureTags combines explicit fixture tags with exact key mappings supplied by the project.
func featureTags(tags []string, fbrc map[string]string, keyTags map[string]string) []string {
	out := slices.Clone(tags)
	for key := range fbrc {
		if tag := keyTags[key]; tag != "" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Catalog reads the project's fixtures fresh each time, so edits show up on reload.
func Catalog(p *project.Project) ([]CatalogItem, error) {
	s := fixtures.New(p)
	var items []CatalogItem

	scenarios, err := s.Scenarios()
	if err != nil {
		return nil, err
	}
	flows, err := s.Flows()
	if err != nil {
		return nil, err
	}
	for _, sc := range scenarios {
		title := sc.Header.Name
		if title == "" {
			title = sc.Name
		}
		items = append(items, CatalogItem{Kind: "scenario", Ref: sc.Name, Title: title, Description: sc.Header.Description,
			Profile: sc.Header.Profile, ProfileGroup: profileGroup(sc.Header.Profile, p.Catalog.ProfileGroups), Tags: featureTags(sc.Header.Tags, sc.Header.FBRC, p.Catalog.FeatureTags), FBRC: sc.Header.FBRC})
	}

	profiles, err := s.ProfileNames()
	if err != nil {
		return nil, err
	}
	for _, name := range profiles {
		if name == "shared" {
			continue
		}
		stubs, _ := s.ProfileMappings(name)
		items = append(items, CatalogItem{Kind: "profile", Ref: name, Title: name, ProfileGroup: profileGroup(name, p.Catalog.ProfileGroups),
			Description: fmt.Sprintf("Profile with %d stubs on top of the shared defaults.", len(stubs))})
	}

	for _, f := range flows {
		title := f.Header.Name
		if title == "" {
			title = f.Rel
		}
		items = append(items, CatalogItem{Kind: "flow", Ref: f.Path, Title: title, Description: f.Header.Description,
			Group: f.Feature, Profile: f.Header.Profile, ProfileGroup: profileGroup(f.Header.Profile, p.Catalog.ProfileGroups), Tags: featureTags(f.Header.Tags, f.Header.FBRC, p.Catalog.FeatureTags), FBRC: f.Header.FBRC})
	}

	// The page derives its folds in catalog order, including groups absent from groupOrder.
	rank := func(group string) int {
		if i := slices.Index(p.Catalog.GroupOrder, group); i >= 0 {
			return i
		}
		return len(p.Catalog.GroupOrder)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if a, b := items[i].ProfileGroup, items[j].ProfileGroup; a != b {
			if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			return a < b
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Title < items[j].Title
	})
	return items, nil
}

func findItem(items []CatalogItem, kind, ref string) (CatalogItem, bool) {
	for _, it := range items {
		if it.Ref == ref && (kind == "" || it.Kind == kind) {
			return it, true
		}
	}
	return CatalogItem{}, false
}

// KnownKey is a Remote Config key some scenario or flow in the project sets, with the values used.
type KnownKey struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// KnownKeys lists every Remote Config key the project's scenarios and flows set, so a tester
// adding one gets suggestions instead of typing it from memory.
func KnownKeys(items []CatalogItem) []KnownKey {
	values := map[string]map[string]bool{}
	for _, it := range items {
		for k, v := range it.FBRC {
			if values[k] == nil {
				values[k] = map[string]bool{}
			}
			values[k][v] = true
		}
	}
	keys := make([]KnownKey, 0, len(values))
	for k, vs := range values {
		kk := KnownKey{Key: k}
		for v := range vs {
			kk.Values = append(kk.Values, v)
		}
		sort.Strings(kk.Values)
		keys = append(keys, kk)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
	return keys
}

// Step kinds on the test card.
const (
	StepDo   = "do"   // something the tester does
	StepSee  = "see"  // something the tester should see
	StepGone = "gone" // something that should no longer be shown
	StepIf   = "if"   // a condition; the steps inside apply only when it holds
)

// Step is one line of the test card QA follows, read from the served flow's own commands.
type Step struct {
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Note     string `json:"note,omitempty"`     // the comment above the command in the flow
	Platform string `json:"platform,omitempty"` // android | ios; empty applies to both
	Optional bool   `json:"optional,omitempty"`
	Steps    []Step `json:"steps,omitempty"` // inside an "if"
}

// LiveCheck is one of the flow's apiVerifications, counted live against the request journal.
type LiveCheck struct {
	Stub   string `json:"stub"`
	Label  string `json:"label"`
	Expect string `json:"expect"`
	Min    int    `json:"min"`
	Max    *int   `json:"max,omitempty"`
}

const maxStepDepth = 6

// FlowSteps turns a flow's commands into the steps a person follows by hand. It reads what the
// flow already says: commands, the label on a command, and the comment above it. Launch and
// utility sub-flows collapse to one line or disappear; waits and scrolling are not steps.
func FlowSteps(flowPath string) []Step { return stepsOf(flowPath, 0, "") }

func stepsOf(flowPath string, depth int, platform string) []Step {
	if depth > maxStepDepth {
		return nil
	}
	data, err := os.ReadFile(flowPath)
	if err != nil {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var header, body yaml.Node
	if dec.Decode(&header) != nil || dec.Decode(&body) != nil || len(body.Content) == 0 || body.Content[0].Kind != yaml.SequenceNode {
		return nil
	}
	return walkSteps(body.Content[0], filepath.Dir(flowPath), depth, platform, body.HeadComment)
}

func walkSteps(seq *yaml.Node, dir string, depth int, platform, lead string) []Step {
	var out []Step
	add := func(s Step) {
		if s.Platform == "" {
			s.Platform = platform
		}
		if n := len(out); n > 0 && out[n-1].Kind == s.Kind && out[n-1].Text == s.Text && s.Kind != StepIf {
			return
		}
		out = append(out, s)
	}
	for i, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && item.Value == "launchApp" {
			add(Step{Kind: StepDo, Text: "Launch the app", Note: stepNote(item, "")})
			continue
		}
		if item.Kind != yaml.MappingNode || len(item.Content) < 2 {
			continue
		}
		cmd, val := item.Content[0].Value, item.Content[1]
		note := stepNote(item, "")
		if i == 0 && note == "" {
			note = stepNote(nil, lead)
		}
		label := ""
		if l := mapField(val, "label"); l != nil {
			label = l.Value
		}
		optional := false
		if o := mapField(val, "optional"); o != nil {
			optional = o.Value == "true"
		}
		step := func(kind, text string) {
			if label != "" {
				text = label
			}
			add(Step{Kind: kind, Text: text, Note: note, Optional: optional})
		}
		switch cmd {
		case "launchApp":
			step(StepDo, "Launch the app")
		case "tapOn", "doubleTapOn", "longPressOn":
			step(StepDo, "Tap “"+selectorText(val)+"”")
		case "inputText":
			text := val.Value
			if t := mapField(val, "text"); t != nil {
				text = t.Value
			}
			step(StepDo, "Type "+text)
		case "scrollUntilVisible":
			step(StepDo, "Scroll to “"+selectorText(mapField(val, "element"))+"”")
		case "back":
			step(StepDo, "Go back")
		case "openLink":
			link := val.Value
			if l := mapField(val, "link"); l != nil {
				link = l.Value
			}
			step(StepDo, "Open the link "+link)
		case "assertVisible":
			step(StepSee, "You should see “"+selectorText(val)+"”")
		case "assertNotVisible":
			step(StepGone, "“"+selectorText(val)+"” should not be shown")
		case "extendedWaitUntil":
			if v := mapField(val, "visible"); v != nil {
				step(StepSee, "You should see “"+selectorText(v)+"”")
			} else if v := mapField(val, "notVisible"); v != nil {
				step(StepGone, "“"+selectorText(v)+"” should go away")
			}
		case "runFlow":
			out = append(out, runFlowSteps(val, dir, depth, platform, note)...)
		}
	}
	return out
}

// runFlowSteps expands a runFlow: a platform branch tags its steps, a visible/notVisible
// condition wraps them in an "if", a JavaScript condition is skipped, and launch or utility
// sub-flows collapse.
func runFlowSteps(val *yaml.Node, dir string, depth int, platform, note string) []Step {
	file := ""
	if val.Kind == yaml.ScalarNode {
		file = val.Value
	} else if f := mapField(val, "file"); f != nil {
		file = f.Value
	}
	when := mapField(val, "when")
	cond := ""
	if when != nil {
		if mapField(when, "true") != nil {
			return nil
		}
		if p := mapField(when, "platform"); p != nil {
			platform = strings.ToLower(p.Value)
		}
		if v := mapField(when, "visible"); v != nil {
			cond = "If “" + selectorText(v) + "” is shown"
		} else if v := mapField(when, "notVisible"); v != nil {
			cond = "If “" + selectorText(v) + "” is not shown"
		}
	}
	var inner []Step
	switch base := filepath.Base(file); {
	case file != "" && strings.HasPrefix(base, "relaunch"):
		inner = []Step{{Kind: StepDo, Text: "Close and reopen the app", Note: note}}
	case file != "" && strings.HasPrefix(base, "launch"):
		inner = []Step{{Kind: StepDo, Text: "Launch the app (fresh start)", Note: note}}
	case file != "" && (strings.HasPrefix(base, "assert-") || strings.HasPrefix(base, "accept-")):
		return nil
	case file != "":
		inner = stepsOf(filepath.Join(dir, file), depth+1, platform)
	default:
		if c := mapField(val, "commands"); c != nil && c.Kind == yaml.SequenceNode {
			inner = walkSteps(c, dir, depth+1, platform, "")
		}
	}
	for i := range inner {
		if inner[i].Platform == "" {
			inner[i].Platform = platform
		}
	}
	if len(inner) == 0 || cond == "" {
		return inner
	}
	return []Step{{Kind: StepIf, Text: cond, Note: note, Platform: platform, Steps: inner}}
}

func mapField(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// stepNote is the comment written above a command, joined into one sentence.
func stepNote(item *yaml.Node, comment string) string {
	if item != nil {
		comment = item.HeadComment
		if comment == "" && len(item.Content) > 0 {
			comment = item.Content[0].HeadComment
		}
	}
	var parts []string
	for _, line := range strings.Split(comment, "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

var (
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	repeatedDots  = regexp.MustCompile(`…+`)
)

// selectorText names an element the way a person would: its label, else its text with the
// regex tidied, else its id as words.
func selectorText(n *yaml.Node) string {
	switch {
	case n == nil:
		return ""
	case n.Kind == yaml.ScalarNode:
		return tidyRegex(n.Value)
	}
	if l := mapField(n, "label"); l != nil && l.Value != "" {
		return l.Value
	}
	if t := mapField(n, "text"); t != nil {
		return tidyRegex(t.Value)
	}
	if id := mapField(n, "id"); id != nil {
		return idWords(id.Value)
	}
	return ""
}

func tidyRegex(s string) string {
	s = strings.ReplaceAll(s, "(?i)", "")
	s = strings.Trim(s, "^$")
	s = strings.ReplaceAll(s, `\.`, ".")
	s = strings.ReplaceAll(s, ".*", "…")
	s = strings.NewReplacer("(", "", ")", "", "|", " or ").Replace(s)
	s = repeatedDots.ReplaceAllString(s, "…")
	return strings.TrimSpace(strings.Trim(s, "… "))
}

func idWords(id string) string {
	id = strings.ToLower(camelBoundary.ReplaceAllString(strings.ReplaceAll(id, "_", " "), "$1 $2"))
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// ChecksFor lists a flow's apiVerifications for the page to count against the journal.
func ChecksFor(vs []fixtures.Verification) []LiveCheck {
	var out []LiveCheck
	for _, v := range vs {
		lo, hi := v.Bounds()
		c := LiveCheck{Stub: v.Stub, Label: stubLabel(v.Stub), Expect: v.Expectation(), Min: lo}
		if hi < math.MaxInt32 {
			c.Max = &hi
		}
		out = append(out, c)
	}
	return out
}

// stubLabel makes a stub name readable: "electricity-only/billing-details-paid" is "Billing details paid".
func stubLabel(stub string) string {
	words := strings.ReplaceAll(path.Base(stub), "-", " ")
	if words == "" {
		return stub
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// Where a stub in a preview comes from, highest priority first.
const (
	GroupTest     = "test"     // added by the flow or scenario itself
	GroupCustomer = "customer" // the customer profile
	GroupShared   = "shared"   // defaults every target gets
)

// StubInfo says which call a stub answers and what it answers with, in brief.
type StubInfo struct {
	Name     string `json:"name"`
	Group    string `json:"group"`
	Method   string `json:"method,omitempty"`
	Path     string `json:"path,omitempty"`
	Status   int    `json:"status,omitempty"`
	Editable bool   `json:"editable"`           // one exact path, so a custom response can take its place
	Checked  bool   `json:"checked,omitempty"`  // the flow's apiVerifications count this stub
	Replaced bool   `json:"replaced,omitempty"` // a higher-priority stub answers the same method and path
	Scenario string `json:"scenario,omitempty"` // its answer changes as the app calls other stubs in the same scenario
	State    string `json:"state,omitempty"`    // the scenario state it answers in ("Started" is the beginning)
	Next     string `json:"next,omitempty"`     // the state calling it moves the scenario to
	After    string `json:"after,omitempty"`    // for a later state: the call that gets the scenario there, e.g. "POST /api/topup"
}

// Preview is what a mock would do, read from its files without starting it.
type Preview struct {
	Kind         string            `json:"kind"`
	Ref          string            `json:"ref"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Profile      string            `json:"profile,omitempty"`
	ProfileGroup string            `json:"profileGroup"`
	FBRC         map[string]string `json:"fbrc"`
	Stubs        []StubInfo        `json:"stubs"` // everything it will answer, highest priority first
	Steps        []Step            `json:"steps,omitempty"`
	Checks       []LiveCheck       `json:"checks,omitempty"`
}

// PreviewOf reads one catalog item's steps, checks, stubs and Remote Config.
func PreviewOf(p *project.Project, it CatalogItem) (Preview, error) {
	s := fixtures.New(p)
	pv := Preview{Kind: it.Kind, Ref: it.Ref, Title: it.Title, Description: it.Description, Profile: it.Profile,
		ProfileGroup: it.ProfileGroup, FBRC: maps.Clone(it.FBRC), Stubs: []StubInfo{}}
	if pv.FBRC == nil {
		pv.FBRC = map[string]string{}
	}
	var tests, customer []string
	switch it.Kind {
	case "flow":
		f, err := s.Flow(it.Ref)
		if err != nil {
			return pv, err
		}
		tests, pv.Steps, pv.Checks = f.Header.Overrides, FlowSteps(f.Path), ChecksFor(f.Header.Verifications)
	case "scenario":
		sc, err := s.FindScenario(it.Ref)
		if err != nil {
			return pv, err
		}
		if sc != nil {
			tests, pv.Checks = sc.Header.Overrides, ChecksFor(sc.Header.Verifications)
		}
	case "profile":
		customer = []string{}
	}
	profile := it.Profile
	if it.Kind == "profile" {
		profile = it.Ref
	}
	if profile != "" {
		names, err := s.ProfileMappings(profile)
		if err != nil {
			return pv, err
		}
		customer = names
	}
	shared, _ := s.ProfileMappings("shared")

	checked := map[string]bool{}
	for _, c := range pv.Checks {
		checked[c.Stub] = true
	}
	answered := map[string]bool{} // method and path already taken by a higher-priority group
	for _, group := range []struct {
		name  string
		stubs []string
	}{{GroupTest, tests}, {GroupCustomer, customer}, {GroupShared, shared}} {
		taken := map[string]bool{}
		for _, name := range group.stubs {
			info := stubInfo(s, name)
			info.Group, info.Checked = group.name, checked[name]
			if key := info.Method + " " + info.Path; info.Editable && answered[key] {
				info.Replaced = true
			}
			taken[info.Method+" "+info.Path] = info.Editable
			pv.Stubs = append(pv.Stubs, info)
		}
		for key, exact := range taken {
			if exact {
				answered[key] = true
			}
		}
	}
	// Name what gets a scenario into each later state, so "paid" reads as "after POST /api/topup".
	setters := map[string]string{}
	for _, st := range pv.Stubs {
		if st.Scenario != "" && st.Next != "" {
			setters[st.Scenario+"\x00"+st.Next] = st.Method + " " + st.Path
		}
	}
	for i, st := range pv.Stubs {
		if st.Scenario != "" && st.State != "" && st.State != "Started" {
			pv.Stubs[i].After = setters[st.Scenario+"\x00"+st.State]
		}
	}
	return pv, nil
}

func stubInfo(s *fixtures.Store, name string) StubInfo {
	info := StubInfo{Name: name}
	stub, err := s.ResolvedStub(name)
	if err != nil {
		return info
	}
	info.Scenario, info.State, info.Next = stub.String("scenarioName"), stub.String("requiredScenarioState"), stub.String("newScenarioState")
	if req := stub.Object("request"); req != nil {
		info.Method = req.String("method")
		for _, k := range []string{"urlPath", "urlPathPattern", "url", "urlPattern"} {
			if v := req.String(k); v != "" {
				info.Path = v
				info.Editable = (k == "urlPath" || k == "url") && info.Method != "" && info.Method != "ANY" && !strings.ContainsAny(v, "?#")
				break
			}
		}
	}
	if resp := stub.Object("response"); resp != nil {
		if v, ok := resp.Get("status"); ok {
			info.Status = numberOf(v)
		}
	}
	return info
}

func numberOf(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := strconv.Atoi(n.String())
		return i
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// StubDetail is one stub's response in full, as the app would get it.
type StubDetail struct {
	Name      string            `json:"name"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	DelayMs   int               `json:"delayMs,omitempty"`
	Scenario  string            `json:"scenario,omitempty"`  // see StubInfo.Scenario
	State     string            `json:"state,omitempty"`     // see StubInfo.State
	Next      string            `json:"next,omitempty"`      // see StubInfo.Next
	Template  bool              `json:"template,omitempty"`  // the body uses {{templates}} that WireMock fills in per request
	Truncated bool              `json:"truncated,omitempty"` // the body is longer than what is shown
	Editable  bool              `json:"editable"`
}

const maxStubBodyShown = 256 << 10

// StubDetailOf resolves a stub (including what it extends) and reads its body: inline text, JSON,
// or a file under __files.
func StubDetailOf(p *project.Project, name string) (StubDetail, error) {
	s := fixtures.New(p)
	// A stub name is a path under the stubs folder; one that climbs out of it is not a stub.
	if !filepath.IsLocal(filepath.FromSlash(name)) || !s.StubExists(name) {
		return StubDetail{}, fmt.Errorf("unknown stub %q", name)
	}
	stub, err := s.ResolvedStub(name)
	if err != nil {
		return StubDetail{}, err
	}
	info := stubInfo(s, name)
	d := StubDetail{Name: name, Method: info.Method, Path: info.Path, Status: info.Status, Headers: map[string]string{}, Editable: info.Editable, Scenario: info.Scenario, State: info.State, Next: info.Next}
	resp := stub.Object("response")
	if resp == nil {
		return d, nil
	}
	if h := resp.Object("headers"); h != nil {
		for _, k := range h.Keys() {
			d.Headers[k] = h.String(k)
		}
	}
	if v, ok := resp.Get("fixedDelayMilliseconds"); ok {
		d.DelayMs = numberOf(v)
	}
	if t, ok := resp.Get("transformers"); ok {
		if raw, err := ojson.Marshal(t); err == nil && bytes.Contains(raw, []byte("response-template")) {
			d.Template = true
		}
	}
	switch {
	case resp.Has("jsonBody"):
		v, _ := resp.Get("jsonBody")
		raw, err := ojson.Marshal(v)
		if err != nil {
			return d, err
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") == nil {
			d.Body = pretty.String()
		} else {
			d.Body = string(raw)
		}
	case resp.Has("body"):
		d.Body = resp.String("body")
	case resp.Has("bodyFileName"):
		file := filepath.Join(p.BodiesRoot(), filepath.FromSlash(resp.String("bodyFileName")))
		if rel, err := filepath.Rel(p.BodiesRoot(), file); err != nil || strings.HasPrefix(rel, "..") {
			return d, fmt.Errorf("body file %q is outside the project's __files", resp.String("bodyFileName"))
		}
		if b, err := os.ReadFile(file); err == nil {
			d.Body = string(b)
		}
	}
	if len(d.Body) > maxStubBodyShown {
		d.Body, d.Truncated = d.Body[:maxStubBodyShown], true
	}
	return d, nil
}
