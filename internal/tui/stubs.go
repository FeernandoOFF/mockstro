package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/ojson"
)

// stubsModel shows a stub's own JSON (default) or the merged result of its extends chain, and
// in a second panel the response WireMock actually serves, with bodyFileName expanded.
type stubsModel struct {
	a         *app
	filter    textinput.Model
	filtering bool
	visible   []string
	cursor    int

	shown     string
	chain     []string
	chainIdx  int  // which chain element the own view shows (0 = the stub itself)
	merged    bool // show the merged stub instead of one element's own JSON
	stubLines []string
	stubTitle string
	respLines []string
	respTitle string
	usedBy    []string
	focus     int // 0 list, 1 stub, 2 response
	stubScrl  int
	respScrl  int
}

func newStubsModel(a *app) *stubsModel {
	ti := textinput.New()
	ti.Placeholder = "filter stubs"
	ti.Prompt = "/ "
	return &stubsModel{a: a, filter: ti}
}

func (s *stubsModel) refresh() {
	q := strings.ToLower(s.filter.Value())
	s.visible = s.visible[:0]
	for _, n := range s.a.stubs {
		if q == "" || strings.Contains(strings.ToLower(n), q) {
			s.visible = append(s.visible, n)
		}
	}
	if s.cursor >= len(s.visible) {
		s.cursor = max(0, len(s.visible)-1)
	}
	s.shown = ""
}

func indent(v any) []string {
	b, err := ojson.MarshalIndent(v)
	if err != nil {
		return []string{err.Error()}
	}
	return strings.Split(string(b), "\n")
}

func errLines(err error) []string { return []string{sBad.Render(err.Error())} }

// load recomputes both panels for the selected stub and view mode.
func (s *stubsModel) load(force bool) {
	if s.cursor >= len(s.visible) {
		s.stubLines, s.respLines, s.chain = nil, nil, nil
		return
	}
	name := s.visible[s.cursor]
	if name == s.shown && !force {
		return
	}
	if name != s.shown {
		s.chainIdx, s.stubScrl, s.respScrl = 0, 0, 0
	}
	s.shown = name
	st := fixtures.New(s.a.p)
	s.chain, _ = st.ExtendsChain(name)
	if s.chainIdx >= len(s.chain) {
		s.chainIdx = 0
	}

	if s.merged {
		s.stubTitle = "Merged (extends applied)"
		if stub, err := st.ResolvedStub(name); err != nil {
			s.stubLines = errLines(err)
		} else {
			s.stubLines = indent(stub)
		}
	} else {
		el := s.chain[s.chainIdx]
		obj, path, err := st.Source(el)
		s.stubTitle = "Own JSON · " + el
		if s.chainIdx > 0 {
			s.stubTitle = fmt.Sprintf("Own JSON · %s (ancestor %d/%d)", el, s.chainIdx, len(s.chain)-1)
		}
		if err != nil {
			s.stubLines = errLines(err)
		} else {
			s.stubLines = append([]string{sMuted.Render(s.a.p.Rel(path)), ""}, indent(obj)...)
		}
	}

	s.respTitle, s.respLines = "Served response", nil
	stub, err := st.ResolvedStub(name)
	if err != nil {
		s.respLines = errLines(err)
	} else {
		s.respLines = s.response(st, stub)
	}

	s.usedBy = nil
	for _, p := range append([]string{"shared"}, s.a.profiles...) {
		ms, _ := st.ProfileMappings(p)
		for _, m := range ms {
			if m == name {
				s.usedBy = append(s.usedBy, "profile "+p)
			}
		}
	}
	for _, f := range s.a.flows {
		for _, o := range f.Header.Overrides {
			if o == name {
				s.usedBy = append(s.usedBy, f.Rel)
			}
		}
	}
}

// response summarises status, headers, faults/delays and the body, reading bodyFileName.
func (s *stubsModel) response(st *fixtures.Store, stub *ojson.Object) []string {
	resp := stub.Object("response")
	if resp == nil {
		return []string{sMuted.Render("no response")}
	}
	var out []string
	if v, ok := resp.Get("status"); ok {
		out = append(out, sBold.Render(fmt.Sprintf("HTTP %v", v)))
	} else {
		out = append(out, sBold.Render("HTTP 200"))
	}
	if req := stub.Object("request"); req != nil {
		out = append(out, sMuted.Render(fixtures.Route(stub)))
	}
	for _, k := range []string{"fault", "fixedDelayMilliseconds", "delayDistribution", "chunkedDribbleDelay", "proxyBaseUrl"} {
		if v, ok := resp.Get(k); ok {
			b, _ := ojson.Marshal(v)
			out = append(out, sWarn.Render(k+": "+string(b)))
		}
	}
	if sc := stub.String("scenarioName"); sc != "" {
		out = append(out, sMuted.Render(fmt.Sprintf("scenario %s: %s → %s", sc, stub.String("requiredScenarioState"), stub.String("newScenarioState"))))
	}
	if h := resp.Object("headers"); h != nil {
		for _, k := range h.Keys() {
			v, _ := h.Get(k)
			out = append(out, sMuted.Render(fmt.Sprintf("%s: %v", k, v)))
		}
	}
	out = append(out, "")
	switch {
	case resp.String("bodyFileName") != "":
		name := resp.String("bodyFileName")
		b, path, err := st.BodyFile(name)
		if err != nil {
			return append(out, errLines(err)...)
		}
		out = append(out, sMuted.Render("bodyFileName → "+s.a.p.Rel(path)), "")
		var pretty bytes.Buffer
		if json.Indent(&pretty, b, "", "  ") == nil {
			return append(out, strings.Split(pretty.String(), "\n")...)
		}
		return append(out, strings.Split(string(b), "\n")...)
	case resp.Has("jsonBody"):
		v, _ := resp.Get("jsonBody")
		return append(out, indent(v)...)
	case resp.Has("body"):
		body := resp.String("body")
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(body), "", "  ") == nil {
			body = pretty.String()
		}
		return append(out, strings.Split(body, "\n")...)
	}
	return append(out, sMuted.Render("(empty body)"))
}

