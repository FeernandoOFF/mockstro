package web

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/project"
)

// CatalogItem is one thing QA can start. Ref is what ResolveMockTarget accepts.
type CatalogItem struct {
	Kind        string            `json:"kind"` // scenario | profile | flow
	Ref         string            `json:"ref"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Group       string            `json:"group,omitempty"`
	Customer    string            `json:"customer"` // Elec only | Gas only | Dual fuel | Other
	Profile     string            `json:"profile,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	FBRC        map[string]string `json:"fbrc,omitempty"`
}

// Customer groups the library is organised by, in display order.
const (
	CustomerElec  = "Elec only"
	CustomerGas   = "Gas only"
	CustomerDual  = "Dual fuel"
	CustomerOther = "Other"
)

// customerType says which kind of customer a profile is, from its name (elec-…, gas-…, dual-fuel…).
// Anything else, including an item with no profile, is "Other" so it is never hidden.
func customerType(profile string) string {
	p := strings.ToLower(profile)
	switch {
	case strings.Contains(p, "dual"):
		return CustomerDual
	case strings.HasPrefix(p, "gas"):
		return CustomerGas
	case strings.HasPrefix(p, "elec"):
		return CustomerElec
	}
	return CustomerOther
}

// featureTags are the header's tags plus any feature the Remote Config keys name, so a scenario can
// be found by feature too. Keys follow Config_<Area>_…, and an area counts only when the team has
// already used it as a tag (vocabulary), which keeps compound key names from becoming noise.
func featureTags(tags []string, fbrc map[string]string, vocabulary map[string]bool) []string {
	out := slices.Clone(tags)
	for key := range fbrc {
		parts := strings.Split(key, "_")
		if len(parts) < 3 || parts[0] != "Config" {
			continue
		}
		if area := strings.ToLower(parts[1]); vocabulary[area] && !slices.Contains(out, area) {
			out = append(out, area)
		}
	}
	sort.Strings(out)
	return out
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
	vocabulary := map[string]bool{}
	for _, sc := range scenarios {
		for _, t := range sc.Header.Tags {
			vocabulary[t] = true
		}
	}
	for _, f := range flows {
		for _, t := range f.Header.Tags {
			vocabulary[t] = true
		}
	}
	for _, sc := range scenarios {
		title := sc.Header.Name
		if title == "" {
			title = sc.Name
		}
		items = append(items, CatalogItem{Kind: "scenario", Ref: sc.Name, Title: title, Description: sc.Header.Description,
			Profile: sc.Header.Profile, Customer: customerType(sc.Header.Profile), Tags: featureTags(sc.Header.Tags, sc.Header.FBRC, vocabulary), FBRC: sc.Header.FBRC})
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
		items = append(items, CatalogItem{Kind: "profile", Ref: name, Title: name, Customer: customerType(name),
			Description: fmt.Sprintf("Customer profile with %d stubs on top of the shared defaults.", len(stubs))})
	}

	for _, f := range flows {
		title := f.Header.Name
		if title == "" {
			title = f.Rel
		}
		items = append(items, CatalogItem{Kind: "flow", Ref: f.Path, Title: title, Description: f.Header.Description,
			Group: f.Feature, Profile: f.Header.Profile, Customer: customerType(f.Header.Profile), Tags: featureTags(f.Header.Tags, f.Header.FBRC, vocabulary), FBRC: f.Header.FBRC})
	}

	sort.SliceStable(items, func(i, j int) bool {
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

// StubInfo says which call a stub answers.
type StubInfo struct {
	Name   string `json:"name"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Preview is what a mock would do, read from its files without starting it.
type Preview struct {
	Kind         string            `json:"kind"`
	Ref          string            `json:"ref"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Profile      string            `json:"profile,omitempty"`
	Customer     string            `json:"customer"`
	FBRC         map[string]string `json:"fbrc"`
	Stubs        []StubInfo        `json:"stubs"`        // what this target adds on top of its customer
	DefaultStubs int               `json:"defaultStubs"` // what the customer profile and shared defaults already answer
	Steps        []Step            `json:"steps,omitempty"`
	Checks       []LiveCheck       `json:"checks,omitempty"`
}

// PreviewOf reads one catalog item's steps, checks, extra stubs and Remote Config.
func PreviewOf(p *project.Project, it CatalogItem) (Preview, error) {
	s := fixtures.New(p)
	pv := Preview{Kind: it.Kind, Ref: it.Ref, Title: it.Title, Description: it.Description, Profile: it.Profile,
		Customer: it.Customer, FBRC: maps.Clone(it.FBRC), Stubs: []StubInfo{}}
	if pv.FBRC == nil {
		pv.FBRC = map[string]string{}
	}
	var stubs []string
	var header *fixtures.Header
	switch it.Kind {
	case "flow":
		f, err := s.Flow(it.Ref)
		if err != nil {
			return pv, err
		}
		header, pv.Steps = f.Header, FlowSteps(f.Path)
	case "scenario":
		sc, err := s.FindScenario(it.Ref)
		if err != nil {
			return pv, err
		}
		if sc != nil {
			header = sc.Header
		}
	case "profile":
		names, err := s.ProfileMappings(it.Ref)
		if err != nil {
			return pv, err
		}
		stubs = names
	}
	if header != nil {
		stubs = header.Overrides
		pv.Checks = ChecksFor(header.Verifications)
	}
	for _, name := range stubs {
		pv.Stubs = append(pv.Stubs, stubInfo(s, name))
	}
	if it.Kind != "profile" && it.Profile != "" {
		if names, err := s.ProfileMappings(it.Profile); err == nil {
			pv.DefaultStubs = len(names)
		}
	}
	if shared, err := s.ProfileMappings("shared"); err == nil && it.Kind != "profile" {
		pv.DefaultStubs += len(shared)
	}
	return pv, nil
}

func stubInfo(s *fixtures.Store, name string) StubInfo {
	info := StubInfo{Name: name}
	stub, err := s.ResolvedStub(name)
	if err != nil {
		return info
	}
	if req := stub.Object("request"); req != nil {
		info.Method = req.String("method")
		for _, k := range []string{"urlPath", "urlPathPattern", "url", "urlPattern"} {
			if v := req.String(k); v != "" {
				info.Path = v
				break
			}
		}
	}
	return info
}
