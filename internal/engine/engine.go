// Package engine orchestrates runs: device selection, locks, build/install, WireMock, flow
// execution, verification and reporting. Front ends (CLI printer, TUI) observe it via events.
package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/build"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/lock"
	"github.com/feernandooff/mockstro/internal/maestro"
	"github.com/feernandooff/mockstro/internal/paths"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/registry"
	"github.com/feernandooff/mockstro/internal/remoteconfig"
)

type Options struct {
	EnvFiles    []string
	Variables   project.Variables
	Only        map[string][]string // exact flow/platform pairs for reruns
	managed     bool                // coordinator owns pruning, last-run and extra JUnit output
	Tags        []string
	FlowArgs    []string
	Platform    string
	DeviceArg   string
	ServerPort  int
	PortPinned  bool
	ProfileArg  string
	FBRC        map[string]string // CLI overrides; win over the flow's own
	Parallel    string            // "", "auto" or a count
	BootAVD     string
	BootRequest bool
	DeviceClass string // emulator | physical | any
	MapRemote   bool
	Owner       string
	SkipBuild   bool
	FailFast    bool
	Isolate     bool // one Maestro process per flow instead of one per batch
	Record      bool
	RecordDir   string
	Optimize    bool
	Attach      bool // mock only
	JUnitPath   string
	ClearProxy  bool   // remove a device-wide HTTP proxy instead of refusing the device
	Keyboard    string // "", "hidden" or "shown": on-screen keyboard state to apply before flows
	Headless    bool   // boot emulators without a window / simulators without Simulator.app
	EmuCores    int    // override AVD vCPUs for emulators mockstro boots (0 = AVD default)
	EmuMemoryMB int    // override AVD RAM for emulators mockstro boots (0 = AVD default)
	EmuGPU      string // emulator -gpu mode for emulators mockstro boots ("" = emulator default)
	EmuSnapshot string // boot emulators from this named snapshot
	// Lite: a lite-integration project (configured as lite). Runs are always --map-remote on
	// the installed app, one device at a time; see docs/integration.md.
	Lite bool

	// Set by a parallel parent for its workers.
	SessionArtifact build.Artifact
}

func DefaultOptions() Options {
	return Options{Tags: []string{"smoke"}, ServerPort: project.ReservedMockPort, FBRC: map[string]string{}}
}

type Engine struct {
	P     *project.Project
	Store *fixtures.Store
	Exec  execx.Runner
	Plat  platform.Platform
	Opts  Options
	Sink  Sink
	// ChooseAVD lets an interactive front end pick what to boot; nil when unattended.
	ChooseAVD func(avds []string) (string, bool)
	Owner     string

	jar string
}

func New(p *project.Project, opts Options, ex execx.Runner, sink Sink) *Engine {
	if len(p.Targets) > 0 {
		name := opts.Platform
		if name == "" {
			name = p.Platform
		}
		if target := p.Targets[name]; target != nil {
			p = target
		}
	}
	if opts.Platform == "" {
		opts.Platform = p.Platform
	}
	copy := *p
	p = &copy
	opts.Lite = p.Lite()
	if opts.FBRC == nil {
		opts.FBRC = map[string]string{}
	}
	if sink == nil {
		sink = func(Event) {}
	}
	e := &Engine{P: p, Store: fixtures.New(p), Exec: ex, Opts: opts, Sink: sink}
	if p.AppID == "" {
		p.AppID = e.flowsAppID()
	}
	e.Plat = platform.New(p, opts.Platform, ex)
	switch p := e.Plat.(type) {
	case *platform.IOS:
		p.BootKeyboard = opts.Keyboard
	case *platform.Android:
		p.Cores, p.MemoryMB, p.GPU = opts.EmuCores, opts.EmuMemoryMB, opts.EmuGPU
		p.Snapshot = opts.EmuSnapshot
	}
	e.Owner = ResolveOwner(opts.Owner)
	return e
}

func (e *Engine) emit(ev Event) {
	ev.Time = time.Now()
	ev.Platform = e.Opts.Platform
	e.Sink(ev)
}

func (e *Engine) Logf(format string, args ...any) {
	e.emit(Event{Kind: EvLog, Level: "info", Text: fmt.Sprintf(format, args...)})
}

