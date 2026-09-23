package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/feernandooff/trun/internal/engine"
	"github.com/feernandooff/trun/internal/fixtures"
)

type cfgField struct {
	label   string
	options []string
	idx     int
	text    bool // free-text field
}

func (f *cfgField) value() string {
	if f.text || len(f.options) == 0 {
		return ""
	}
	return f.options[f.idx]
}

// configModel is the run setup form; it shows the equivalent CLI command.
type configModel struct {
	a        *app
	active   bool
	editing  bool
	flows    []*fixtures.Flow
	byTag    string
	explicit bool
	fields   []*cfgField
	cursor   int
	fbrc     textinput.Model
	err      string
}

const (
	fDevice = iota
	fParallel
	fBuild
	fBoot
	fProfile
	fFailFast
	fRecord
	fIntercept
	fProxy
	fKeyboard
	fFBRC
)

func newConfigModel(a *app) *configModel {
	ti := textinput.New()
	ti.Placeholder = "key=value, key=value"
	ti.Prompt = ""
	ti.CharLimit = 2000
	return &configModel{a: a, fbrc: ti}
}

func (c *configModel) open(flows []*fixtures.Flow, tag string, explicit bool) {
	c.active, c.flows, c.byTag, c.explicit, c.err = true, flows, tag, explicit || c.a.flowTab.filter.Value() != "", ""
	devices := []string{"auto"}
	if st := c.a.status; st != nil {
		devices = append(devices, st.Available.Eligible...)
	}
	profiles := append([]string{"flow default"}, c.a.profiles...)
	prev := map[int]string{}
	for i, f := range c.fields {
		prev[i] = f.value()
	}
	c.fields = []*cfgField{
		{label: "Device", options: devices},
		{label: "Parallel", options: []string{"off", "auto", "2", "3", "4"}},
		{label: "Build", options: []string{"build & install", "skip build (use installed app)"}},
		{label: "Boot emulator", options: []string{"if none free", "never"}},
		{label: "Profile", options: profiles},
		{label: "Fail fast", options: []string{"no", "yes"}},
		{label: "Record video", options: []string{"no", "yes"}},
		{label: "Interception", options: []string{"runtime override"}},
		{label: "Device proxy", options: []string{"clear if set", "refuse device"}},
		{label: "Virtual keyboard", options: []string{"leave as is", "hidden", "shown"}},
		{label: "FBRC overrides", text: true},
	}
	if c.a.opts.Platform != "ios" {
		c.fields[fIntercept].options = append(c.fields[fIntercept].options, "map remote (Proxyman/Charles, port 18080)")
	}
	// Keep previous choices when they still exist.
	for i, f := range c.fields {
		for j, o := range f.options {
			if o == prev[i] {
				f.idx = j
			}
		}
	}
	c.cursor = 0
}

