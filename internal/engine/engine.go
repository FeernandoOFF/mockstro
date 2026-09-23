// Package engine orchestrates runs: device selection, locks, build/install, WireMock, flow
// execution, verification and reporting. Front ends (CLI printer, TUI) observe it via events.
package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/feernandooff/trun/internal/execx"
	"github.com/feernandooff/trun/internal/fixtures"
	"github.com/feernandooff/trun/internal/lock"
	"github.com/feernandooff/trun/internal/paths"
	"github.com/feernandooff/trun/internal/platform"
	"github.com/feernandooff/trun/internal/project"
	"github.com/feernandooff/trun/internal/registry"
)

type Options struct {
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
	Record      bool
	RecordDir   string
	Optimize    bool
	Attach      bool // mock only
	JUnitPath   string
	ClearProxy  bool   // remove a device-wide HTTP proxy instead of refusing the device
	Keyboard    string // "", "hidden" or "shown": on-screen keyboard state to apply before flows

	// Set by a parallel parent for its workers.
	SessionAPK    string
	SessionAPKSHA string
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
	if opts.Platform == "" {
		opts.Platform = p.Platform
	}
	if opts.FBRC == nil {
		opts.FBRC = map[string]string{}
	}
	if sink == nil {
		sink = func(Event) {}
	}
	if p.GitCommonDir != "" {
		paths.RememberProject(p.Root, p.LegacySharedRoot())
	}
	e := &Engine{P: p, Store: fixtures.New(p), Exec: ex, Opts: opts, Sink: sink}
	e.Plat = platform.New(p, opts.Platform, ex)
	e.Owner = ResolveOwner(opts.Owner)
	return e
}

func (e *Engine) emit(ev Event) {
	ev.Time = time.Now()
	e.Sink(ev)
}

func (e *Engine) Logf(format string, args ...any) {
	e.emit(Event{Kind: EvLog, Level: "info", Text: fmt.Sprintf(format, args...)})
}

func (e *Engine) Warnf(format string, args ...any) {
	e.emit(Event{Kind: EvLog, Level: "warn", Text: fmt.Sprintf(format, args...)})
}

// Lock and registry roots: global first (authoritative, cross-project), then the repository's
// legacy test.sh roots so a test.sh run and a trun run never share a device or port.
// Every known project's legacy root is included, so test.sh runs in other repositories count.
func (e *Engine) LockRoots() []string {
	return e.roots(paths.LocksRoot(), "locks")
}

func (e *Engine) RegistryRoots() []string {
	return e.roots(paths.RegistryRoot(), "runs")
}

func (e *Engine) roots(global, sub string) []string {
	out := []string{global}
	seen := map[string]bool{global: true}
	for _, shared := range append([]string{e.P.LegacySharedRoot()}, paths.LegacyRoots()...) {
		r := filepath.Join(shared, sub)
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
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

// NewRunID: <utc stamp>-<pid>-<hex>, the shape test.sh used (the app accepts [A-Za-z0-9._-]).
func NewRunID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d-%x", time.Now().UTC().Format("20060102T150405Z"), os.Getpid(), b)
}

func (e *Engine) newLocks(runID string) *lock.Manager {
	return &lock.Manager{Roots: e.LockRoots(), Exec: e.Exec, RunID: runID, Who: e.Owner}
}

func (e *Engine) registry() *registry.Registry { return &registry.Registry{Roots: e.RegistryRoots()} }

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
		if f.MatchesPlatform(e.Opts.Platform) && f.MatchesTags(e.Opts.Tags) {
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
	out := map[string]string{}
	if h != nil {
		maps.Copy(out, h.FBRC)
	}
	maps.Copy(out, e.Opts.FBRC)
	return out
}

// CheckEnvironment verifies the external tools a command needs.
func (e *Engine) CheckEnvironment(command string) error {
	var need []string
	switch command {
	case "setup", "run", "mock", "record-api", "playback":
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
		if _, err := e.Exec.LookPath(n); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		hints := map[string]string{"maestro": "brew install mobile-dev-inc/tap/maestro", "HandBrakeCLI": "brew install handbrake", "java": "a JDK 17+", "adb": "Android platform-tools", "xcrun": "Xcode command line tools"}
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

// Validate checks the option combinations test.sh enforced in parse_args.
func (o *Options) Validate(command string) error {
	if o.ServerPort < 1 || o.ServerPort > 65535 {
		return fmt.Errorf("--server-port must be a number from 1 to 65535")
	}
	if o.Platform != "" && o.Platform != "android" && o.Platform != "ios" {
		return fmt.Errorf("unknown platform: %s", o.Platform)
	}
	if o.MapRemote {
		if command != "run" {
			return fmt.Errorf("--map-remote is only valid with run")
		}
		if o.Platform == "ios" {
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
	if o.DeviceClass == "physical" && !o.MapRemote {
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
		} else if !strings.HasPrefix(o.DeviceArg, "emulator-") {
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
			return fmt.Errorf("mock --attach requires --device <emulator-serial>")
		}
		if !o.Attach && o.DeviceArg != "" {
			return fmt.Errorf("mock --device requires explicit --attach")
		}
	}
	if o.SkipBuild && o.SessionAPK != "" {
		return fmt.Errorf("--skip-build cannot be combined with a session APK")
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