func (e *Engine) Warnf(format string, args ...any) {
	e.emit(Event{Kind: EvLog, Level: "warn", Text: fmt.Sprintf(format, args...)})
}

// ResolveOwner: explicit flag, env, CI, agent env, interactive tty; otherwise agent, the
// conservative default that keeps away from the developer's devices.
func ResolveOwner(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("MAESTRO_RUN_OWNER"); v != "" {
		return v
	}
	if os.Getenv("CI") != "" || os.Getenv("BUILD_NUMBER") != "" {
		return "ci"
	}
	if os.Getenv("CLAUDECODE") != "" || os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" || os.Getenv("CURSOR_TRACE_ID") != "" {
		return "agent"
	}
	if isTTY(os.Stdin) && isTTY(os.Stdout) {
		return "human"
	}
	return "agent"
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// NewRunID: <utc stamp>-<pid>-<hex> (the app accepts [A-Za-z0-9._-]).
func NewRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d-%x", time.Now().UTC().Format("20060102T150405Z"), os.Getpid(), b)
}

func (e *Engine) newLocks(runID string) *lock.Manager {
	return &lock.Manager{Root: paths.LocksRoot(), Exec: e.Exec, RunID: runID, Who: e.Owner}
}

func (e *Engine) registry() *registry.Registry { return &registry.Registry{Root: paths.RegistryRoot()} }

// DiscoverFlows resolves explicit paths or filters flows/ by tag and platform.
func (e *Engine) DiscoverFlows() ([]*fixtures.Flow, error) {
	if len(e.Opts.FlowArgs) > 0 {
		seen := map[string]bool{}
		var out []*fixtures.Flow
		for _, arg := range e.Opts.FlowArgs {
			f, err := e.Store.Flow(arg)
			if err != nil {
				return nil, err
			}
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			out = append(out, f)
		}
		return out, nil
	}
	all, err := e.Store.Flows()
	if err != nil {
		return nil, err
	}
	var out []*fixtures.Flow
	for _, f := range all {
		if f.MatchesTags(e.Opts.Tags) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Maestro flows matched tags: %s", strings.Join(e.Opts.Tags, ","))
	}
	return out, nil
}

// FBRCFor merges the flow's Remote Config values with CLI overrides (CLI wins).
func (e *Engine) FBRCFor(h *fixtures.Header) map[string]string {
	if h == nil {
		return remoteconfig.Copy(e.Opts.FBRC)
	}
	return remoteconfig.Merge(h.FBRC, e.Opts.FBRC)
}

// CheckEnvironment verifies the external tools a command needs.
func (e *Engine) CheckEnvironment(command string) error {
	var need []string
	switch command {
	case "check", "run", "mock", "record-api", "playback":
		need = append(need, "java", "lsof")
	}
	if command == "run" || (command == "mock" && e.Opts.Attach) {
		if e.Opts.Platform == "ios" {
			need = append(need, "xcrun")
		} else {
			need = append(need, "adb")
		}
	}
	if command == "run" {
		need = append(need, "maestro")
	}
	if e.Opts.Optimize {
		need = append(need, "HandBrakeCLI")
	}
	var missing []string
	for _, n := range need {
		_, err := e.Exec.LookPath(n)
		if n == "maestro" {
			_, err = maestro.Command(e.Exec)
		}
		if err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		hints := map[string]string{"maestro": "run `./mockstrow check` to install", "HandBrakeCLI": "brew install handbrake", "java": javaInstallHint, "adb": "Android platform-tools", "xcrun": "Xcode command line tools"}
		var parts []string
		for _, m := range missing {
			if h, ok := hints[m]; ok {
				parts = append(parts, m+" ("+h+")")
			} else {
				parts = append(parts, m)
			}
		}
		return fmt.Errorf("missing required command(s): %s", strings.Join(parts, ", "))
	}
	return nil
}

