package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
)

func TestIOSWorkerAllocationCountsOnlyBootedSimulators(t *testing.T) {
	for _, tc := range []struct {
		name, parallel string
		states         []string
		workers, boots int
	}{
		{"auto cold", "auto", []string{"Shutdown", "Shutdown"}, 1, 1},
		{"auto one ready", "auto", []string{"Shutdown", "Booted"}, 1, 0},
		{"auto both ready", "auto", []string{"Booted", "Booted"}, 2, 0},
		{"explicit one", "1", []string{"Shutdown", "Shutdown"}, 1, 1},
		{"explicit two", "2", []string{"Shutdown", "Shutdown"}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProject(t)
			p.Platform, p.IOSProject, p.IOSScheme = "ios", "App.xcodeproj", "App"
			p.IOSSimNames = []string{"iPhone A", "iPhone B"}
			states := slices.Clone(tc.states)
			var booted []string
			stop := errors.New("stop after device preparation")
			fake := &execx.Fake{Handler: func(c execx.Cmd) (string, error) {
				switch {
				case c.Name == "xcodebuild":
					return "", stop
				case c.Name == "xcrun" && len(c.Args) > 1 && c.Args[1] == "list":
					var sims []map[string]any
					for i, state := range states {
						sims = append(sims, map[string]any{"udid": fmt.Sprint(i), "name": p.IOSSimNames[i], "state": state, "isAvailable": true})
					}
					b, _ := json.Marshal(map[string]any{"devices": map[string]any{"iOS": sims}})
					return string(b), nil
				case c.Name == "xcrun" && len(c.Args) > 2 && c.Args[1] == "boot":
					booted = append(booted, c.Args[2])
					for i := range states {
						if fmt.Sprint(i) == c.Args[2] {
							states[i] = "Booted"
						}
					}
				}
				return "", nil
			}}
			o := DefaultOptions()
			o.Owner, o.Platform, o.Parallel, o.Headless = "agent", "ios", tc.parallel, true
			var logs []string
			e := New(p, o, fake, func(ev Event) { logs = append(logs, ev.Text) })
			// Stop at the build: this exercises planning and the platform's boot requests
			// without installing an app or launching Maestro.
			s := e.runParallel(context.Background(), []*fixtures.Flow{{}, {}}, time.Now())
			if s.Err == nil || !strings.Contains(s.Err.Error(), "xcodebuild failed") {
				t.Fatalf("did not stop at build: %v", s.Err)
			}
			want := fmt.Sprintf("Running 2 flow(s) across %d worker(s).", tc.workers)
			if !slices.Contains(logs, want) || len(booted) != tc.boots {
				t.Fatalf("want %d workers, %d boots; booted %v, logs %v", tc.workers, tc.boots, booted, logs)
			}
			// A ready simulator must win over a shut-down one listed before it.
			if tc.name == "auto one ready" {
				locks := e.newLocks("selection")
				defer locks.ReleaseAll()
				serial, err := e.SelectDevice(context.Background(), locks)
				if err != nil || serial != "1" {
					t.Fatalf("selected %q, %v; wanted existing booted simulator", serial, err)
				}
			}
		})
	}
}
