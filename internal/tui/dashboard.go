package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/paths"
)

// dashModel: devices, bootable AVDs/simulators, active runs across every project, activity.
type dashModel struct {
	a            *app
	cursor       int // over devices, then bootable
	booting      string
	confirmSweep bool
	confirmProxy string // serial awaiting confirmation
	clearing     string
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
		if k.String() != "y" && k.String() != "Y" {
			return nil
		}
		d.clearing = serial
		was := ""
		if dv := d.device(serial); dv != nil {
			was = dv.Proxy
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			return proxyClearedMsg{serial, was, a.query.Plat.ClearProxy(ctx, serial)}
		}
	}
	if d.confirmSweep {
		d.confirmSweep = false
		if k.String() == "y" || k.String() == "Y" {
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
	switch k.String() {
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j":
		if d.cursor < nDev+len(boot)-1 {
			d.cursor++
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
	case "b", "enter":
		i := d.cursor - nDev
		if d.booting != "" || i < 0 || i >= len(boot) {
			return nil
		}
		name := boot[i]
		d.booting = name
		a.log("info", "booting %s…", name)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			serial, err := a.query.Plat.Boot(ctx, name, func(s string) { a.prog.Send(activityMsg{"info", s}) })
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
			fmt.Fprintf(&dev, "%s%s %s%s  %s\n", cur, pad(x.Serial, 16), pad(x.Name, 22), des, state)
			switch {
			case x.Serial == d.clearing:
				dev.WriteString(sWarn.Render("    clearing proxy…") + "\n")
			case x.Proxy != "":
				dev.WriteString(sBad.Render("    ⚠ proxy "+x.Proxy) + sMuted.Render("  (p to clear)") + "\n")
			}
		}
		dev.WriteString("\n" + sBold.Render("Bootable") + sMuted.Render("  (b to boot)") + "\n")
		if len(st.Bootable) == 0 {
			dev.WriteString(sMuted.Render("none — designate AVDs in .maestro/agent-avds.txt or "+paths.DesignatedFile()) + "\n")
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
	fmt.Fprintf(&info, "App id     %s\n", a.p.AppID)
	if a.opts.Platform == "ios" {
		fmt.Fprintf(&info, "Scheme     %s (%s)\n", a.p.IOSScheme, a.p.IOSConfig)
	} else {
		fmt.Fprintf(&info, "Build      %s\n", a.p.GradleTask)
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
	help := "↑↓ select · p clear device proxy · b boot · S sweep stale locks"
	if d.confirmProxy != "" {
		help = sWarn.Render(fmt.Sprintf("Remove the device-wide proxy on %s (Proxyman/Charles setting)? [y/N]", d.confirmProxy))
	}
	if d.confirmSweep {
		help = sWarn.Render("Reclaim locks whose owning process is gone? [y/N]")
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