// Validate rejects invalid option combinations.
func (o *Options) Validate(command string) error {
	if o.ServerPort < 1 || o.ServerPort > 65535 {
		return fmt.Errorf("--server-port must be a number from 1 to 65535")
	}
	if o.Lite {
		const why = " needs full integration (.maestro/mockstro.yaml and the app's override seam; see docs/integration.md)"
		switch {
		case o.Parallel != "":
			return fmt.Errorf("--parallel" + why)
		case o.Attach:
			return fmt.Errorf("mock --attach" + why)
		}
		if command == "run" {
			o.MapRemote, o.SkipBuild = true, true
		}
	}
	if o.Platform != "" && o.Platform != "android" && o.Platform != "ios" {
		return fmt.Errorf("unknown platform: %s", o.Platform)
	}
	if o.MapRemote {
		if command != "run" {
			return fmt.Errorf("--map-remote is only valid with run")
		}
		if o.Platform == "ios" && !o.Lite {
			return fmt.Errorf("--map-remote is Android only")
		}
		if o.Parallel != "" {
			return fmt.Errorf("--map-remote cannot run in parallel: it owns port %d", project.ReservedMockPort)
		}
		if o.PortPinned && o.ServerPort != project.ReservedMockPort {
			return fmt.Errorf("--map-remote always uses port %d", project.ReservedMockPort)
		}
		o.ServerPort, o.PortPinned = project.ReservedMockPort, true
	}
	manualAndroidMock := command == "mock" && o.Attach && o.Platform != "ios"
	if o.DeviceClass == "physical" && !o.MapRemote && !manualAndroidMock {
		return fmt.Errorf("only designated emulators are supported; use --map-remote for a physical device")
	}
	if o.Parallel != "" {
		if command != "run" {
			return fmt.Errorf("--parallel is only valid with run")
		}
		if o.Parallel != "auto" {
			n, err := strconv.Atoi(o.Parallel)
			if err != nil || n < 1 || n > 99 {
				return fmt.Errorf("--parallel requires a positive number or 'auto'")
			}
		}
	}
	if (o.BootRequest || o.BootAVD != "") && command != "run" {
		return fmt.Errorf("--boot-emulator is only valid with run")
	}
	if o.DeviceArg != "" && command != "run" && command != "mock" {
		return fmt.Errorf("--device is only valid with run or mock")
	}
	if o.DeviceArg != "" && !o.MapRemote {
		if o.Platform == "ios" {
			if !isUDID(o.DeviceArg) {
				return fmt.Errorf("--device must be a simulator UDID")
			}
		} else if !manualAndroidMock && !strings.HasPrefix(o.DeviceArg, "emulator-") {
			return fmt.Errorf("--device must name a designated emulator")
		}
	}
	if o.ProfileArg != "" && command != "run" && command != "mock" {
		return fmt.Errorf("--profile is only valid with run or mock")
	}
	if len(o.FBRC) > 0 && command != "run" && command != "mock" {
		return fmt.Errorf("--fbrc is only valid with run or mock")
	}
	if o.Keyboard != "" && o.Keyboard != "hidden" && o.Keyboard != "shown" {
		return fmt.Errorf("--keyboard requires hidden or shown")
	}
	if o.Attach && command != "mock" {
		return fmt.Errorf("--attach is only valid with mock")
	}
	if command == "mock" {
		if o.PortPinned && o.ServerPort != project.ReservedMockPort {
			return fmt.Errorf("mock always uses fixed port %d", project.ReservedMockPort)
		}
		o.ServerPort, o.PortPinned = project.ReservedMockPort, true
		if o.Attach && o.DeviceArg == "" {
			return fmt.Errorf("mock --attach requires --device <serial-or-simulator-UDID>")
		}
		if !o.Attach && o.DeviceArg != "" {
			return fmt.Errorf("mock --device requires explicit --attach")
		}
	}
	if o.SkipBuild && o.SessionArtifact.Path != "" {
		return fmt.Errorf("--skip-build cannot be combined with a session artifact")
	}
	return nil
}

func isUDID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// Background returns a context that survives the caller's cancellation, for cleanup.
func cleanupCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// flowsAppID supports programmatic engine users without a configured app ID.
func (e *Engine) flowsAppID() string {
	flows, _ := e.Store.Flows()
	count, best := map[string]int{}, ""
	for _, f := range flows {
		if id := f.Header.AppID; id != "" {
			count[id]++
			if count[id] > count[best] {
				best = id
			}
		}
	}
	return best
}
