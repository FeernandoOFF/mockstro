package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/wiremock"
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
	label            string // what the list shows: a flow's name: header, else its file name
	rel              string // flows: path under flows/
	path             string // editable source file
}

// stubRef is one stub a target serves, in priority order (overrides, profile, shared).
type stubRef struct{ layer, name, route string }

// mockModel serves scenarios/profiles/flows on 18080 and shows the request journal live,
// highlighting requests no stub matched.
type mockModel struct {
	a         *app
	filter    textinput.Model
	filtering bool
	all       []mockTarget
	visible   []mockRow
	open      map[string]bool // folder key → expanded
	cursor    int
	attachIdx int // 0 = server-only, else index into eligible devices+1
	session   *engine.MockSession
	eng       *engine.Engine
	starting  bool
	busy      bool // stopping or switching
	startErr  error
	journal   []wiremock.LoggedRequest
	jErr      error
	polling   bool
	onlyMiss  bool
	focusLog  bool // arrows move in the right pane instead of picking targets
	jCur      int
	showStubs bool // while serving, the right pane lists stubs instead of the journal
	stubsFor  string
	stubList  []stubRef
	stubErr   error
	sCur      int
}

func newMockModel(a *app) *mockModel {
	ti := textinput.New()
	ti.Placeholder = "filter"
	ti.Prompt = "/ "
	return &mockModel{a: a, filter: ti, open: map[string]bool{}}
}

func (m *mockModel) refresh() {
	m.all = m.all[:0]
	for _, s := range m.a.scenarios {
		m.all = append(m.all, mockTarget{kind: "scenario", name: s.Name, desc: s.Header.Description, label: s.Name, path: s.Path})
	}
	for _, p := range m.a.profiles {
		m.all = append(m.all, mockTarget{kind: "profile", name: p, desc: "bare customer profile", label: p, path: filepath.Join(m.a.p.ProfilesRoot(), p+".json")})
	}
	for _, f := range m.a.flows {
		label := f.Header.Name
		if label == "" {
			label = f.File
		}
		m.all = append(m.all, mockTarget{kind: "flow", name: m.a.p.Rel(f.Path), desc: f.Header.Name, label: label, rel: f.Rel, path: f.Path})
	}
	m.apply()
}

