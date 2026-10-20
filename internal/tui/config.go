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
	"github.com/feernandooff/mockstro/internal/project"
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
	if f.idx < len(f.vals) && (f.vals[f.idx] != "" || f.label == "Platforms") {
		return f.vals[f.idx]
	}
	return f.options[f.idx]
}

// configModel is the run setup form; it shows the equivalent CLI command.
type configModel struct {
	a         *app
	active    bool
	editing   bool
	flows     []*fixtures.Flow
	byTag     string
	explicit  bool
	fields    []*cfgField
	cursor    int
	fbrc      textinput.Model
	envFiles  textinput.Model
	variables textinput.Model
	only      map[string][]string
	err       string
	note      string
	real      map[string]bool // device serials that are phones, not emulators
}

const (
	fPlatform = iota
	fDevice
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
	fEnvFiles
	fVariables
)

func newConfigModel(a *app) *configModel {
	ti := textinput.New()
	ti.Placeholder = "key=value, key=value"
	ti.Prompt = ""
	ti.CharLimit = 2000
	files := textinput.New()
	files.Prompt = ""
	files.Placeholder = "vars.yaml, local-vars.yaml"
	vars := textinput.New()
	vars.Prompt = ""
	vars.Placeholder = "KEY=value, OTHER=value"
	return &configModel{a: a, fbrc: ti, envFiles: files, variables: vars}
}

func (c *configModel) open(flows []*fixtures.Flow, tag string, explicit bool) {
	c.active, c.flows, c.byTag, c.explicit, c.err, c.note = true, flows, tag, explicit || c.a.flowTab.filter.Value() != "", "", ""
	c.only = nil
	if c.fields == nil {
		c.envFiles.SetValue(strings.Join(c.a.opts.EnvFiles, ", "))
		var inputs []string
		for _, key := range slices.Sorted(maps.Keys(c.a.opts.Variables)) {
			inputs = append(inputs, key+"="+c.a.opts.Variables[key])
		}
		c.variables.SetValue(strings.Join(inputs, ", "))
	}
	targets, _ := c.a.p.Selected("")
	var platforms, platformValues []string
	if len(targets) > 1 {
		platforms = append(platforms, "both")
		platformValues = append(platformValues, "")
	}
	for _, target := range targets {
		platforms = append(platforms, target.Platform)
		platformValues = append(platformValues, target.Platform)
	}
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
		{group: "Run", label: "Platforms", options: platforms, vals: platformValues},
		{group: "Run", label: "Device", options: devices, vals: serials},
		{group: "Run", label: "Workers per platform", options: []string{"1", "auto", "2", "3", "4"}},
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
		{group: "Inputs", label: "Variable files", text: true},
		{group: "Inputs", label: "Variables", text: true},
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
	if c.a.opts.Platform != "" {
		for i, value := range platformValues {
			if value == c.a.opts.Platform {
				c.fields[fPlatform].idx = i
			}
		}
	}
	c.cursor = fDevice
	if len(targets) > 1 {
		c.cursor = fPlatform
	}
}

func (c *configModel) options() (engine.Options, error) {
	o := c.a.opts
	o.Platform = c.fields[fPlatform].value()
	o.FlowArgs = nil
	o.DeviceArg = ""
	o.Parallel = ""
	o.ProfileArg = ""
	o.Keyboard = ""
	o.Only = c.only
	o.Lite = false
	targets, err := c.a.p.Selected(o.Platform)
	if err != nil {
		return o, err
	}
	for _, target := range targets {
		o.Lite = o.Lite || target.Lite()
	}
	o.EnvFiles = nil
	for _, file := range strings.Split(c.envFiles.Value(), ",") {
		if file = strings.TrimSpace(file); file != "" {
			o.EnvFiles = append(o.EnvFiles, file)
		}
	}
	o.Variables = map[string]string{}
	for _, input := range strings.Split(c.variables.Value(), ",") {
		if input = strings.TrimSpace(input); input == "" {
			continue
		}
		key, value, ok := strings.Cut(input, "=")
		if !ok {
			return o, fmt.Errorf("variables must be KEY=value")
		}
		key = strings.TrimSpace(key)
		if err := project.ValidateVariable(key); err != nil {
			return o, err
		}
		o.Variables[key] = value
	}
	o.FBRC = map[string]string{}
	o.Tags = []string{"smoke"}
	if !c.explicit && c.byTag != "" {
		o.Tags = []string{c.byTag}
	} else {
		for _, f := range c.flows {
			o.FlowArgs = append(o.FlowArgs, c.a.p.Rel(f.Path))
		}
	}
	real := len(targets) == 1 && c.realDevice()
	if d := c.fields[fDevice].value(); len(targets) == 1 && d != "auto" {
		o.DeviceArg = d
	}
	o.MapRemote = real || c.fields[fIntercept].idx == 1 || (o.Lite && len(targets) == 1)
	if p := c.fields[fParallel].value(); p != "1" && !o.MapRemote {
		o.Parallel = p
		o.DeviceArg = ""
	}
	o.SkipBuild = c.fields[fBuild].idx == 1 || (o.Lite && len(targets) == 1)
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
	if len(targets) > 1 && o.DeviceArg != "" {
		return o, fmt.Errorf("choose one platform to select a specific device")
	}
	for _, target := range targets {
		child := o
		child.Platform = target.Platform
		child.Lite = target.Lite()
		if child.Lite {
			child.Parallel = ""
		}
		if err := child.Validate("run"); err != nil {
			return o, err
		}
	}
	return o, nil
}

