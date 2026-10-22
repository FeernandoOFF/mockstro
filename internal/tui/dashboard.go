package tui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/paths"
	"github.com/feernandooff/mockstro/internal/project"
)

// dashModel: devices, bootable AVDs/simulators, active runs across every project, activity.
type dashModel struct {
	a            *app
	cursor       int // over devices, then bootable
	booting      string
	confirmSweep bool
	confirmProxy string // serial awaiting confirmation
	confirmStop  string
	clearing     string
	savingAgents bool
}

type agentAccessMsg struct {
	p                    *project.Project
	path, name, platform string
	allow                bool
	err                  error
}

type keyboardSetMsg struct {
	serial, state string
	err           error
}

type deviceStoppedMsg struct {
	serial string
	err    error
}

type proxyClearedMsg struct {
	serial, was string
	err         error
}

func (d *dashModel) key(k tea.KeyMsg) tea.Cmd {
	a := d.a
	if d.confirmProxy != "" {
		serial := d.confirmProxy
		d.confirmProxy = ""
		if !confirmed(k) {
			return nil
		}
		d.clearing = serial
		was := ""
		if dv := d.device(serial); dv != nil {
			was = dv.Proxy
		}
		query := a.deviceQuery(serial)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return proxyClearedMsg{serial, was, query.Plat.ClearProxy(ctx, serial)}
		}
	}
	if d.confirmStop != "" {
		serial := d.confirmStop
		d.confirmStop = ""
		if !confirmed(k) {
			return nil
		}
		d.clearing = serial
		a.log("info", "stopping %s…", serial)
		query := a.deviceQuery(serial)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return deviceStoppedMsg{serial, query.Plat.Shutdown(ctx, serial)}
		}
	}
	if d.confirmSweep {
		d.confirmSweep = false
		if confirmed(k) {
			query := a.query
			return func() tea.Msg {
				return sweepDoneMsg(query.Sweep(context.Background(), false))
			}
		}
		return nil
	}
	boot := d.bootables()
	nDev := 0
	if a.status != nil {
		nDev = len(a.status.Devices)
	}
	if c, ok := navKey(k.String(), d.cursor, nDev+len(boot)); ok {
		d.cursor = c
		return nil
	}
	switch k.String() {
	case "a":
		if a.query.Owner != "human" || d.savingAgents {
			return nil
		}
		var selected engine.StatusEmulator
		if d.cursor < nDev {
			device := a.status.Devices[d.cursor]
			if device.Class != "emulator" {
				return nil
			}
			selected = engine.StatusEmulator{Platform: device.Platform, Name: device.Name, Designated: device.Designated, GlobalDesignated: device.GlobalDesignated}
		} else if i := d.cursor - nDev; i >= 0 && i < len(boot) {
			selected = boot[i]
		}
		if selected.Name == "" {
			a.log("warn", "emulator name is unavailable; refresh devices first")
			return nil
		}
		if selected.GlobalDesignated {
			a.log("warn", "%s is designated globally; edit %s to remove its agent access", selected.Name, paths.DesignatedFile())
			return nil
		}
		if selected.Platform == "" {
			selected.Platform = a.query.Opts.Platform
		}
		root, allow := a.p.Root, !selected.Designated
		d.savingAgents = true
		return func() tea.Msg {
			p, path, err := project.SetAgentDevice(root, selected.Platform, selected.Name, allow)
			return agentAccessMsg{p: p, path: path, name: selected.Name, platform: selected.Platform, allow: allow, err: err}
		}
	case "p":
		if d.cursor < nDev {
			dv := a.status.Devices[d.cursor]
			switch {
			case dv.Proxy == "":
				a.log("info", "%s has no device-wide proxy", dv.Serial)
			case dv.LockedBy != nil:
				a.log("warn", "%s is held by run %s; not touching its proxy", dv.Serial, *dv.LockedBy)
			default:
				d.confirmProxy = dv.Serial
			}
		}
	case "x":
		if d.cursor < nDev {
			dv := a.status.Devices[d.cursor]
			switch {
			case dv.LockedBy != nil:
				a.log("warn", "%s is held by run %s; not stopping it", dv.Serial, *dv.LockedBy)
			case dv.Class != "emulator":
				a.log("warn", "%s is a physical device; not stopping it", dv.Serial)
			default:
				d.confirmStop = dv.Serial
			}
		}
	case "v":
		if d.cursor < nDev {
			dv := a.status.Devices[d.cursor]
			switch {
			case dv.LockedBy != nil:
				a.log("warn", "%s is held by run %s; not changing its keyboard", dv.Serial, *dv.LockedBy)
			case dv.Keyboard == "":
				a.log("warn", "%s: keyboard setting not supported", dv.Serial)
			default:
				show := dv.Keyboard == "hidden"
				state := map[bool]string{true: "shown", false: "hidden"}[show]
				d.clearing = dv.Serial
				a.log("info", "setting on-screen keyboard %s on %s…", state, dv.Serial)
				query := a.deviceQuery(dv.Serial)
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					return keyboardSetMsg{dv.Serial, state, query.Plat.SetKeyboard(ctx, dv.Serial, show)}
				}
			}
		}
	case "b", "B", "enter":
		i := d.cursor - nDev
		if d.booting != "" || i < 0 || i >= len(boot) {
			return nil
		}
		item := boot[i]
		name := item.Name
		bootID := emulatorID(item)
		query := a.query
		if item.Platform != "" {
			o := a.opts
			o.Platform = item.Platform
			query = engine.New(a.p, o, a.query.Exec, nil)
		}
		if query.Owner != "human" && !item.Designated {
			a.log("warn", "%s is not available to agents", name)
			return nil
		}
		headless := k.String() == "B"
		d.booting = bootID
		mode := ""
		if headless {
			mode = " headless"
		}
		a.log("info", "booting %s%s…", name, mode)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			serial, err := query.Plat.Boot(ctx, bootID, headless, func(s string) { a.prog.Send(activityMsg{"info", s}) })
			return bootDoneMsg{name, serial, err}
		}
	case "S":
		d.confirmSweep = true
	}
	return nil
}

