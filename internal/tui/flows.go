package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/feernandooff/trun/internal/fixtures"
)

// flowsModel: a tag menu beside a filterable, multi-select flow picker grouped by feature.
type flowsModel struct {
	a         *app
	filter    textinput.Model
	filtering bool
	tags      []string // "" = all
	tagIdx    int
	tagCount  []int // flows per tag after the text filter
	focusList bool  // arrows move in the flow list instead of the tag menu
	visible   []*fixtures.Flow
	selected  map[string]bool
	cursor    int
}

func newFlowsModel(a *app) *flowsModel {
	ti := textinput.New()
	ti.Placeholder = "filter by path, tag or profile"
	ti.Prompt = "/ "
	return &flowsModel{a: a, filter: ti, selected: map[string]bool{}, tags: []string{""}}
}

func (f *flowsModel) refresh() {
	seen := map[string]bool{}
	tags := []string{""}
	var rest []string
	for _, fl := range f.a.flows {
		for _, t := range fl.Header.Tags {
			if !seen[t] {
				seen[t] = true
				rest = append(rest, t)
			}
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		rank := func(t string) int {
			switch t {
			case "smoke":
				return 0
			case "regression":
				return 1
			}
			return 2
		}
		if rank(rest[i]) != rank(rest[j]) {
			return rank(rest[i]) < rank(rest[j])
		}
		return rest[i] < rest[j]
	})
	f.tags = append(tags, rest...)
	if f.tagIdx >= len(f.tags) {
		f.tagIdx = 0
	}
	f.apply()
}

func (f *flowsModel) apply() {
	q := strings.ToLower(strings.TrimSpace(f.filter.Value()))
	tag := f.tags[f.tagIdx]
	f.visible = f.visible[:0]
	f.tagCount = make([]int, len(f.tags))
	for _, fl := range f.a.flows {
		if !fl.MatchesPlatform(f.a.opts.Platform) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(fl.Rel + " " + strings.Join(fl.Header.Tags, " ") + " " + fl.Header.Profile + " " + fl.Header.Name)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		for i, t := range f.tags {
			if t == "" || fl.HasTag(t) {
				f.tagCount[i]++
			}
		}
		if tag != "" && !fl.HasTag(tag) {
			continue
		}
		f.visible = append(f.visible, fl)
	}
	if f.cursor >= len(f.visible) {
		f.cursor = max(0, len(f.visible)-1)
	}
}

// chosen: the explicit selection, or everything visible when nothing is selected.
func (f *flowsModel) chosen() []*fixtures.Flow {
	var out []*fixtures.Flow
	for _, fl := range f.a.flows {
		if f.selected[fl.Path] {
			out = append(out, fl)
		}
	}
	if len(out) == 0 {
		out = append(out, f.visible...)
	}
	return out
}

func (f *flowsModel) key(k tea.KeyMsg) tea.Cmd {
	if f.filtering {
		switch k.String() {
		case "enter", "esc":
			f.filtering = false
			f.filter.Blur()
			return nil
		}
		var cmd tea.Cmd
		f.filter, cmd = f.filter.Update(k)
		f.apply()
		return cmd
	}
	if !f.focusList {
		if c, ok := navKey(k.String(), f.tagIdx, len(f.tags)); ok {
			if c != f.tagIdx {
				f.tagIdx, f.cursor = c, 0
				f.apply()
			}
			return nil
		}
	} else if c, ok := navKey(k.String(), f.cursor, len(f.visible)); ok {
		f.cursor = c
		return nil
	}
	switch k.String() {
	case "/":
		f.filtering = true
		return f.filter.Focus()
	case "esc":
		f.filter.SetValue("")
		f.apply()
	case "f":
		f.focusList = !f.focusList
	case " ":
		if f.focusList && f.cursor < len(f.visible) {
			p := f.visible[f.cursor].Path
			f.selected[p] = !f.selected[p]
			if !f.selected[p] {
				delete(f.selected, p)
			}
			if f.cursor < len(f.visible)-1 {
				f.cursor++
			}
		}
	case "a":
		for _, fl := range f.visible {
			f.selected[fl.Path] = true
		}
	case "n":
		f.selected = map[string]bool{}
	case "enter":
		flows := f.chosen()
		if len(flows) == 0 {
			return nil
		}
		f.a.cfg.open(flows, f.tags[f.tagIdx], len(f.selected) > 0)
	}
	return nil
}

func (f *flowsModel) view(w, h int) (string, string) {
	tagW := 26
	listW := w - tagW
	head := fmt.Sprintf("%s   %d visible · %d selected", f.filter.View(), len(f.visible), len(f.selected))

	var tb strings.Builder
	ts, te := window(len(f.tags), f.tagIdx, h-5)
	for i := ts; i < te; i++ {
		name := f.tags[i]
		if name == "" {
			name = "all"
		}
		cur, line := "  ", pad(name, tagW-12)+sMuted.Render(fmt.Sprintf("%4d", f.tagCount[i]))
		if i == f.tagIdx {
			cur, line = sSel.Render("› "), sSel.Render(pad(name, tagW-12))+sMuted.Render(fmt.Sprintf("%4d", f.tagCount[i]))
		}
		tb.WriteString(cur + line + "\n")
	}

	listH := h - 5
	start, end := window(len(f.visible), f.cursor, listH)
	var b strings.Builder
	lastFeature := ""
	for i := start; i < end; i++ {
		fl := f.visible[i]
		mark := "[ ]"
		if f.selected[fl.Path] {
			mark = sOK.Render("[x]")
		}
		cur := "  "
		name := fl.File
		if i == f.cursor && f.focusList {
			cur, name = sSel.Render("› "), sSel.Render(name)
		}
		feature := ""
		if fl.Feature != lastFeature {
			feature = fl.Feature
			lastFeature = fl.Feature
		}
		tags := sMuted.Render(strings.Join(fl.Header.Tags, ","))
		line := fmt.Sprintf("%s%s %s %s %s %s", cur, mark, pad(sMuted.Render(feature), 12), pad(name, 44), pad(fl.Header.Profile, 11), tags)
		b.WriteString(truncate(line, listW-4) + "\n")
	}
	if len(f.visible) == 0 {
		b.WriteString(sMuted.Render("no flows match"))
	}
	var detail string
	if f.focusList && f.cursor < len(f.visible) {
		fl := f.visible[f.cursor]
		detail = fmt.Sprintf("%s  %s  overrides %d · verifications %d · fbrc %d",
			sBold.Render(fl.Header.Name), sMuted.Render(fl.Rel), len(fl.Header.Overrides), len(fl.Header.Verifications), len(fl.Header.FBRC))
	}
	panes := lipgloss.JoinHorizontal(lipgloss.Top,
		focusPanel(!f.focusList, "Tags", tb.String(), tagW, listH+2),
		focusPanel(f.focusList, "Flows", b.String(), listW, listH+2))
	body := lipgloss.JoinVertical(lipgloss.Left, " "+head, panes, " "+truncate(detail, w-2))
	help := navHelp + " · f switch panel · space select · a all · n none · / filter · esc clear · enter configure run"
	if !f.focusList {
		help = navHelp + " tag · f switch panel · a all · n none · / filter · esc clear · enter configure run"
	}
	if f.filtering {
		help = "type to filter · enter/esc done"
	}
	return body, help
}
