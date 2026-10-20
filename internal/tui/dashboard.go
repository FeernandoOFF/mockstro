package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/paths"
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
			return func() tea.Msg {
				return sweepDoneMsg(a.query.Sweep(context.Background(), false))
			}
		}
		return nil
	}
	var boot []string
	nDev := 0
	if a.status != nil {
		boot = a.status.Bootable
		nDev = len(a.status.Devices)
	}
	if c, ok := navKey(k.String(), d.cursor, nDev+len(boot)); ok {
		d.cursor = c
		return nil
	}
	switch k.String() {
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
		name := boot[i]
		query := a.query
		if platform, rest, ok := strings.Cut(name, "/"); ok && (platform == "android" || platform == "ios") {
			o := a.opts
			o.Platform = platform
			query = engine.New(a.p, o, a.query.Exec, nil)
			name = rest
		}
		headless := k.String() == "B"
		d.booting = name
		mode := ""
		if headless {
			mode = " headless"
		}
		a.log("info", "booting %s%s…", name, mode)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			serial, err := query.Plat.Boot(ctx, name, headless, func(s string) { a.prog.Send(activityMsg{"info", s}) })
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
	if st == nil {
		dev.WriteString(sMuted.Render("probing devices…"))
	} else {
		if len(st.Devices) == 0 {
			dev.WriteString(sMuted.Render("no devices attached") + "\n")
		}
		for i, x := range st.Devices {
			cur := "  "
			if i == d.cursor {
				cur = sSel.Render("› ")
			}
			state := sOK.Render("free")
			if x.LockedBy != nil {
				state = sWarn.Render("locked " + *x.LockedBy)
			} else if !x.Eligible {
				state = sMuted.Render("not eligible")
			}
			des := ""
			if x.Designated {
				des = sTitle.Render(" ★")
			}
			fmt.Fprintf(&dev, "%s%s %s%s  %s\n", cur, pad(strings.TrimPrefix(x.Platform+"/"+x.Serial, "/"), 24), pad(x.Name, 22), des, state)
			switch x.Keyboard {
			case "shown", "hidden":
				dev.WriteString(sMuted.Render("    ⌨ keyboard "+x.Keyboard) + "\n")
			}
			switch {
			case x.Serial == d.clearing:
				dev.WriteString(sWarn.Render("    applying…") + "\n")
			case x.Proxy != "":
				dev.WriteString(sBad.Render("    ⚠ proxy "+x.Proxy) + sMuted.Render("  (p to clear)") + "\n")
			}
		}
		dev.WriteString("\n" + sBold.Render("Bootable") + sMuted.Render("  (b to boot, B headless)") + "\n")
		if len(st.Bootable) == 0 {
			dev.WriteString(sMuted.Render("none — set platforms.android.devices in .maestro/mockstro.yaml or list AVDs in "+paths.DesignatedFile()) + "\n")
		}
		for i, n := range st.Bootable {
			cur := "  "
			if i+len(st.Devices) == d.cursor {
				cur = sSel.Render("› ")
			}
			line := cur + n
			if n == d.booting {
				line += sWarn.Render("  booting…")
			}
			dev.WriteString(line + "\n")
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
		} else if target.BuildSystem == "kotlin-toolchain" {
			fmt.Fprintf(&info, "Build      Kotlin Toolchain (%s)\n", target.KotlinVariant)
		} else if target.Platform == "ios" {
			fmt.Fprintf(&info, "Scheme     %s (%s)\n", target.IOSScheme, target.IOSConfig)
		} else {
			fmt.Fprintf(&info, "Build      %s\n", target.GradleTask)
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
		panel("Devices", dev.String(), leftW, topH),
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
		if dv.LockedBy != nil {
			return ""
		}
		keys := ""
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
	if d.cursor-len(st.Devices) < len(st.Bootable) {
		return " · enter/b boot · B boot headless"
	}
	return ""
}
