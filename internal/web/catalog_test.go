package web

import (
	"slices"
	"testing"

	"github.com/feernandooff/mockstro/internal/project"
)

func TestFeatureTagsUseOnlyFixtureTagsAndExplicitKeyMetadata(t *testing.T) {
	tags := []string{"smoke", "membership", "smoke"}
	keys := map[string]string{
		"feedback.enabled":          "true",
		"feedback.rate":             "100",
		"Config_Membership_Enabled": "true",
		"unmapped":                  "true",
	}
	metadata := map[string]string{
		"feedback.enabled": "Feedback",
		"feedback.rate":    "Feedback",
		"absent.key":       "unused",
		"unmapped":         "",
	}
	if got, want := featureTags(tags, keys, metadata), []string{"Feedback", "membership", "smoke"}; !slices.Equal(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
	if !slices.Equal(tags, []string{"smoke", "membership", "smoke"}) {
		t.Fatalf("fixture tags mutated: %v", tags)
	}
	if got := featureTags(nil, keys, nil); len(got) != 0 {
		t.Fatalf("keys without metadata must not invent tags: %v", got)
	}
}

func TestCatalogSupportsProjectGroupsAndKeepsUnknownProfilesVisible(t *testing.T) {
	_, p := testServer(t)
	p.Catalog = project.CatalogConfig{
		ProfileGroups: map[string]string{"member": "Members", "staff": "Team", "admin": "Team"},
		GroupOrder:    []string{"Team", "unused", "Members"},
		FeatureTags:   map[string]string{"survey.enabled": "feedback"},
	}
	for _, name := range []string{"staff", "admin", "visitor"} {
		write(t, p.MaestroRoot, "wiremock/profiles/"+name+".json", `{"mappings": []}`)
	}
	write(t, p.MaestroRoot, "scenarios/visitor.yaml", "tags: [visitor, smoke]\nproperties:\n  apiProfile: visitor\n")
	write(t, p.MaestroRoot, "flows/plain.yaml", "name: Plain\ntags: [regression, arbitrary-tag]\n---\n- launchApp\n")
	items, err := Catalog(p)
	if err != nil {
		t.Fatal(err)
	}
	var groups []string
	for _, it := range items {
		if !slices.Contains(groups, it.ProfileGroup) {
			groups = append(groups, it.ProfileGroup)
		}
	}
	if want := []string{"Team", "Members", "Other", "visitor"}; !slices.Equal(groups, want) {
		t.Fatalf("groups = %v, want %v (unlisted groups must remain)", groups, want)
	}
	for _, name := range []string{"staff", "admin"} {
		if it, ok := findItem(items, "profile", name); !ok || it.ProfileGroup != "Team" {
			t.Errorf("profile %s = %+v, found %v", name, it, ok)
		}
	}
	if it, ok := findItem(items, "scenario", "visitor"); !ok || it.ProfileGroup != "visitor" || !slices.Equal(it.Tags, []string{"smoke", "visitor"}) {
		t.Errorf("unmapped scenario = %+v, found %v", it, ok)
	}
	if it, ok := findItem(items, "flow", p.FlowsRoot()+"/plain.yaml"); !ok || it.ProfileGroup != "Other" || !slices.Equal(it.Tags, []string{"arbitrary-tag", "regression"}) {
		t.Errorf("ungrouped flow = %+v, found %v", it, ok)
	}
}

func TestCatalogDefaultsToProfileNamesAndFixtureTags(t *testing.T) {
	_, p := testServer(t)
	p.Catalog = project.CatalogConfig{}
	items, err := Catalog(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ProfileGroup != "member" {
			t.Errorf("default group = %q, want member", it.ProfileGroup)
		}
		if it.Kind == "scenario" && len(it.Tags) != 0 {
			t.Errorf("unexpected inferred tags: %v", it.Tags)
		}
		if it.Kind == "flow" && !slices.Equal(it.Tags, []string{"regression", "survey"}) {
			t.Errorf("explicit tags = %v", it.Tags)
		}
	}
}
