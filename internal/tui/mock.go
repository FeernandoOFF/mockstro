package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/wiremock"
)

type (
	journalMsg struct {
		reqs []wiremock.LoggedRequest
		err  error
	}
	mockStartedMsg struct {
		m   *engine.MockSession
		e   *engine.Engine
		err error
	}
	mockStoppedMsg  struct{}
	mockSwitchedMsg struct {
		target *engine.MockTarget
		err    error
	}
)

type mockTarget struct {
	kind, name, desc string
}

// mockModel serves scenarios/profiles/flows on 18080 and shows the request journal live,
// highlighting requests no stub matched.
type mockModel struct {
	a         *app
	filter    textinput.Model
	filtering bool
	all       []mockTarget
	visible   []mockTarget
	cursor    int
	attachIdx int // 0 = server-only, else index into eligible devices+1
	session   *engine.MockSession
	eng       *engine.Engine
	starting  bool
	busy      bool // stopping or switching
	journal   []wiremock.LoggedRequest
	jErr      error
	polling   bool
	onlyMiss  bool
	jCursor   int
}

func newMockModel(a *app) *mockModel {
	ti := textinput.New()
	ti.Placeholder = "filter"
	ti.Prompt = "/ "
	return &mockModel{a: a, filter: ti}
}

func (m *mockModel) refresh() {
	m.all = m.all[:0]
	for _, s := range m.a.scenarios {
		m.all = append(m.all, mockTarget{"scenario", s.Name, s.Header.Description})
	}
	for _, p := range m.a.profiles {
		m.all = append(m.all, mockTarget{"profile", p, "bare customer profile"})
	}
	for _, f := range m.a.flows {
		m.all = append(m.all, mockTarget{"flow", m.a.p.Rel(f.Path), f.Header.Name})
	}
	m.apply()
}

