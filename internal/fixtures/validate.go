package fixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/feernandooff/mockstro/internal/ojson"
)

type Counts struct {
	Stubs, Bodies, Profiles, Scenarios, Flows int
}

func (c Counts) String() string {
	return fmt.Sprintf("Validated %d stubs, %d bodies, %d profiles, %d scenarios, and %d flows.",
		c.Stubs, c.Bodies, c.Profiles, c.Scenarios, c.Flows)
}

// ValidateAll checks every fixture; flows are skipped when includeFlows is false (mock).
func (s *Store) ValidateAll(includeFlows bool) (Counts, error) {
	var c Counts
	for _, dir := range []string{s.P.StubsRoot(), s.P.BodiesRoot(), s.P.ProfilesRoot()} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return c, fmt.Errorf("%s does not exist", dir)
		}
	}
	if includeFlows {
		if st, err := os.Stat(s.P.FlowsRoot()); err != nil || !st.IsDir() {
			return c, fmt.Errorf("%s does not exist", s.P.FlowsRoot())
		}
		if _, err := os.Stat(s.P.ConfigPath()); err != nil {
			return c, fmt.Errorf("%s does not exist", s.P.ConfigPath())
		}
	}

	names, err := s.StubNames()
	if err != nil {
		return c, err
	}
	for _, name := range names {
		resolved, err := s.ResolvedStub(name)
		if err != nil {
			return c, err
		}
		if err := s.ValidateStub(resolved, name, s.StubPath(name)); err != nil {
			return c, err
		}
		c.Stubs++
	}
	if c.Stubs == 0 {
		return c, fmt.Errorf("no WireMock stubs found")
	}

	bodies, _ := listFiles(s.P.BodiesRoot(), ".json")
	for _, b := range bodies {
		if _, err := ojson.ParseFile(b); err != nil {
			return c, fmt.Errorf("invalid JSON body in %s", s.P.Rel(b))
		}
		c.Bodies++
	}

	profiles, _ := listFiles(s.P.ProfilesRoot(), ".json")
	for _, f := range profiles {
		if err := s.validateProfile(strings.TrimSuffix(filepath.Base(f), ".json")); err != nil {
			return c, err
		}
		c.Profiles++
	}
	if c.Profiles == 0 {
		return c, fmt.Errorf("no WireMock profiles found")
	}
	if !s.ProfileExists("shared") {
		return c, fmt.Errorf("missing shared WireMock profile")
	}

	if includeFlows {
		flows, err := s.Flows()
		if err != nil {
			return c, err
		}
		for _, f := range flows {
			if err := s.ValidateFlow(f); err != nil {
				return c, err
			}
			c.Flows++
		}
		if c.Flows == 0 {
			return c, fmt.Errorf("no Maestro flows found")
		}
	}

	scenarios, err := s.Scenarios()
	if err != nil {
		return c, err
	}
	for _, sc := range scenarios {
		if err := s.ValidateScenario(sc); err != nil {
			return c, err
		}
		c.Scenarios++
	}
	return c, nil
}

func (s *Store) validateProfile(name string) error {
	mappings, err := s.ProfileMappings(name)
	if err != nil {
		return err
	}
	for _, m := range mappings {
		if !s.StubExists(m) {
			return fmt.Errorf("profile '%s' references missing stub '%s'", name, m)
		}
	}
	return s.ValidateLayerDuplicates(fmt.Sprintf("profile '%s'", name), mappings)
}

func (s *Store) ValidateFlow(f *Flow) error {
	rel := s.P.Rel(f.Path)
	h := f.Header
	if err := h.validateProperties(rel); err != nil {
		return err
	}
	if !s.ProfileExists(h.Profile) {
		return fmt.Errorf("unknown apiProfile '%s' in %s", h.Profile, rel)
	}
	for _, o := range h.Overrides {
		if !s.StubExists(o) {
			return fmt.Errorf("flow %s references missing stub '%s'", rel, o)
		}
	}
	if err := s.ValidateLayerDuplicates("flow "+rel+" overrides", h.Overrides); err != nil {
		return err
	}
	if err := h.validateVerifications(rel); err != nil {
		return err
	}
	if len(h.Verifications) == 0 {
		return nil
	}
	active := map[string]bool{}
	for _, o := range h.Overrides {
		active[o] = true
	}
	for _, layer := range []string{h.Profile, "shared"} {
		ms, err := s.ProfileMappings(layer)
		if err != nil {
			return err
		}
		for _, m := range ms {
			active[m] = true
		}
	}
	for _, v := range h.Verifications {
		if !s.StubExists(v.Stub) {
			return fmt.Errorf("flow %s verifies missing stub '%s'", rel, v.Stub)
		}
		if !active[v.Stub] {
			return fmt.Errorf("flow %s verifies inactive stub '%s'", rel, v.Stub)
		}
	}
	return nil
}

func (s *Store) ValidateScenario(sc *Scenario) error {
	rel := s.P.Rel(sc.Path)
	h := sc.Header
	if h.HasSteps {
		return fmt.Errorf("scenario %s must not declare Maestro steps", rel)
	}
	if h.Description == "" {
		return fmt.Errorf("missing description in %s", rel)
	}
	if err := h.validateProperties(rel); err != nil {
		return err
	}
	if h.Profile == "shared" || !s.ProfileExists(h.Profile) {
		return fmt.Errorf("unknown apiProfile '%s' in %s", h.Profile, rel)
	}
	for _, o := range h.Overrides {
		if !s.StubExists(o) {
			return fmt.Errorf("scenario %s references missing stub '%s'", rel, o)
		}
	}
	return s.ValidateLayerDuplicates("scenario "+rel+" overrides", h.Overrides)
}