// commandLine renders the equivalent `mockstro run` invocation.
func commandLine(o engine.Options) string {
	if o.Only != nil {
		var commands []string
		for _, name := range []string{"", "android", "ios"} {
			paths := o.Only[name]
			if len(paths) == 0 || (o.Platform != "" && name != "" && name != o.Platform) {
				continue
			}
			child := o
			child.Only = nil
			child.FlowArgs = paths
			child.Platform = name
			if name == "" {
				child.Platform = o.Platform
			}
			commands = append(commands, commandLine(child))
		}
		return strings.Join(commands, "\n")
	}
	parts := []string{"./mockstrow run"}
	if o.Platform != "" {
		parts = append(parts, "--platform "+o.Platform)
	}
	for _, file := range o.EnvFiles {
		parts = append(parts, "--env-file "+shellQuote(file))
	}
	for _, key := range slices.Sorted(maps.Keys(o.Variables)) {
		parts = append(parts, "-e "+shellQuote(key+"="+o.Variables[key]))
	}

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
	if o.Only == nil {
		for _, path := range o.FlowArgs {
			parts = append(parts, shellQuote(path))
		}
	}
	return strings.Join(parts, " ")
}

func (c *configModel) key(k tea.KeyMsg) tea.Cmd {
	if c.editing {
		switch k.String() {
		case "enter", "esc", "tab":
			c.editing = false
			c.input().Blur()
			return nil
		}
		var cmd tea.Cmd
		input := c.input()
		*input, cmd = input.Update(k)
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
			return c.input().Focus()
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
			return c.input().Focus()
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
	real, lite := c.realDevice(), false
	targets, _ := c.a.p.Selected(c.fields[fPlatform].value())
	for _, target := range targets {
		lite = lite || target.Lite()
	}
	real = real && len(targets) == 1
	mapRemote := real || (lite && len(targets) == 1) || c.fields[fIntercept].idx == 1
	switch {
	case len(targets) > 1 && i == fDevice:
		return "auto per platform (choose one platform for a device)"
	case lite && len(targets) == 1 && i == fBuild:
		return "installed app (lite: mockstro does not build)"
	case lite && len(targets) == 1 && i == fParallel:
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
	focusLine := 0
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
			focusLine = strings.Count(b.String(), "\n")
			cur = sSel.Render("› ")
			label = sSel.Render(label)
		}
		var val string
		if f.text {
			input := c.fieldInput(i)
			val = input.View()
			if input.Value() == "" && !c.editing {
				val = sMuted.Render("none (e to edit)")
			}
		} else if c.disabled(i) {
			label = sMuted.Render(label)
			val = sMuted.Render(c.disabledReason(i))
		} else {
			choice := f.value()
			if i == fPlatform {
				choice = f.options[f.idx]
			}
			val = "‹ " + choice + " ›"
			if i != c.cursor {
				val = choice
			}
		}
		b.WriteString(cur + label + val + "\n")
	}
	b.WriteString("\n")
	if o, err := c.options(); err == nil {
		targets, _ := c.a.p.Selected(o.Platform)
		mode := "concurrent"
		for _, target := range targets {
			if target.Lite() || o.MapRemote {
				mode = "sequential (lite/proxy)"
			}
		}
		b.WriteString(fmt.Sprintf("Targets (%s): ", mode))
		for _, target := range targets {
			count := 0
			for _, flow := range c.flows {
				if len(o.FlowArgs) > 0 || flow.MatchesTags(o.Tags) {
					count++
				}
			}
			if o.Only != nil {
				count = len(o.Only[target.Platform])
				if count == 0 {
					count = len(o.Only[""])
				}
			}
			if count > 0 {
				b.WriteString(fmt.Sprintf("%s %d flows  ", target.Platform, count))
			}
		}
		b.WriteString("\n")
		if o.Parallel == "auto" {
			b.WriteString(sMuted.Render("Auto uses running free devices (one if none are running); choose 1 to limit each platform.") + "\n")
		}
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
	return panel("Configure run", strings.Join(visibleLines(strings.Split(b.String(), "\n"), focusLine, h-3), "\n"), w, h), help
}

// shellQuote single-quotes s when a shell would split or expand it (wireless adb serials).
func shellQuote(s string) string {
	if strings.ContainsAny(s, " ()'\"$`\\&;|<>*?") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func (c *configModel) fieldInput(i int) *textinput.Model {
	switch i {
	case fEnvFiles:
		return &c.envFiles
	case fVariables:
		return &c.variables
	default:
		return &c.fbrc
	}
}
func (c *configModel) input() *textinput.Model { return c.fieldInput(c.cursor) }

func (c *configModel) openResults(results []*engine.Result, failedOnly bool) {
	var flows []*fixtures.Flow
	seen := map[string]bool{}
	var selected []*engine.Result
	for _, r := range results {
		if failedOnly && (r.Passed() || unfinished(r)) {
			continue
		}
		selected = append(selected, r)
		for _, f := range c.a.flows {
			if (r.Path == f.Path || (r.Feature == f.Feature && r.File == f.File)) && !seen[f.Path] {
				seen[f.Path] = true
				flows = append(flows, f)
			}
		}
	}
	if len(flows) == 0 {
		return
	}
	c.a.tab = tabFlows
	c.open(flows, "", true)
	pairs := map[string][]string{}
	for _, r := range selected {
		path := r.Path
		if path == "" {
			for _, f := range flows {
				if r.Feature == f.Feature && r.File == f.File {
					path = f.Path
				}
			}
		}
		if path != "" {
			pairs[r.Platform] = append(pairs[r.Platform], path)
		}
	}
	c.only = pairs
	selectedPlatform := ""
	if len(pairs) == 1 {
		for name := range pairs {
			selectedPlatform = name
		}
	}
	for i, value := range c.fields[fPlatform].vals {
		if value == selectedPlatform {
			c.fields[fPlatform].idx = i
		}
	}
}
