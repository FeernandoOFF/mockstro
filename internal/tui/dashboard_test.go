package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
)

func TestDashboardAgentEmulatorsSortFirstAndKeepSelection(t *testing.T) {
	a := testApp(t)
	st := &engine.Status{
		Devices: []engine.StatusDevice{
			{Device: platform.Device{Serial: "personal", Name: "A personal", Class: "emulator"}},
			{Device: platform.Device{Serial: "agent", Name: "Z agent", Class: "emulator"}, Designated: true},
		},
		Emulators: []engine.StatusEmulator{
			{Platform: "android", Name: "A personal"},
			{Platform: "ios", Name: "Z agent", Designated: true},
			{Platform: "android", Name: "Z global", GlobalDesignated: true},
		},
	}
	a.Update(statusMsg{st: st})
	if st.Devices[0].Serial != "agent" || st.Emulators[0].Name != "Z global" || st.Emulators[1].Name != "Z agent" || st.Emulators[2].Name != "A personal" {
		t.Fatal("agent emulators did not sort first in both sections", st)
	}
	a.dash.cursor = len(st.Devices) + 2
	selected := a.dash.selectionID()
	a.Update(agentAccessMsg{p: a.p, platform: "android", name: "A personal", allow: true})
	if st.Emulators[0].Name != "A personal" || a.dash.cursor != len(st.Devices) || a.dash.selectionID() != selected {
		t.Fatal("enabling agents did not move the emulator and retain selection")
	}
	a.Update(agentAccessMsg{p: a.p, platform: "android", name: "A personal", allow: false})
	if st.Emulators[2].Name != "A personal" || a.dash.selectionID() != selected {
		t.Fatal("disabling agents changed the selected emulator")
	}
	// A poll can return a different discovery order; selection must stay on the same device.
	next := *st
	next.Emulators = slices.Clone(st.Emulators)
	slices.Reverse(next.Emulators)
	a.Update(statusMsg{st: &next})
	if a.dash.selectionID() != selected || next.Emulators[2].Name != "A personal" {
		t.Fatal("status refresh changed selection or agent ordering")
	}
	st.Emulators = []engine.StatusEmulator{
		{Platform: "ios", Name: "iPad", Serial: "old", OSVersion: "iOS 26.0"},
		{Platform: "ios", Name: "iPad", Serial: "new", OSVersion: "iOS 26.3.1"},
	}
	a.status = st
	a.dash.cursor = len(st.Devices)
	old := a.dash.selectionID()
	a.dash.cursor++
	if a.dash.selectionID() == old {
		t.Fatal("same-name simulator versions share a selection identity")
	}
}

func TestDashboardAgentAccessPersistsAndReloads(t *testing.T) {
	a := testApp(t)
	write(t, filepath.Join(a.p.MaestroRoot, "mockstro.yaml"), `platforms:
  android:
    appId: x
    integration: full
    devices: [Pixel]
  ios:
    appId: x
    integration: full
`)
	p, err := project.Load(a.p.Root)
	if err != nil {
		t.Fatal(err)
	}
	a.p, a.store = p, fixtures.New(p)
	a.opts.Platform = ""
	fake := a.query.Exec.(*execx.Fake)
	fake.Handler = func(c execx.Cmd) (string, error) {
		if strings.Contains(c.String(), "ro.boot.qemu.avd_name") {
			return "Pixel", nil
		}
		return "", nil
	}
	a.query = engine.New(p, a.opts, fake, nil)
	a.status.Devices[0].Platform = "android"
	a.status.Emulators = []engine.StatusEmulator{{Platform: "ios", Name: "iPhone Dev"}}
	oldQuery := a.query
	oldStatus := *a.status
	oldStatus.Devices = append([]engine.StatusDevice(nil), a.status.Devices...)
	pressA := func() {
		t.Helper()
		cmd := a.dash.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		if cmd == nil {
			t.Fatal("agent access action missing")
		}
		msg := cmd().(agentAccessMsg)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		a.Update(msg)
	}
	pressA() // Remove the running Android emulator from the project's designation.
	if a.status.Devices[0].Designated || len(a.p.Targets["android"].Devices) != 0 || a.dash.savingAgents {
		t.Fatal("saved designation was not applied immediately")
	}
	o := a.opts
	o.Owner, o.Platform = "agent", "android"
	if engine.New(a.p, o, fake, nil).Allowed(context.Background(), "emulator-5554") {
		t.Fatal("query still uses the old agent configuration")
	}
	a.Update(statusMsg{st: &oldStatus, query: oldQuery})
	if a.status.Devices[0].Designated {
		t.Fatal("in-flight status restored stale designation")
	}
	a.dash.cursor = 1 // The stopped iOS simulator, despite the default query being Android.
	pressA()
	if !slices.Equal(a.p.Targets["ios"].Devices, []string{"iPhone Dev"}) || len(a.p.Targets["android"].Devices) != 0 || !a.status.Emulators[0].Designated {
		t.Fatal("edited the wrong platform")
	}
	pressA()
	if len(a.p.Targets["ios"].Devices) != 0 {
		t.Fatal("could not toggle the stopped simulator back off")
	}
}

func TestDashboardAgentAccessGuards(t *testing.T) {
	for _, kind := range []string{"phone", "agent-owner", "global-designation"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			switch kind {
			case "phone":
				a.status.Devices[0].Class = "physical"
			case "agent-owner":
				a.query.Owner = "agent"
			case "global-designation":
				a.status.Devices[0].GlobalDesignated = true
			}
			if cmd := a.dash.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}); cmd != nil || a.dash.savingAgents {
				t.Fatal("offered an unsafe designation edit")
			}
			if strings.Contains(a.dash.selectionKeys(), "a allow") || strings.Contains(a.dash.selectionKeys(), "a disable") {
				t.Fatal("offered an unavailable agent access action")
			}
		})
	}
}

func TestDashboardScrollsAllInstalledEmulators(t *testing.T) {
	a := testApp(t)
	for i := range 40 {
		a.status.Emulators = append(a.status.Emulators, engine.StatusEmulator{Platform: "android", Name: fmt.Sprintf("Personal_%02d", i)})
	}
	a.dash.cursor = len(a.status.Devices) + len(a.status.Emulators) - 1
	v := a.View()
	if !strings.Contains(v, "Personal_39") || strings.Contains(v, "Personal_00") || !strings.Contains(v, "agents: no") || !strings.Contains(v, "a allow agents") {
		t.Fatalf("selected emulator is not visible/actionable:\n%s", v)
	}
	a.query.Owner = "agent"
	if strings.Contains(a.dash.selectionKeys(), "boot") {
		t.Fatal("an agent can boot a non-designated emulator from the inventory")
	}
}

func TestDashboardDesignationErrorPreservesCurrentConfig(t *testing.T) {
	a := testApp(t)
	old := a.p
	a.dash.savingAgents = true
	a.Update(agentAccessMsg{err: fmt.Errorf("config is read-only")})
	if a.p != old || a.dash.savingAgents || !strings.Contains(a.activity[len(a.activity)-1].text, "read-only") {
		t.Fatal("failed save changed in-memory config or left the action stuck")
	}
	// A failed save remains retryable.
	a.status.Emulators = []engine.StatusEmulator{{Platform: "android", Name: "Pixel_B"}}
	a.dash.cursor = len(a.status.Devices)
	if !strings.Contains(a.dash.selectionKeys(), "a allow agents") {
		t.Fatal("failed save disabled retry")
	}
}
