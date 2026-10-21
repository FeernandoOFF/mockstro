package web

import (
	"fmt"
	"slices"
	"sort"
	"strings"

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
