// Package tui is trun's interactive front end. It drives the same engine as the CLI; every
// run it starts could equally have been started from the command line (the config screen
// shows the equivalent command).
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/lock"
	"github.com/feernandooff/trun/internal/project"
)

const (
	tabDash = iota
	tabFlows
	tabRun
	tabHistory
	tabMock
	tabStubs
)

var tabNames = []string{"Dashboard", "Flows", "Run", "History", "Mock", "Stubs"}

type (
	evMsg       engine.Event
	statusMsg   struct{ st *engine.Status }
	tickMsg     time.Time
	activityMsg struct {
		level, text string
	}
	fixturesMsg struct {
		flows     []*fixtures.Flow
		scenarios []*fixtures.Scenario
		profiles  []string
		stubs     []string
		counts    fixtures.Counts
		err       error
	}
	bootDoneMsg struct {
		name, serial string
		err          error
	}
	sweepDoneMsg []lock.SweepAction
)

type activity struct {
	at    time.Time
	level string
	text  string
}

type app struct {
	p     *project.Project
	opts  engine.Options
	prog  *tea.Program
	query *engine.Engine
	store *fixtures.Store

	w, h int
	tab  int

	flows     []*fixtures.Flow
	scenarios []*fixtures.Scenario
	profiles  []string
	stubs     []string
	counts    fixtures.Counts
	fixErr    error

	status     *engine.Status
	activity   []activity
	confirmQ   bool
	quitting   bool
	statusBusy bool

	dash    *dashModel
	flowTab *flowsModel
	cfg     *configModel
	runv    *runModel
	hist    *historyModel
	mock    *mockModel
	stubTab *stubsModel
}

// Run starts the TUI for project p.
func Run(p *project.Project, opts engine.Options) error {
	a := &app{p: p, opts: opts, store: fixtures.New(p)}
	a.query = engine.New(p, opts, execx.Real{}, func(ev engine.Event) {
		if ev.Kind == engine.EvLog && a.prog != nil {
			a.prog.Send(activityMsg{ev.Level, ev.Text})
		}
	})
	a.dash = &dashModel{a: a}
	a.flowTab = newFlowsModel(a)
	a.cfg = newConfigModel(a)
	a.runv = &runModel{a: a}
	a.hist = &historyModel{a: a}
	a.mock = newMockModel(a)
	a.stubTab = newStubsModel(a)
	a.prog = tea.NewProgram(a, tea.WithAltScreen())
	_, err := a.prog.Run()
	a.mock.stopSync()
	return err
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(a.loadFixtures(), a.refreshStatus(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *app) loadFixtures() tea.Cmd {
	return func() tea.Msg {
		var m fixturesMsg
		m.counts, m.err = a.store.ValidateAll(true)
		m.flows, _ = a.store.Flows()
		m.scenarios, _ = a.store.Scenarios()
		m.profiles, _ = a.store.ProfileNames()
		m.stubs, _ = a.store.StubNames()
		return m
	}
}

func (a *app) refreshStatus() tea.Cmd {
	if a.statusBusy {
		return nil
	}
	a.statusBusy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return statusMsg{a.query.Status(ctx)}
	}
}

func (a *app) log(level, format string, args ...any) {
	a.activity = append(a.activity, activity{time.Now(), level, fmt.Sprintf(format, args...)})
	if len(a.activity) > 300 {
		a.activity = a.activity[len(a.activity)-300:]
	}
}

// inputFocused: a text field owns the keyboard.
func (a *app) inputFocused() bool {
	switch a.tab {
	case tabFlows:
		if a.cfg.active {
			return a.cfg.editing
		}
		return a.flowTab.filtering
	case tabMock:
		return a.mock.filtering
	case tabStubs:
		return a.stubTab.filtering
	}
	return false
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = m.Width, m.Height
		return a, nil
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		if time.Time(m).Second()%3 == 0 {
			cmds = append(cmds, a.refreshStatus())
		}
		if c := a.mock.onTick(); c != nil {
			cmds = append(cmds, c)
		}
		return a, tea.Batch(cmds...)
	case statusMsg:
		a.statusBusy = false
		a.status = m.st
		return a, nil
	case fixturesMsg:
		a.flows, a.scenarios, a.profiles, a.stubs, a.counts, a.fixErr = m.flows, m.scenarios, m.profiles, m.stubs, m.counts, m.err
		if m.err != nil {
			a.log("error", "fixtures: %v", m.err)
		} else {
			a.log("info", "%s", m.counts)
		}
		a.flowTab.refresh()
		a.mock.refresh()
		a.stubTab.refresh()
		return a, nil
	case activityMsg:
		a.log(m.level, "%s", m.text)
		return a, nil
	case bootDoneMsg:
		if m.err != nil {
			a.log("error", "boot %s: %v", m.name, m.err)
		} else {
			a.log("info", "booted %s as %s", m.name, m.serial)
		}
		a.dash.booting = ""
		return a, a.refreshStatus()
	case keyboardSetMsg:
		a.dash.clearing = ""
		if m.err != nil {
			a.log("error", "keyboard: %v", m.err)
		} else {
			a.log("info", "on-screen keyboard %s on %s", m.state, m.serial)
		}
		return a, a.refreshStatus()
	case proxyClearedMsg:
		a.dash.clearing = ""
		if m.err != nil {
			a.log("error", "proxy: %v", m.err)
		} else {
			a.log("info", "cleared proxy on %s (%s); verified clean", m.serial, m.was)
		}
		return a, a.refreshStatus()
	case sweepDoneMsg:
		if len(m) == 0 {
			a.log("info", "sweep: no safely reclaimable locks found")
		}
		for _, s := range m {
			a.log("info", "sweep: %s (run %s): %s", s.Name, s.RunID, s.Note)
		}
		return a, a.refreshStatus()
	case evMsg, runDoneMsg:
		return a, a.runv.update(msg)
	case journalMsg, mockStartedMsg, mockStoppedMsg, mockSwitchedMsg:
		return a, a.mock.update(msg)
	case tea.KeyMsg:
		if a.quitting {
			return a, nil
		}
		return a.handleKey(m)
	}
	return a, nil
}