func (s *stubsModel) key(k tea.KeyMsg) tea.Cmd {
	if s.filtering {
		switch k.String() {
		case "enter", "esc":
			s.filtering = false
			s.filter.Blur()
			return nil
		}
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(k)
		s.refresh()
		return cmd
	}
	if s.focus == 0 {
		if c, ok := navKey(k.String(), s.cursor, len(s.visible)); ok {
			s.cursor = c
			return nil
		}
	} else {
		lines, scrl := s.stubLines, &s.stubScrl
		if s.focus == 2 {
			lines, scrl = s.respLines, &s.respScrl
		}
		if c, ok := navKey(k.String(), *scrl, len(lines)); ok {
			*scrl = c
			return nil
		}
	}
	switch k.String() {
	case "/":
		s.filtering = true
		return s.filter.Focus()
	case "esc":
		s.filter.SetValue("")
		s.refresh()
	case "m":
		s.merged = !s.merged
		s.stubScrl = 0
		s.load(true)
	case "e":
		if len(s.chain) > 1 {
			s.merged = false
			s.chainIdx = (s.chainIdx + 1) % len(s.chain)
			s.stubScrl = 0
			s.load(true)
		}
	case "E":
		if len(s.chain) > 1 {
			s.merged = false
			s.chainIdx = (s.chainIdx + len(s.chain) - 1) % len(s.chain)
			s.stubScrl = 0
			s.load(true)
		}
	case "f":
		s.focus = (s.focus + 1) % 3
	case "enter":
		s.focus = min(s.focus+1, 2)
	case "y":
		if s.cursor < len(s.visible) {
			copyText(s.a, s.visible[s.cursor])
		}
	}
	return nil
}

func sliceFrom(lines []string, from int) []string {
	if from >= len(lines) {
		return nil
	}
	return lines[from:]
}

func (s *stubsModel) view(w, h int) (string, string) {
	s.load(false)
	listW := 40
	restW := w - listW
	stubW := restW / 2
	respW := restW - stubW

	var lb strings.Builder
	lb.WriteString(s.filter.View() + "\n")
	st, en := window(len(s.visible), s.cursor, h-5)
	for i := st; i < en; i++ {
		if i == s.cursor {
			lb.WriteString(sSel.Render("› "+s.visible[i]) + "\n")
		} else {
			lb.WriteString("  " + s.visible[i] + "\n")
		}
	}

	var head []string
	if len(s.chain) > 1 {
		parts := make([]string, len(s.chain))
		for i, c := range s.chain {
			parts[i] = c
			if !s.merged && i == s.chainIdx {
				parts[i] = sSel.Render(c)
			}
		}
		head = append(head, sMuted.Render("extends: ")+strings.Join(parts, sMuted.Render(" → ")))
	}
	if len(s.usedBy) > 0 {
		head = append(head, sMuted.Render("used by: ")+strings.Join(s.usedBy, ", "))
	}
	if len(head) > 0 {
		head = append(head, "")
	}
	s.stubScrl = min(s.stubScrl, max(0, len(s.stubLines)-(h-3-len(head))))
	s.respScrl = min(s.respScrl, max(0, len(s.respLines)-(h-3)))
	stubBody := strings.Join(append(head, sliceFrom(s.stubLines, s.stubScrl)...), "\n")

	chain := ""
	if len(s.chain) > 1 {
		chain = " · e/E walk extends chain"
	}
	help := navHelp + " · f switch panel · enter focus right · m own/merged" + chain + " · / filter · esc clear"
	return lipgloss.JoinHorizontal(lipgloss.Top,
		focusPanel(s.focus == 0, "Stubs", lb.String(), listW, h),
		focusPanel(s.focus == 1, s.stubTitle, stubBody, stubW, h),
		focusPanel(s.focus == 2, s.respTitle, strings.Join(sliceFrom(s.respLines, s.respScrl), "\n"), respW, h)), help
}