func (d *dashModel) view(w, h int) (string, string) {
	a := d.a
	st := a.status
	leftW := w / 2
	rightW := w - leftW
	topH := h * 3 / 5
	botH := h - topH

	var dev strings.Builder
	selectedLine := 0
	if st == nil {
		dev.WriteString(sMuted.Render("probing devices…"))
	} else {
		if len(st.Devices) == 0 {
			dev.WriteString(sMuted.Render("no devices attached") + "\n")
		}
		for i, x := range st.Devices {
			cur := "  "
			if i == d.cursor {
				selectedLine = strings.Count(dev.String(), "\n")
				cur = sSel.Render("› ")
			}
			state := sOK.Render("free")
			if x.LockedBy != nil {
				state = sWarn.Render("locked " + *x.LockedBy)
			} else if !x.Eligible {
				state = sMuted.Render("not eligible")
			}
			des := ""
			if x.Class == "emulator" {
				des = agentLabel(x.Designated, x.GlobalDesignated)
			}
			dev.WriteString(dashboardDeviceRow(cur, strings.TrimPrefix(x.Platform+"/"+x.Name, "/"), x.OSVersion, des, leftW-4) + "\n")
			fmt.Fprintf(&dev, "    %s  %s", pad(x.Serial, 24), state)
			switch x.Keyboard {
			case "shown", "hidden":
				dev.WriteString(sMuted.Render("  ⌨ keyboard " + x.Keyboard))
			}
			dev.WriteString("\n")
			switch {
			case x.Serial == d.clearing:
				dev.WriteString(sWarn.Render("    applying…") + "\n")
			case x.Proxy != "":
				dev.WriteString(sBad.Render("    ⚠ proxy "+x.Proxy) + sMuted.Render("  (p to clear)") + "\n")
			}
		}
		dev.WriteString("\n" + sBold.Render("Stopped emulators / simulators") + "\n")
		boot := d.bootables()
		if len(boot) == 0 {
			dev.WriteString(sMuted.Render("none installed or all are running") + "\n")
		}
		for i, item := range boot {
			cur := "  "
			if i+len(st.Devices) == d.cursor {
				selectedLine = strings.Count(dev.String(), "\n")
				cur = sSel.Render("› ")
			}
			name := strings.TrimPrefix(item.Platform+"/"+item.Name, "/")
			if emulatorID(item) == d.booting {
				name += sWarn.Render("  booting…")
			}
			dev.WriteString(dashboardDeviceRow(cur, name, item.OSVersion, agentLabel(item.Designated, item.GlobalDesignated), leftW-4) + "\n")
		}
	}

	var runs strings.Builder
	if st == nil || st.ActiveCount == 0 {
		runs.WriteString(sMuted.Render("No Maestro run is active (any project)."))
	} else {
		for _, r := range st.Runs {
			fmt.Fprintf(&runs, "%s %s\n", sBold.Render(r.RunID), sMuted.Render(r.Owner))
			fmt.Fprintf(&runs, "  %s · port %d · %s %s\n", d2(r.Device), r.Port, sWarn.Render(d2(r.Stage)), r.Flow)
			if r.Worktree != "" {
				fmt.Fprintf(&runs, "  %s\n", sMuted.Render(r.Worktree))
			}
		}
	}

	var info strings.Builder
	targets, _ := a.p.Selected(a.opts.Platform)
	for _, target := range targets {
		fmt.Fprintf(&info, "%s: %s · %s\n", target.Platform, target.AppID, target.IntegrationLabel())
		if target.Lite() {
			fmt.Fprintf(&info, "Build      %s\n", sMuted.Render("none: runs use the installed app"))
		} else if target.Build.System == "command" {
			fmt.Fprintf(&info, "Build      %s\n", strings.Join(target.Build.Command, " "))
		} else if target.Build.System == "kotlin-toolchain" {
			fmt.Fprintf(&info, "Build      Kotlin Toolchain (%s)\n", target.Build.Variant)
		} else if target.Platform == "ios" {
			fmt.Fprintf(&info, "Scheme     %s (%s)\n", target.Build.Scheme, target.Build.Configuration)
		} else {
			fmt.Fprintf(&info, "Build      %s\n", target.Build.Task)
		}
	}
	if a.fixErr != nil {
		fmt.Fprintf(&info, "Fixtures   %s\n", sBad.Render(a.fixErr.Error()))
	} else {
		c := a.counts
		fmt.Fprintf(&info, "Fixtures   %d flows · %d scenarios · %d profiles · %d stubs\n", c.Flows, c.Scenarios, c.Profiles, c.Stubs)
	}
	fmt.Fprintf(&info, "State      %s\n", sMuted.Render(paths.StateRoot()))

	var act strings.Builder
	start := len(a.activity) - (botH - 3)
	if start < 0 {
		start = 0
	}
	for _, x := range a.activity[start:] {
		line := x.at.Format("15:04:05") + " " + x.text
		switch x.level {
		case "warn":
			line = sWarn.Render(line)
		case "error":
			line = sBad.Render(line)
		}
		act.WriteString(line + "\n")
	}

	top := lipgloss.JoinHorizontal(lipgloss.Top,
		panel("Devices", dashboardWindow(dev.String(), selectedLine, topH-3), leftW, topH),
		lipgloss.JoinVertical(lipgloss.Left,
			panel("Project", info.String(), rightW, 7),
			panel("Active runs (all projects)", runs.String(), rightW, topH-7)))
	body := lipgloss.JoinVertical(lipgloss.Left, top, panel("Activity", act.String(), w, botH))
	help := navHelp + d.selectionKeys() + " · S sweep stale locks"
	if d.confirmProxy != "" {
		help = sWarn.Render(fmt.Sprintf("Remove the device-wide proxy on %s (Proxyman/Charles setting)? "+confirmHint, d.confirmProxy))
	}
	if d.confirmStop != "" {
		help = sWarn.Render(fmt.Sprintf("Stop emulator %s? "+confirmHint, d.confirmStop))
	}
	if d.confirmSweep {
		help = sWarn.Render("Reclaim locks whose owning process is gone? " + confirmHint)
	}
	return body, help
}