func (m *mockModel) apply() {
	q := strings.ToLower(m.filter.Value())
	m.visible = m.visible[:0]
	for _, t := range m.all {
		if q == "" || strings.Contains(strings.ToLower(t.kind+" "+t.name+" "+t.desc), q) {
			m.visible = append(m.visible, t)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
}

func (m *mockModel) devices() []string {
	out := []string{"server-only"}
	if st := m.a.status; st != nil {
		out = append(out, st.Available.Eligible...)
	}
	return out
}

func (m *mockModel) stopSync() {
	if m.session != nil {
		m.session.Stop()
		m.session = nil
		m.eng = nil
	}
}

func (m *mockModel) onTick() tea.Cmd {
	if m.session == nil || m.polling {
		return nil
	}
	m.polling = true
	wm := m.session.Run.WM
	return func() tea.Msg {
		reqs, err := wm.Journal(false)
		return journalMsg{reqs, err}
	}
}

func (m *mockModel) update(msg tea.Msg) tea.Cmd {
	switch x := msg.(type) {
	case journalMsg:
		m.polling = false
		m.journal, m.jErr = x.reqs, x.err
	case mockStoppedMsg:
		m.busy = false
		m.a.log("info", "mock stopped")
		return m.a.refreshStatus()
	case mockSwitchedMsg:
		m.busy = false
		if x.err != nil {
			m.a.log("error", "mock: %v", x.err)
		} else {
			if m.session != nil {
				m.session.Target = x.target
			}
			m.a.log("info", "mock now serving %s %s", x.target.Kind, x.target.Name)
		}
	case mockStartedMsg:
		m.starting = false
		if x.err != nil {
			m.a.log("error", "mock: %v", x.err)
			return nil
		}
		m.session, m.eng = x.m, x.e
		m.a.log("info", "mock serving %s %s on :%d", x.m.Target.Kind, x.m.Target.Name, x.m.Run.Port)
		return m.a.refreshStatus()
	}
	return nil
}

func (m *mockModel) key(k tea.KeyMsg) tea.Cmd {
	if m.filtering {
		switch k.String() {
		case "enter", "esc":
			m.filtering = false
			m.filter.Blur()
			return nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(k)
		m.apply()
		return cmd
	}
	switch k.String() {
	case "/":
		m.filtering = true
		return m.filter.Focus()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case "d":
		if m.session == nil {
			m.attachIdx = (m.attachIdx + 1) % len(m.devices())
		}
	case "u":
		m.onlyMiss = !m.onlyMiss
	case "s":
		if m.session != nil && !m.busy {
			sess := m.session
			m.session, m.eng, m.journal, m.busy = nil, nil, nil, true
			return func() tea.Msg { sess.Stop(); return mockStoppedMsg{} }
		}
	case "enter":
		if m.cursor >= len(m.visible) || m.starting {
			return nil
		}
		t := m.visible[m.cursor]
		arg := t.name
		if m.session != nil {
			if m.busy {
				return nil
			}
			// Same server, new fixtures; off the UI loop because activation emits events.
			sess, eng := m.session, m.eng
			m.busy = true
			return func() tea.Msg {
				target, err := eng.ResolveMockTarget([]string{arg})
				if err == nil {
					err = sess.Run.Activate(context.Background(), target.Profile, target.FBRC, target.Overrides)
				}
				return mockSwitchedMsg{target, err}
			}
		}
		m.starting = true
		devs := m.devices()
		device := ""
		if m.attachIdx > 0 && m.attachIdx < len(devs) {
			device = devs[m.attachIdx]
		}
		a := m.a
		return func() tea.Msg {
			o := a.opts
			o.FBRC = map[string]string{}
			o.ServerPort, o.PortPinned = 18080, true
			if device != "" {
				o.Attach, o.DeviceArg, o.ClearProxy = true, device, true
			}
			if err := o.Validate("mock"); err != nil {
				return mockStartedMsg{err: err}
			}
			e := engine.New(a.p, o, execx.Real{}, func(ev engine.Event) {
				if ev.Kind == engine.EvLog {
					a.prog.Send(activityMsg{ev.Level, ev.Text})
				}
			})
			if err := e.CheckEnvironment("mock"); err != nil {
				return mockStartedMsg{err: err}
			}
			target, err := e.ResolveMockTarget([]string{arg})
			if err != nil {
				return mockStartedMsg{err: err}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			ms, err := e.StartMock(ctx, target)
			return mockStartedMsg{m: ms, e: e, err: err}
		}
	}
	return nil
}

func (m *mockModel) view(w, h int) (string, string) {
	listW := 58
	var lb strings.Builder
	lb.WriteString(m.filter.View() + "\n")
	s, e := window(len(m.visible), m.cursor, h-5)
	for i := s; i < e; i++ {
		t := m.visible[i]
		cur := "  "
		name := t.name
		if i == m.cursor {
			cur = sSel.Render("› ")
			name = sSel.Render(name)
		}
		if m.session != nil && m.session.Target.Name == t.name {
			name += sOK.Render(" ●")
		}
		fmt.Fprintf(&lb, "%s%s %s\n", cur, pad(sMuted.Render(t.kind), 9), name)
	}

	var jb strings.Builder
	devs := m.devices()
	mode := devs[min(m.attachIdx, len(devs)-1)]
	switch {
	case m.starting:
		jb.WriteString(sWarn.Render("starting WireMock on :18080…") + "\n")
	case m.session == nil:
		fmt.Fprintf(&jb, "Target device: %s %s\n\n", sSel.Render(mode), sMuted.Render("(d to change)"))
		jb.WriteString(sMuted.Render("server-only: point Proxyman/Charles Map Remote at 127.0.0.1:18080.\nattach: installs the build on the device and writes the runtime override.\n\nenter on a target to start serving it."))
	default:
		t := m.session.Target
		unmatched := 0
		for _, r := range m.journal {
			if r.StubName() == wiremock.Fallback {
				unmatched++
			}
		}
		fmt.Fprintf(&jb, "%s %s · profile %s · %d overrides · :%d · %s\n", sOK.Render("● serving"), sBold.Render(t.Name), t.Profile, len(t.Overrides), m.session.Run.Port, deviceLabel(m.session.Run.Device))
		fmt.Fprintf(&jb, "%d requests · %s\n\n", len(m.journal), sBad.Render(fmt.Sprintf("%d unmatched", unmatched)))
		if m.jErr != nil {
			jb.WriteString(sBad.Render(m.jErr.Error()) + "\n")
		}
		rows := 0
		for _, r := range m.journal {
			miss := r.StubName() == wiremock.Fallback
			if m.onlyMiss && !miss {
				continue
			}
			if rows >= h-8 {
				break
			}
			rows++
			at := time.UnixMilli(r.Request.LoggedDate).Format("15:04:05")
			stub := r.StubName()
			line := fmt.Sprintf("%s %-6s %s", at, r.Request.Method, r.Request.URL)
			if miss {
				jb.WriteString(sBad.Render(truncate(line+"  ← 599 unstubbed", w-listW-6)) + "\n")
			} else {
				jb.WriteString(truncate(line+"  "+sMuted.Render(stub), w-listW-6) + "\n")
			}
		}
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		panel("Scenarios · profiles · flows", lb.String(), listW, h),
		panel("Request journal", jb.String(), w-listW, h))
	help := "↑↓ move · enter serve/switch · d device · u unmatched only · s stop · / filter"
	if m.filtering {
		help = "type to filter · enter/esc done"
	}
	return body, help
}

func deviceLabel(d string) string {
	if d == "" {
		return "server-only"
	}
	return "attached " + d
}
