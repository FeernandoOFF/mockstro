package cli

import (
	"fmt"
	"strings"

	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/ojson"
)

func listCmd(g *globals, f *runFlags, args []string) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	s := fixtures.New(p)
	if _, err := s.ValidateAll(true); err != nil {
		return err
	}
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	if f.showFallbk && target != "profiles" {
		return fmt.Errorf("--show-fallback is only valid with 'list profiles'")
	}
	switch target {
	case "":
		flows, err := s.Flows()
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %s %s %s\n", fit("Flow", 46), fit("Tags", 38), fit("Profile", 12), "API", "FBRC")
		fmt.Println(strings.Repeat("-", 110))
		for _, fl := range flows {
			tags := strings.Join(fl.Header.Tags, ",")
			if tags == "" {
				tags = "-"
			}
			fmt.Printf("%s %s %s %-3d %d\n", fit(fl.Rel, 46), fit(tags, 38), fit(fl.Header.Profile, 12), len(fl.Header.Overrides), len(fl.Header.FBRC))
		}
	case "profiles":
		profiles, _ := s.ProfileNames()
		shared, _ := s.ProfileMappings("shared")
		flows, _ := s.Flows()
		for _, prof := range profiles {
			fmt.Printf("Profile: %s\n  Shared defaults (priority 5):\n", prof)
			for _, m := range shared {
				fmt.Printf("    - %s\n", m)
			}
			fmt.Println("  Profile defaults (priority 3):")
			pm, _ := s.ProfileMappings(prof)
			for _, m := range pm {
				fmt.Printf("    - %s\n", m)
			}
			fmt.Println("  Flows (explicit overrides at priority 1):")
			any := false
			for _, fl := range flows {
				if fl.Header.Profile != prof {
					continue
				}
				any = true
				ov := strings.Join(fl.Header.Overrides, ", ")
				if ov == "" {
					ov = "-"
				}
				fmt.Printf("    - %s: %s\n", fl.Rel, ov)
			}
			if !any {
				fmt.Println("    - none")
			}
			fmt.Println()
		}
		if f.showFallbk {
			fmt.Println("\nFallback (priority 100):\n  Matcher: ANY urlPattern=.*\n  Response: HTTP 599 {\"error\":\"No WireMock stub matched this request\"}")
		}
	case "scenarios":
		scs, err := s.Scenarios()
		if err != nil {
			return err
		}
		if len(scs) == 0 {
			fmt.Println("No scenarios defined.")
			return nil
		}
		fmt.Printf("%s %s %s %s\n", fit("Scenario", 28), fit("Profile", 14), fit("API", 5), "FBRC")
		fmt.Println(strings.Repeat("-", 110))
		for _, sc := range scs {
			fmt.Printf("%s %s %s %d\n", fit(sc.Name, 28), fit(sc.Header.Profile, 14), fit(fmt.Sprint(len(sc.Header.Overrides)), 5), len(sc.Header.FBRC))
			fmt.Printf("  %s\n", sc.Header.Description)
		}
	default:
		return fmt.Errorf("unknown list target: %s (expected 'profiles' or 'scenarios')", target)
	}
	return nil
}

func resolveCmd(g *globals, name string) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	stub, err := fixtures.New(p).ResolvedStub(name)
	if err != nil {
		return err
	}
	b, err := ojson.MarshalIndent(stub)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