func d2(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (d *dashModel) device(serial string) *engine.StatusDevice {
	if d.a.status == nil {
		return nil
	}
	for i := range d.a.status.Devices {
		if d.a.status.Devices[i].Serial == serial {
			return &d.a.status.Devices[i]
		}
	}
	return nil
}

// selectionKeys lists only the actions that apply to what the cursor is on.
func (d *dashModel) selectionKeys() string {
	st := d.a.status
	if st == nil {
		return ""
	}
	if d.cursor < len(st.Devices) {
		dv := st.Devices[d.cursor]
		keys := ""
		if dv.Class == "emulator" {
			keys = d.agentKey(dv.Designated, dv.GlobalDesignated)
		}
		if dv.LockedBy != nil {
			return keys
		}
		if dv.Proxy != "" {
			keys += " · p clear device proxy"
		}
		if dv.Keyboard == "shown" || dv.Keyboard == "hidden" {
			keys += " · v toggle keyboard"
		}
		if dv.Class == "emulator" {
			keys += " · x stop emulator"
		}
		return keys
	}
	if i := d.cursor - len(st.Devices); i >= 0 && i < len(d.bootables()) {
		item := d.bootables()[i]
		keys := d.agentKey(item.Designated, item.GlobalDesignated)
		if d.a.query.Owner == "human" || item.Designated {
			keys += " · enter/b boot · B boot headless"
		}
		return keys
	}
	return ""
}

func (d *dashModel) bootables() []engine.StatusEmulator {
	st := d.a.status
	if st == nil {
		return nil
	}
	if st.Emulators != nil {
		return st.Emulators
	}
	var out []engine.StatusEmulator
	for _, name := range st.Bootable {
		platform := d.a.query.Opts.Platform
		if prefix, rest, ok := strings.Cut(name, "/"); ok && (prefix == "android" || prefix == "ios") {
			platform, name = prefix, rest
		}
		out = append(out, engine.StatusEmulator{Platform: platform, Name: name})
	}
	return out
}

// setStatus keeps the selected device while moving agent emulators to the top
// of each section, including after an agent-access toggle.
func (d *dashModel) setStatus(st *engine.Status) {
	selected := d.selectionID()
	d.a.status = st
	if st == nil {
		d.cursor = 0
		return
	}
	rank := func(allowed bool) int {
		if allowed {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(st.Devices, func(a, b engine.StatusDevice) int {
		return cmp.Or(
			cmp.Compare(rank(a.Class == "emulator" && (a.Designated || a.GlobalDesignated)), rank(b.Class == "emulator" && (b.Designated || b.GlobalDesignated))),
			cmp.Compare(a.Platform, b.Platform), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Serial, b.Serial))
	})
	slices.SortStableFunc(st.Emulators, func(a, b engine.StatusEmulator) int {
		return cmp.Or(
			cmp.Compare(rank(a.Designated || a.GlobalDesignated), rank(b.Designated || b.GlobalDesignated)),
			cmp.Compare(a.Platform, b.Platform), cmp.Compare(a.Name, b.Name), cmp.Compare(a.OSVersion, b.OSVersion), cmp.Compare(a.Serial, b.Serial))
	})
	count := len(st.Devices) + len(d.bootables())
	previous := min(d.cursor, max(0, count-1))
	if selected != "" {
		for d.cursor = 0; d.cursor < count; d.cursor++ {
			if d.selectionID() == selected {
				return
			}
		}
	}
	d.cursor = previous
}

func (d *dashModel) selectionID() string {
	st := d.a.status
	if st == nil || d.cursor < 0 {
		return ""
	}
	if d.cursor < len(st.Devices) {
		device := st.Devices[d.cursor]
		return device.Platform + "/device/" + device.Serial
	}
	if i := d.cursor - len(st.Devices); i < len(d.bootables()) {
		device := d.bootables()[i]
		return device.Platform + "/emulator/" + emulatorID(device)
	}
	return ""
}

func emulatorID(device engine.StatusEmulator) string {
	if device.Serial != "" {
		return device.Serial
	}
	return device.Name
}

func dashboardDeviceRow(cursor, name, version, access string, width int) string {
	right := sMuted.Render(version) + access
	if right != "" {
		right = "  " + right
	}
	return cursor + pad(name, max(0, width-lipgloss.Width(cursor)-lipgloss.Width(right))) + right
}

func (d *dashModel) agentKey(allowed, global bool) string {
	if d.a.query.Owner != "human" || global || d.savingAgents {
		return ""
	}
	if allowed {
		return " · a disable agents"
	}
	return " · a allow agents"
}

func agentLabel(allowed, global bool) string {
	if global {
		return sOK.Render("  agents: yes (global)")
	}
	if allowed {
		return sOK.Render("  agents: yes")
	}
	return sMuted.Render("  agents: no")
}

func dashboardWindow(body string, selected, height int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	start, end := window(len(lines), selected, max(1, height))
	return strings.Join(lines[start:end], "\n")
}