func (a *app) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.confirmQ {
		switch k.String() {
		case "y", "Y", "enter":
			return a.quit()
		default:
			a.confirmQ = false
			return a, nil
		}
	}
	if k.String() == "ctrl+c" {
		return a.requestQuit()
	}
	if !a.inputFocused() {
		switch k.String() {
		case "q":
			if !(a.tab == tabFlows && a.cfg.active) {
				return a.requestQuit()
			}
		case "1", "2", "3", "4", "5", "6":
			a.tab = int(k.String()[0] - '1')
			return a, nil
		case "tab":
			if !(a.tab == tabFlows && a.cfg.active) {
				a.tab = (a.tab + 1) % len(tabNames)
				return a, nil
			}
		case "shift+tab":
			if !(a.tab == tabFlows && a.cfg.active) {
				a.tab = (a.tab + len(tabNames) - 1) % len(tabNames)
				return a, nil
			}
		case "R":
			// Everything: fixtures from disk, device/run status, history.
			a.log("info", "reloading…")
			a.hist.dirty = true
			return a, tea.Batch(a.loadFixtures(), a.refreshStatus(), a.mock.onTick())
		case "r":
			if !(a.tab == tabFlows && a.cfg.active) {
				return a, a.refreshTab()
			}
		}
	}
	switch a.tab {
	case tabDash:
		return a, a.dash.key(k)
	case tabFlows:
		if a.cfg.active {
			return a, a.cfg.key(k)
		}
		return a, a.flowTab.key(k)
	case tabRun:
		return a, a.runv.key(k)
	case tabHistory:
		return a, a.hist.key(k)
	case tabMock:
		return a, a.mock.key(k)
	case tabStubs:
		return a, a.stubTab.key(k)
	}
	return a, nil
}

func (a *app) requestQuit() (tea.Model, tea.Cmd) {
	if a.runv.running() || a.mock.session != nil {
		a.confirmQ = true
		return a, nil
	}
	return a.quit()
}

// quit cleans up off the UI loop: cleanup emits events through prog.Send, which would block
// forever if Update were waiting for it.
func (a *app) quit() (tea.Model, tea.Cmd) {
	if a.quitting {
		return a, nil
	}
	a.quitting = true
	if a.runv.cancel != nil {
		a.runv.cancel()
	}
	done := a.runv.done
	sess := a.mock.session
	a.mock.session = nil
	return a, func() tea.Msg {
		if sess != nil {
			sess.Stop()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Minute):
			}
		}
		return tea.QuitMsg{}
	}
}

func (a *app) View() string {
	if a.quitting {
		return "Cleaning up…\n"
	}
	if a.w == 0 {
		return "Loading…"
	}
	var tabs []string
	for i, n := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if i == tabRun && a.runv.running() {
			label += " ●"
		}
		if i == tabMock && a.mock.session != nil {
			label += " ●"
		}
		if i == a.tab {
			tabs = append(tabs, sTabActive.Render(label))
		} else {
			tabs = append(tabs, sTab.Render(label))
		}
	}
	head := lipgloss.JoinHorizontal(lipgloss.Top, sTitle.Render(" trun "), strings.Join(tabs, ""))
	proj := sMuted.Render(fmt.Sprintf(" %s · %s · owner %s", a.p.Root, a.opts.Platform, a.query.Owner))
	bodyH := a.h - 4
	var body, help string
	switch a.tab {
	case tabDash:
		body, help = a.dash.view(a.w, bodyH)
	case tabFlows:
		if a.cfg.active {
			body, help = a.cfg.view(a.w, bodyH)
		} else {
			body, help = a.flowTab.view(a.w, bodyH)
		}
	case tabRun:
		body, help = a.runv.view(a.w, bodyH)
	case tabHistory:
		body, help = a.hist.view(a.w, bodyH)
	case tabMock:
		body, help = a.mock.view(a.w, bodyH)
	case tabStubs:
		body, help = a.stubTab.view(a.w, bodyH)
	}
	footer := sHelp.Render(" " + help + " · 1-6/tab switch · r/R refresh · q quit")
	if a.confirmQ {
		footer = sWarn.Render(" A run or mock is active. Stop it and quit? [y/N]")
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, head, proj, body, footer)
}

// refreshTab refreshes what the current tab shows.
func (a *app) refreshTab() tea.Cmd {
	switch a.tab {
	case tabDash, tabRun:
		return a.refreshStatus()
	case tabHistory:
		a.hist.load()
	case tabMock:
		return a.mock.onTick()
	case tabFlows, tabStubs:
		return a.loadFixtures()
	}
	return nil
}
