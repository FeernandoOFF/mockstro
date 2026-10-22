package web

import (
	"fmt"
	"slices"
	"sort"

	"github.com/feernandooff/mockstro/internal/fixtures"
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