func (m *mockModel) apply() {
	q := strings.ToLower(m.filter.Value())
	var match []*mockTarget
	for i, t := range m.all {
		if q == "" || strings.Contains(strings.ToLower(t.kind+" "+t.name+" "+t.desc), q) {
			match = append(match, &m.all[i])
		}
	}
	m.visible = buildRows(match, m.open, q != "")
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
		m.startErr = x.err
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
	m.loadStubs()
	if m.focusLog && m.stubsPane() {
		if c, ok := navKey(k.String(), m.sCur, len(m.stubList)); ok {
			m.sCur = c
			return nil
		}
	} else if m.focusLog {
		if c, ok := navKey(k.String(), m.jCur, len(m.journalRows())); ok {
			m.jCur = c
			return nil
		}
	} else if c, ok := navKey(k.String(), m.cursor, len(m.visible)); ok {
		m.cursor = c
		return nil
	}
	switch k.String() {
	case "/":
		m.filtering = true
		return m.filter.Focus()
	case "esc":
		m.filter.SetValue("")
		m.apply()
	case "a":
		if m.session == nil && !m.a.p.Lite() {
			m.attachIdx = (m.attachIdx + 1) % len(m.devices())
		}
	case "m":
		if !m.stubsPane() {
			m.onlyMiss = !m.onlyMiss
			m.jCur = 0
		}
	case "s":
		if m.session != nil {
			m.showStubs = !m.showStubs
		}
	case "f":
		m.focusLog = !m.focusLog
	case "e":
		if m.focusLog {
			if name := m.paneStub(); name != "" {
				return m.a.editStub(name)
			}
		} else if m.cursor < len(m.visible) && m.visible[m.cursor].target != nil {
			return m.a.editFile(m.visible[m.cursor].target.path)
		}
	case "y":
		if m.cursor < len(m.visible) {
			row := m.visible[m.cursor]
			switch {
			case row.target == nil:
				copyText(m.a, row.label)
			case row.target.rel != "":
				copyText(m.a, row.target.rel)
			default:
				copyText(m.a, row.target.name)
			}
		}
	case "x":
		if m.session != nil && !m.busy {
			sess := m.session
			m.session, m.eng, m.journal, m.busy = nil, nil, nil, true
			return func() tea.Msg { sess.Stop(); return mockStoppedMsg{} }
		}
	case "right", "left", " ":
		if m.cursor < len(m.visible) && m.visible[m.cursor].folder != "" {
			m.toggle(map[string]int{"right": 1, "left": 0, " ": -1}[k.String()])
		}
	case "enter":
		if m.focusLog {
			if name := m.paneStub(); name != "" {
				m.a.tab = tabStubs
				m.a.stubTab.show(name)
			}
			return nil
		}
		if m.cursor >= len(m.visible) || m.starting {
			return nil
		}
		row := m.visible[m.cursor]
		if row.target == nil {
			m.toggle(-1)
			return nil
		}
		t := row.target
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
		m.starting, m.startErr = true, nil
		devs := m.devices()
		device := ""
		if m.attachIdx > 0 && m.attachIdx < len(devs) {
			device = devs[m.attachIdx]
		}
		a := m.a
		target := a.query.P
		if device != "" {
			target = a.deviceQuery(device).P
		}
		return func() tea.Msg {
			o := a.opts
			o.Platform = target.Platform
			o.Lite = target.Lite()
			o.FBRC = map[string]string{}
			o.ServerPort, o.PortPinned = 18080, true
			if device != "" {
				o.Attach, o.DeviceArg, o.ClearProxy = true, device, true
			}
			if err := o.Validate("mock"); err != nil {
				return mockStartedMsg{err: err}
			}
			e := engine.New(target, o, execx.Real{}, func(ev engine.Event) {
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
		row := m.visible[i]
		cur := "  "
		name := row.label
		if row.target == nil {
			arrow := "▸ "
			if i+1 < len(m.visible) && m.visible[i+1].depth > row.depth {
				arrow = "▾ "
			}
			name = arrow + name + "/"
		}
		if i == m.cursor && !m.focusLog {
			cur = sSel.Render("› ")
			name = sSel.Render(name)
		} else if row.target == nil {
			name = sMuted.Render(name)
		}
		if row.target != nil && m.session != nil && m.session.Target.Name == row.target.name {
			name += sOK.Render(" ●")
		}
		lb.WriteString(cur + strings.Repeat("  ", row.depth) + name + "\n")
	}

	var jb strings.Builder
	devs := m.devices()
	mode := devs[min(m.attachIdx, len(devs)-1)]
	switch {
	case m.starting:
		jb.WriteString(sWarn.Render("starting WireMock on :18080…") + "\n")
	case m.session == nil:
		if m.startErr != nil {
			jb.WriteString(sBad.Width(w-listW-8).Render("failed to start: "+m.startErr.Error()) + "\n\n")
		}
		if m.a.p.Lite() {
			fmt.Fprintf(&jb, "Target device: %s %s\n\n", sSel.Render("server-only"), sMuted.Render("(attach needs full integration)"))
		} else {
			fmt.Fprintf(&jb, "Target device: %s %s\n\n", sSel.Render(mode), sMuted.Render("(a to change)"))
		}
		hint := "server-only: point Proxyman/Charles Map Remote at 127.0.0.1:18080.\nattach: installs the build on the device and writes the runtime override.\nenter on a target to start serving it."
		if m.a.p.Lite() {
			hint = "point Proxyman/Charles Map Remote at 127.0.0.1:18080.\nenter on a target to start serving it."
		}
		jb.WriteString(sMuted.Render(hint) + "\n\n")
		if m.cursor < len(m.visible) {
			if t := m.visible[m.cursor].target; t != nil {
				fmt.Fprintf(&jb, "%s %s\n%s\n\n", sMuted.Render(t.kind), sBold.Render(t.label), sMuted.Render(t.name))
				m.writeStubs(&jb, w-listW-6, h)
			}
		}
	case m.showStubs:
		t := m.session.Target
		fmt.Fprintf(&jb, "%s %s · %s\n\n", sOK.Render("● serving"), sBold.Render(t.Name), sMuted.Render("s show journal"))
		m.writeStubs(&jb, w-listW-6, h)
	default:
		t := m.session.Target
		unmatched := 0
		for _, r := range m.journal {
			if r.StubName() == wiremock.Fallback {
				unmatched++
			}
		}
		fmt.Fprintf(&jb, "%s %s · profile %s · %d overrides · :%d · %s\n", sOK.Render("● serving"), sBold.Render(t.Name), t.Profile, len(t.Overrides), m.session.Run.Port, deviceLabel(m.session.Run.Device))
		showing, toggle := "all requests", "m unmatched only"
		if m.onlyMiss {
			showing, toggle = "unmatched only", "m all requests"
		}
		fmt.Fprintf(&jb, "%d requests · %s\n", len(m.journal), sBad.Render(fmt.Sprintf("%d unmatched", unmatched)))
		fmt.Fprintf(&jb, "%s %s · %s\n\n", sMuted.Render("showing"), showing, sMuted.Render(toggle+" · s stubs"))
		if m.jErr != nil {
			jb.WriteString(sBad.Render(m.jErr.Error()) + "\n")
		}
		rows := m.journalRows()
		m.jCur = min(m.jCur, max(0, len(rows)-1))
		st, en := window(len(rows), m.jCur, h-9)
		for i := st; i < en; i++ {
			r := rows[i]
			cur := "  "
			if i == m.jCur && m.focusLog {
				cur = sSel.Render("› ")
			}
			jb.WriteString(cur)
			miss := r.StubName() == wiremock.Fallback
			at := time.UnixMilli(r.Request.LoggedDate).Format("15:04:05")
			stub := r.StubName()
			line := fmt.Sprintf("%s %-6s %s", at, r.Request.Method, r.Request.URL)
			if miss {
				jb.WriteString(sBad.Render(truncate(line+"  ← 599 unstubbed", w-listW-8)) + "\n")
			} else {
				jb.WriteString(truncate(line+"  "+sMuted.Render(stub), w-listW-8) + "\n")
			}
		}
	}
	title := "Request journal"
	if m.stubsPane() {
		title = "Stubs served"
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		focusPanel(!m.focusLog, "Scenarios · profiles · flows", lb.String(), listW, h),
		focusPanel(m.focusLog, title, jb.String(), w-listW, h))
	attach := " · a attach device"
	if m.a.p.Lite() {
		attach = ""
	}
	enter := " · enter serve/switch"
	if m.focusLog {
		enter = " · enter open in Stubs"
	}
	help := navHelp + " · f switch panel" + enter + attach + " · e edit · x stop · / filter · esc clear"
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

// journalRows is the journal as shown: every request, or only the unmatched ones.
func (m *mockModel) journalRows() []wiremock.LoggedRequest {
	if !m.onlyMiss {
		return m.journal
	}
	var out []wiremock.LoggedRequest
	for _, r := range m.journal {
		if r.StubName() == wiremock.Fallback {
			out = append(out, r)
		}
	}
	return out
}

// toggle opens (1), closes (0) or flips (-1) the folder under the cursor.
func (m *mockModel) toggle(to int) {
	row := m.visible[m.cursor]
	open := isOpen(m.open, row.folder, row.depth, false)
	if to == -1 {
		open = !open
	} else {
		open = to == 1
	}
	m.open[row.folder] = open
	m.apply()
}

func (m *mockModel) stubsPane() bool { return m.session == nil || m.showStubs }

// paneTarget is the target whose stubs the right pane lists: the served one, else the highlighted one.
func (m *mockModel) paneTarget() string {
	if m.session != nil {
		return m.session.Target.Name
	}
	if m.cursor < len(m.visible) && m.visible[m.cursor].target != nil {
		return m.visible[m.cursor].target.name
	}
	return ""
}

func (m *mockModel) loadStubs() {
	name := m.paneTarget()
	if name == m.stubsFor {
		return
	}
	m.stubsFor, m.stubList, m.stubErr, m.sCur = name, nil, nil, 0
	if name != "" {
		m.stubList, m.stubErr = m.a.targetStubs(name)
	}
}

// targetStubs lists what a mock target (scenario, profile or flow path) serves, as its
// fixtures are now: overrides, then its profile, then shared.
func (a *app) targetStubs(name string) ([]stubRef, error) {
	t, err := a.query.ResolveMockTarget([]string{name})
	if err != nil {
		return nil, err
	}
	var out []stubRef
	add := func(layer string, names []string) {
		for _, n := range names {
			ref := stubRef{layer: layer, name: n}
			if st, err := a.store.ResolvedStub(n); err == nil {
				ref.route = fixtures.Route(st)
			}
			out = append(out, ref)
		}
	}
	add("overrides", t.Overrides)
	for _, p := range []string{t.Profile, "shared"} {
		if p == "" {
			continue
		}
		pm, err := a.store.ProfileMappings(p)
		if err != nil {
			return out, err
		}
		add("profile "+p, pm)
	}
	return out, nil
}

// stubListLines renders refs grouped by layer; at is the line holding the cursor.
func stubListLines(refs []stubRef, cur int, focused bool, w int) (lines []string, at int) {
	layer := ""
	for i, r := range refs {
		if r.layer != layer {
			layer = r.layer
			lines = append(lines, sMuted.Render(layer))
		}
		mark, name := "  ", r.name
		if i == cur && focused {
			mark, name = sSel.Render("› "), sSel.Render(name)
			at = len(lines)
		}
		lines = append(lines, truncate(mark+name+"  "+sMuted.Render(r.route), w))
	}
	return lines, at
}

// paneStub is the stub under the right-pane cursor, if any.
func (m *mockModel) paneStub() string {
	if m.stubsPane() {
		if m.sCur < len(m.stubList) {
			return m.stubList[m.sCur].name
		}
		return ""
	}
	rows := m.journalRows()
	if m.jCur >= len(rows) {
		return ""
	}
	if n := rows[m.jCur].StubName(); n != wiremock.Fallback && m.a.store.StubExists(n) {
		return n
	}
	return ""
}

func (m *mockModel) writeStubs(b *strings.Builder, w, h int) {
	m.loadStubs()
	if m.stubErr != nil {
		b.WriteString(sBad.Render(m.stubErr.Error()))
		return
	}
	lines, at := stubListLines(m.stubList, m.sCur, m.focusLog, w)
	if len(lines) == 0 {
		b.WriteString(sMuted.Render("no stubs"))
		return
	}
	room := h - 4 - strings.Count(b.String(), "\n")
	if !m.focusLog {
		room -= 2
	}
	st, en := window(len(lines), at, room)
	b.WriteString(strings.Join(lines[st:en], "\n"))
	if !m.focusLog {
		b.WriteString("\n\n" + sMuted.Render("f to pick a stub · enter opens it in Stubs"))
	}
}
