package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/fixtures"
)

type cfgField struct {
	group   string
	label   string
	options []string
	vals    []string // what each option means when it differs from its label
	idx     int
	text    bool // free-text field
}

func (f *cfgField) value() string {
	if f.text || len(f.options) == 0 {
		return ""
	}
	if f.idx < len(f.vals) && f.vals[f.idx] != "" {
		return f.vals[f.idx]
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
	note     string
	real     map[string]bool // device serials that are phones, not emulators
}

const (
	fDevice = iota
	fParallel
	fBuild
	fProfile
	fFailFast
	fRecord
	fWindow
	fKeyboard
	fBoot
	fIntercept
	fProxy
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
	c.active, c.flows, c.byTag, c.explicit, c.err, c.note = true, flows, tag, explicit || c.a.flowTab.filter.Value() != "", "", ""
	devices := []string{"auto"}
	var serials []string
	c.real = map[string]bool{}
	if st := c.a.status; st != nil {
		for _, d := range st.Available.Eligible {
			if !slices.Contains(st.Available.Physical, d) {
				devices = append(devices, d)
			}
		}
		// A real device only runs with map remote: the user's own proxy does the mocking.
		for _, d := range st.Devices {
			if d.Class == "physical" && d.LockedBy == nil && c.a.opts.Platform != "ios" {
				label := d.Serial
				if d.Name != "" {
					label = d.Name + " · " + d.Serial
				}
				for len(serials) < len(devices) {
					serials = append(serials, "")
				}
				devices = append(devices, label+"  (real device · map remote)")
				serials = append(serials, d.Serial)
				c.real[d.Serial] = true
			}
		}
	}
	profiles := append([]string{"flow default"}, c.a.profiles...)
	prev := map[int]string{}
	for i, f := range c.fields {
		if f.idx < len(f.options) {
			prev[i] = f.options[f.idx]
		}
	}
	c.fields = []*cfgField{
		{group: "Run", label: "Device", options: devices, vals: serials},
		{group: "Run", label: "Parallel", options: []string{"off", "auto", "2", "3", "4"}},
		{group: "Run", label: "Build", options: []string{"build & install", "skip build (use installed app)"}},
		{group: "Run", label: "Profile", options: profiles},
		{group: "Run", label: "Fail fast", options: []string{"no", "yes"}},
		{group: "Run", label: "Record video", options: []string{"no", "yes"}},
		{group: "Device", label: "Emulator window", options: []string{"shown", "headless (faster in parallel)"}},
		{group: "Device", label: "Virtual keyboard", options: []string{"leave as is", "hidden", "shown"}},
		{group: "Device", label: "Boot emulator", options: []string{"if none free", "never"}},
		{group: "Network", label: "Interception", options: []string{"runtime override"}},
		{group: "Network", label: "Device proxy", options: []string{"clear if set", "refuse device"}},
		{group: "Network", label: "FBRC overrides", text: true},
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
	real := c.realDevice()
	if d := c.fields[fDevice].value(); d != "auto" {
		o.DeviceArg = d
	}
	o.MapRemote = real || c.fields[fIntercept].idx == 1 || o.Lite
	if p := c.fields[fParallel].value(); p != "off" && !o.MapRemote {
		o.Parallel = p
		o.DeviceArg = ""
	}
	o.SkipBuild = c.fields[fBuild].idx == 1 || o.Lite
	o.BootRequest = c.fields[fBoot].idx == 0 && !real
	if p := c.fields[fProfile].value(); p != "flow default" {
		o.ProfileArg = p
	}
	o.FailFast = c.fields[fFailFast].idx == 1
	o.Record = c.fields[fRecord].idx == 1
	o.ClearProxy = c.fields[fProxy].idx == 0 && !o.MapRemote
	o.Headless = c.fields[fWindow].idx == 1 && !real
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

// commandLine renders the equivalent `mockstro run` invocation.
func commandLine(o engine.Options) string {
	parts := []string{"mockstro run"}
	if len(o.FlowArgs) == 0 {
		parts = append(parts, "--tags "+strings.Join(o.Tags, ","))
	}
	if o.DeviceArg != "" {
		parts = append(parts, "--device "+shellQuote(o.DeviceArg))
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
	if o.Headless {
		parts = append(parts, "--headless")
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
	c.note = ""
	f := c.fields[c.cursor]
	key := map[string]string{"tab": "down", "shift+tab": "up"}[k.String()]
	if key == "" {
		key = k.String()
	}
	if cur, ok := navKey(key, c.cursor, len(c.fields)); ok {
		c.cursor = c.skipDisabled(cur, cur-c.cursor)
		return nil
	}
	switch k.String() {
	case "esc":
		c.active = false
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
	case "c":
		o, err := c.options()
		if err != nil {
			c.err = err.Error()
			return nil
		}
		if err := clipboard.WriteAll(commandLine(o)); err != nil {
			c.note = sBad.Render("copy failed: " + err.Error())
		} else {
			c.note = sOK.Render("copied")
		}
	case "e":
		if f.text {
			c.editing = true
			return c.fbrc.Focus()
		}
	case "enter":
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

func (c *configModel) realDevice() bool { return c.real[c.fields[fDevice].value()] }

// disabledReason is why field i does not apply to the current choices ("" = it does).
func (c *configModel) disabledReason(i int) string {
	real, lite := c.realDevice(), c.a.p.Lite()
	mapRemote := real || lite || c.fields[fIntercept].idx == 1
	switch {
	case lite && i == fBuild:
		return "installed app (lite: mockstro does not build)"
	case lite && i == fParallel:
		return "n/a in lite (needs full integration)"
	case lite && i == fIntercept:
		return "map remote (lite: your Proxyman/Charles)"
	case real && (i == fBoot || i == fWindow):
		return "n/a on a real device"
	case real && i == fIntercept:
		return "map remote (required on a real device)"
	case mapRemote && i == fParallel:
		return "n/a with map remote (one device)"
	case mapRemote && i == fProxy:
		return "n/a with map remote (your proxy stays)"
	}
	return ""
}

func (c *configModel) disabled(i int) bool { return c.disabledReason(i) != "" }

// skipDisabled steps past disabled fields in the direction of travel, or back if at the end.
func (c *configModel) skipDisabled(cur, dir int) int {
	step := 1
	if dir < 0 {
		step = -1
	}
	for n := cur; n >= 0 && n < len(c.fields); n += step {
		if !c.disabled(n) {
			return n
		}
	}
	for n := cur; n >= 0 && n < len(c.fields); n -= step {
		if !c.disabled(n) {
			return n
		}
	}
	return cur
}

func (c *configModel) view(w, h int) (string, string) {
	var b strings.Builder
	what := fmt.Sprintf("%d flow(s)", len(c.flows))
	if !c.explicit && c.byTag != "" {
		what += " tagged " + sSel.Render(c.byTag)
	}
	b.WriteString(sBold.Render("Run "+what) + "\n\n")
	group := ""
	for i, f := range c.fields {
		if f.group != group {
			if group != "" {
				b.WriteString("\n")
			}
			group = f.group
			b.WriteString(sMuted.Render(group) + "\n")
		}
		cur := "  "
		label := pad(f.label, 22)
		if i == c.cursor {
			cur = sSel.Render("› ")
			label = sSel.Render(label)
		}
		var val string
		if f.text {
			val = c.fbrc.View()
			if c.fbrc.Value() == "" && !c.editing {
				val = sMuted.Render("none (e to edit)")
			}
		} else if c.disabled(i) {
			label = sMuted.Render(label)
			val = sMuted.Render(c.disabledReason(i))
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
		b.WriteString(sMuted.Render("Equivalent command:") + " " + c.note + "\n  " + truncate(commandLine(o), w-8) + "\n")
	} else {
		b.WriteString(sBad.Render(err.Error()) + "\n")
	}
	if c.err != "" {
		b.WriteString("\n" + sBad.Render(c.err) + "\n")
	}
	b.WriteString("\n" + sMuted.Render("Flows:") + "\n")
	for i, f := range c.flows {
		if i >= h-len(c.fields)-19 {
			b.WriteString(sMuted.Render(fmt.Sprintf("  … and %d more", len(c.flows)-i)) + "\n")
			break
		}
		b.WriteString("  " + f.Rel + "\n")
	}
	help := navHelp + " · ←→ change · e edit · c copy command · enter start · esc back"
	if c.editing {
		help = "type key=value pairs separated by commas · enter done"
	}
	return panel("Configure run", b.String(), w, h), help
}

// shellQuote single-quotes s when a shell would split or expand it (wireless adb serials).
func shellQuote(s string) string {
	if strings.ContainsAny(s, " ()'\"$`\\&;|<>*?") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