func (c *configModel) options() (engine.Options, error) {
	o := c.a.opts
	o.FBRC = map[string]string{}
	o.Tags = []string{"smoke"}
	if !c.explicit && c.byTag != "" {
		o.Tags = []string{c.byTag}
	} else {
		for _, f := range c.flows {
			o.FlowArgs = append(o.FlowArgs, c.a.p.Rel(f.Path))
		}
	}
	if d := c.fields[fDevice].value(); d != "auto" {
		o.DeviceArg = d
	}
	if p := c.fields[fParallel].value(); p != "off" {
		o.Parallel = p
		o.DeviceArg = ""
	}
	o.SkipBuild = c.fields[fBuild].idx == 1
	o.BootRequest = c.fields[fBoot].idx == 0
	if p := c.fields[fProfile].value(); p != "flow default" {
		o.ProfileArg = p
	}
	o.FailFast = c.fields[fFailFast].idx == 1
	o.Record = c.fields[fRecord].idx == 1
	o.MapRemote = c.fields[fIntercept].idx == 1
	o.ClearProxy = c.fields[fProxy].idx == 0 && !o.MapRemote
	if kb := c.fields[fKeyboard].value(); kb != "leave as is" {
		o.Keyboard = kb
	}
	for _, part := range strings.Split(c.fbrc.Value(), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return o, fmt.Errorf("FBRC entries must be key=value")
		}
		o.FBRC[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return o, o.Validate("run")
}

// commandLine renders the equivalent `trun run` invocation.
func commandLine(o engine.Options) string {
	parts := []string{"trun run"}
	if len(o.FlowArgs) == 0 {
		parts = append(parts, "--tags "+strings.Join(o.Tags, ","))
	}
	if o.DeviceArg != "" {
		parts = append(parts, "--device "+o.DeviceArg)
	}
	if o.Parallel != "" {
		parts = append(parts, "--parallel "+o.Parallel)
	}
	if o.SkipBuild {
		parts = append(parts, "--skip-build")
	}
	if o.BootRequest {
		parts = append(parts, "--boot-emulator")
	}
	if o.ProfileArg != "" {
		parts = append(parts, "--profile "+o.ProfileArg)
	}
	if o.FailFast {
		parts = append(parts, "--fail-fast")
	}
	if o.Record {
		parts = append(parts, "--record")
	}
	if o.MapRemote {
		parts = append(parts, "--map-remote")
	}
	if o.ClearProxy {
		parts = append(parts, "--clear-proxy")
	}
	if o.Keyboard != "" {
		parts = append(parts, "--keyboard "+o.Keyboard)
	}
	for _, k := range slices.Sorted(maps.Keys(o.FBRC)) {
		parts = append(parts, fmt.Sprintf("--fbrc '%s=%s'", k, o.FBRC[k]))
	}
	parts = append(parts, o.FlowArgs...)
	return strings.Join(parts, " ")
}

func (c *configModel) key(k tea.KeyMsg) tea.Cmd {
	if c.editing {
		switch k.String() {
		case "enter", "esc", "tab":
			c.editing = false
			c.fbrc.Blur()
			return nil
		}
		var cmd tea.Cmd
		c.fbrc, cmd = c.fbrc.Update(k)
		return cmd
	}
	f := c.fields[c.cursor]
	switch k.String() {
	case "esc", "q":
		c.active = false
	case "up", "k", "shift+tab":
		if c.cursor > 0 {
			c.cursor--
		}
	case "down", "j", "tab":
		if c.cursor < len(c.fields)-1 {
			c.cursor++
		}
	case "left", "h":
		if !f.text && len(f.options) > 0 {
			f.idx = (f.idx + len(f.options) - 1) % len(f.options)
		}
	case "right", "l", " ":
		if f.text {
			c.editing = true
			return c.fbrc.Focus()
		}
		if len(f.options) > 0 {
			f.idx = (f.idx + 1) % len(f.options)
		}
	case "e":
		if f.text {
			c.editing = true
			return c.fbrc.Focus()
		}
	case "enter", "s":
		if f.text && k.String() == "enter" {
			c.editing = true
			return c.fbrc.Focus()
		}
		o, err := c.options()
		if err != nil {
			c.err = err.Error()
			return nil
		}
		if c.a.runv.running() {
			c.err = "a run is already in progress (tab 3)"
			return nil
		}
		c.active = false
		c.a.tab = tabRun
		return c.a.runv.start(o, len(c.flows))
	}
	return nil
}

func (c *configModel) view(w, h int) (string, string) {
	var b strings.Builder
	what := fmt.Sprintf("%d flow(s)", len(c.flows))
	if !c.explicit && c.byTag != "" {
		what += " tagged " + sSel.Render(c.byTag)
	}
	b.WriteString(sBold.Render("Run "+what) + "\n\n")
	for i, f := range c.fields {
		cur := "  "
		label := pad(f.label, 16)
		if i == c.cursor {
			cur = sSel.Render("› ")
			label = sSel.Render(label)
		}
		var val string
		if f.text {
			val = c.fbrc.View()
			if c.fbrc.Value() == "" && !c.editing {
				val = sMuted.Render("none (enter to edit)")
			}
		} else {
			val = "‹ " + f.value() + " ›"
			if i != c.cursor {
				val = f.value()
			}
		}
		b.WriteString(cur + label + val + "\n")
	}
	b.WriteString("\n")
	if o, err := c.options(); err == nil {
		b.WriteString(sMuted.Render("Equivalent command:") + "\n  " + truncate(commandLine(o), w-8) + "\n")
	} else {
		b.WriteString(sBad.Render(err.Error()) + "\n")
	}
	if c.err != "" {
		b.WriteString("\n" + sBad.Render(c.err) + "\n")
	}
	b.WriteString("\n" + sMuted.Render("Flows:") + "\n")
	for i, f := range c.flows {
		if i >= h-len(c.fields)-14 {
			b.WriteString(sMuted.Render(fmt.Sprintf("  … and %d more", len(c.flows)-i)) + "\n")
			break
		}
		b.WriteString("  " + f.Rel + "\n")
	}
	help := "↑↓ field · ←→ change · enter start · esc back"
	if c.editing {
		help = "type key=value pairs separated by commas · enter done"
	}
	return panel("Configure run", b.String(), w, h), help
}
