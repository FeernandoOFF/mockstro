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

// flowsModel: filterable, multi-select flow picker grouped by feature.
type flowsModel struct {
	a         *app
	filter    textinput.Model
	filtering bool
	tags      []string // "" = all
	tagIdx    int
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
	for _, fl := range f.a.flows {
		if !fl.MatchesPlatform(f.a.opts.Platform) {
			continue
		}
		if tag != "" && !fl.HasTag(tag) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(fl.Rel + " " + strings.Join(fl.Header.Tags, " ") + " " + fl.Header.Profile + " " + fl.Header.Name)
			if !strings.Contains(hay, q) {
				continue
			}
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
	switch k.String() {
	case "/":
		f.filtering = true
		return f.filter.Focus()
	case "esc":
		f.filter.SetValue("")
		f.apply()
	case "up", "k":
		if f.cursor > 0 {
			f.cursor--
		}
	case "down", "j":
		if f.cursor < len(f.visible)-1 {
			f.cursor++
		}
	case "pgup":
		f.cursor = max(0, f.cursor-10)
	case "pgdown":
		f.cursor = min(len(f.visible)-1, f.cursor+10)
	case " ", "x":
		if f.cursor < len(f.visible) {
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
	case "t":
		f.tagIdx = (f.tagIdx + 1) % len(f.tags)
		f.apply()
	case "T":
		f.tagIdx = (f.tagIdx + len(f.tags) - 1) % len(f.tags)
		f.apply()
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
	tag := f.tags[f.tagIdx]
	if tag == "" {
		tag = "all"
	}
	head := fmt.Sprintf("Tag: %s   %s   %d visible · %d selected", sSel.Render(tag), f.filter.View(), len(f.visible), len(f.selected))
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
		if i == f.cursor {
			cur = sSel.Render("› ")
		}
		feature := ""
		if fl.Feature != lastFeature {
			feature = fl.Feature
			lastFeature = fl.Feature
		}
		name := fl.File
		if i == f.cursor {
			name = sSel.Render(name)
		}
		tags := sMuted.Render(strings.Join(fl.Header.Tags, ","))
		line := fmt.Sprintf("%s%s %s %s %s %s", cur, mark, pad(sMuted.Render(feature), 12), pad(name, 52), pad(fl.Header.Profile, 11), tags)
		b.WriteString(truncate(line, w-4) + "\n")
	}
	if len(f.visible) == 0 {
		b.WriteString(sMuted.Render("no flows match"))
	}
	var detail string
	if f.cursor < len(f.visible) {
		fl := f.visible[f.cursor]
		detail = fmt.Sprintf("%s  %s  overrides %d · verifications %d · fbrc %d",
			sBold.Render(fl.Header.Name), sMuted.Render(fl.Rel), len(fl.Header.Overrides), len(fl.Header.Verifications), len(fl.Header.FBRC))
	}
	body := lipgloss.JoinVertical(lipgloss.Left, " "+head, panel("Flows", b.String(), w, listH+2), " "+truncate(detail, w-2))
	help := "↑↓ move · space select · a all · n none · t/T tag · / filter · enter configure run"
	if f.filtering {
		help = "type to filter · enter/esc done"
	}
	return body, help
}
