// Package cli is mockstro's command-line front end. Every command is scriptable and
// only prompts in a terminal; the TUI (`mockstro` in a terminal, or `mockstro tui`) drives the same engine.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"
)

// ExitError carries a specific process exit status.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return "exit " + strconv.Itoa(e.Code) }

// TUIFunc is injected by main to avoid an import cycle.
var TUIFunc func(p *project.Project, opts engine.Options) error

type globals struct {
	projectDir string
	platform   string
	owner      string
}

type runFlags struct {
	envFiles    []string
	variables   []string
	rerunFailed bool
	tags        string
	device      string
	serverPort  int
	profile     string
	fbrc        []string
	parallel    string
	boot        string
	devices     string
	mapRemote   bool
	skipBuild   bool
	failFast    bool
	isolate     bool
	record      bool
	recordDir   string
	optimize    bool
	attach      bool
	junit       string
	quiet       bool
	clearProxy  bool
	keyboard    string
	headless    bool
	emuCores    int
	emuMemory   int
	emuGPU      string
	emuSnapshot string
	jsonOut     bool
	quietStatus bool
	dryRun      bool
	showFallbk  bool
	target      string
	output      string
	recordPaths string
}

const bootAny = "\x00any"

func Execute(version string) int {
	g := &globals{}
	f := &runFlags{}
	root := &cobra.Command{
		Use:           "mockstro",
		Short:         "Maestro + WireMock end-to-end runner (Android & iOS)",
		Long:          "mockstro runs the .maestro suite of the repository it is started in. Without a command it opens the TUI in a terminal, and runs the smoke suite otherwise.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !cmd.Flags().Changed("tags") && engine.ResolveOwner(g.owner) == "human" && TUIFunc != nil {
				return runTUI(g, f)
			}
			return runCmd(cmd.Context(), g, f, args)
		},
	}
	root.PersistentFlags().StringVarP(&g.projectDir, "project", "C", "", "repository to operate on (default: nearest parent with .maestro/)")
	root.PersistentFlags().StringVar(&g.platform, "platform", "", "android or ios (default: all configured platforms)")
	root.PersistentFlags().StringVar(&g.owner, "owner", "", "human, agent or ci (default: auto-detected)")
	addRunFlags(root, f)

	run := &cobra.Command{
		Use:   "run [flow...]",
		Short: "Run matching flows (default: the smoke suite) or explicit flow paths",
		RunE:  func(cmd *cobra.Command, args []string) error { return runCmd(cmd.Context(), g, f, args) },
	}
	addRunFlags(run, f)

	mock := &cobra.Command{
		Use:   "mock [scenario|profile|flow [stub...]]",
		Short: "Serve a scenario's fixtures on port 18080 (server-only unless --attach --device)",
		RunE:  func(cmd *cobra.Command, args []string) error { return mockCmd(cmd.Context(), g, f, args) },
	}
	mock.Flags().StringVar(&f.device, "device", "", "emulator serial / simulator UDID to attach")
	mock.Flags().BoolVar(&f.attach, "attach", false, "install the build on --device and point it at the mock")
	mock.Flags().StringVar(&f.profile, "profile", "", "serve a different customer profile")
	mock.Flags().StringArrayVar(&f.fbrc, "fbrc", nil, "Remote Config override key=value (repeatable)")
	mock.Flags().IntVar(&f.serverPort, "server-port", project.ReservedMockPort, "must be 18080 for mock")
	mock.Flags().BoolVar(&f.skipBuild, "skip-build", false, "with --attach: use the build already on the device")
	mock.Flags().BoolVar(&f.clearProxy, "clear-proxy", false, "with --attach: remove a device-wide HTTP proxy first")

	var installMaestro bool
	check := &cobra.Command{Use: "check", Short: "Validate fixtures and check this machine: Java, Maestro and WireMock",
		RunE: func(cmd *cobra.Command, args []string) error { return checkCmd(cmd.Context(), g, installMaestro) }}
	check.Flags().BoolVar(&installMaestro, "install", false, "install Maestro if missing without asking")

	list := &cobra.Command{Use: "list [profiles|scenarios]", Short: "List flows, profiles or scenarios", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return listCmd(g, f, args) }}
	list.Flags().BoolVar(&f.showFallbk, "show-fallback", false, "with profiles: describe the fallback mapping")

	resolve := &cobra.Command{Use: "resolve <stub>", Short: "Print a stub with its extends chain applied", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return resolveCmd(g, args[0]) }}

	status := &cobra.Command{Use: "status", Short: "Show active runs, devices and ports (global, all projects)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return statusCmd(cmd.Context(), g, f) }}
	status.Flags().BoolVar(&f.jsonOut, "json", false, "machine-readable output")
	status.Flags().BoolVar(&f.quietStatus, "quiet", false, "no output; exit 0 idle, 10 active with a free device, 11 active and nothing free")

	sweep := &cobra.Command{Use: "sweep", Short: "Reclaim locks left by crashed runs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return sweepCmd(cmd.Context(), g, f) }}
	sweep.Flags().BoolVar(&f.dryRun, "dry-run", false, "report what would be reclaimed")

	lastRun := &cobra.Command{Use: "last-run", Short: "Show the result of the most recent run", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return lastRunCmd(g) }}

	recordAPI := &cobra.Command{Use: "record-api", Short: "Record API traffic with native WireMock recording (default: /api/ paths)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return recordAPICmd(cmd.Context(), g, f) }}
	recordAPI.Flags().StringVar(&f.target, "target", "", "upstream (default: the project's proxyBaseUrl)")
	recordAPI.Flags().StringVar(&f.output, "output", "", "directory name below .maestro/.runtime/wiremock/recordings")
	recordAPI.Flags().StringVar(&f.recordPaths, "url-path-pattern", "/api/.*", "WireMock URL-path regex to record (use '/.*' for all paths)")
	recordAPI.Flags().IntVar(&f.serverPort, "server-port", project.ReservedMockPort, "port to listen on")

	playback := &cobra.Command{Use: "playback <directory>", Short: "Serve a native WireMock recording", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return playbackCmd(cmd.Context(), g, f, args[0]) }}
	playback.Flags().IntVar(&f.serverPort, "server-port", project.ReservedMockPort, "port to listen on")

	boot := &cobra.Command{Use: "boot [avd|simulator]", Short: "Boot a designated emulator/simulator (lists them without an argument)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return bootCmd(cmd.Context(), g, args, f.headless) }}
	boot.Flags().BoolVar(&f.headless, "headless", false, "boot without a window")

	var proxyDevice string
	var proxyClear bool
	proxy := &cobra.Command{Use: "proxy", Short: "Show (or --clear) device-wide HTTP proxies on attached Android devices", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return proxyCmd(cmd.Context(), g, proxyDevice, proxyClear)
		}}
	proxy.Flags().StringVar(&proxyDevice, "device", "", "only this serial")
	proxy.Flags().BoolVar(&proxyClear, "clear", false, "remove the proxy settings and verify they are gone")

	var keep int
	prune := &cobra.Command{Use: "prune", Short: "Delete old run dirs of this project and strip APK snapshots from the rest", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return pruneCmd(cmd.Context(), g, keep) }}
	prune.Flags().IntVar(&keep, "keep", engine.KeepRuns(), "finished run dirs to keep")

	tui := &cobra.Command{Use: "tui", Short: "Open the interactive terminal UI", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runTUI(g, f) }}

	root.AddCommand(initCommand(g, version), skillCommand(g), run, mock, check, list, resolve, status, sweep, lastRun, recordAPI, playback, boot, prune, proxy, tui, uiCommand(g, f))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	go func() {
		<-ctx.Done()
		// A second signal while cleaning up: give up immediately.
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		fmt.Fprintln(os.Stderr, "\nForced exit; run `mockstro sweep` if locks were left behind.")
		os.Exit(130)
	}()
	err := root.ExecuteContext(ctx)
	var ee ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func addRunFlags(c *cobra.Command, f *runFlags) {
	fl := c.Flags()
	fl.StringArrayVar(&f.envFiles, "env-file", nil, "YAML test-variable file (repeatable; later files win)")
	fl.StringArrayVarP(&f.variables, "env", "e", nil, "Maestro KEY=VALUE input (repeatable; overrides files)")
	fl.BoolVar(&f.rerunFailed, "rerun-failed", false, "rerun only failed flow/platform pairs from the last run")
	fl.StringVar(&f.tags, "tags", "smoke", "comma-separated tags")
	fl.StringVar(&f.device, "device", "", "emulator serial / simulator UDID")
	fl.IntVar(&f.serverPort, "server-port", project.ReservedMockPort, "pin the WireMock port")
	fl.StringVar(&f.profile, "profile", "", "override the profile each flow declares")
	fl.StringArrayVar(&f.fbrc, "fbrc", nil, "Remote Config override key=value (repeatable; wins over the flow's)")
	fl.StringVar(&f.parallel, "parallel", "", "workers per full platform: n (boots devices if needed), or 'auto' (already free devices)")
	fl.StringVar(&f.boot, "boot-emulator", "", "boot a designated AVD/simulator when none is free (optionally named)")
	fl.Lookup("boot-emulator").NoOptDefVal = bootAny
	fl.StringVar(&f.devices, "devices", "", "restrict device class: emulator, physical or any")
	fl.BoolVar(&f.mapRemote, "map-remote", false, "Android: any device via your Proxyman/Charles Map Remote rules (port 18080, no override)")
	fl.BoolVar(&f.skipBuild, "skip-build", false, "do not build or install; test the build already on the device")
	fl.BoolVar(&f.failFast, "fail-fast", false, "stop after the first failed flow")
	fl.BoolVar(&f.isolate, "isolate", false, "run each flow in its own Maestro process (slower; for debugging)")
	fl.BoolVar(&f.record, "record", false, "record videos instead of running tests")
	fl.StringVar(&f.recordDir, "record-output-dir", "", "video directory (default: <run dir>/recordings)")
	fl.BoolVar(&f.optimize, "optimize-recordings", false, "re-encode videos with HandBrakeCLI afterwards")
	fl.StringVar(&f.junit, "junit", "", "also write a JUnit XML report to this path")
	fl.BoolVar(&f.quiet, "quiet-output", false, "hide raw Maestro/Gradle output")
	fl.BoolVar(&f.headless, "headless", false, "run emulators and simulators without a window (less host CPU/GPU per device)")
	fl.IntVar(&f.emuCores, "emulator-cores", 0, "vCPUs for emulators mockstro boots (default: the AVD's)")
	fl.IntVar(&f.emuMemory, "emulator-memory", 0, "RAM in MB for emulators mockstro boots (default: the AVD's)")
	fl.StringVar(&f.emuSnapshot, "emulator-snapshot", "", "boot emulators from this named snapshot (e.g. saved with `adb emu avd snapshot save <name>`)")
	fl.StringVar(&f.emuGPU, "emulator-gpu", "", "emulator -gpu mode for emulators mockstro boots (default: host when --headless, else the AVD's)")
	fl.StringVar(&f.keyboard, "keyboard", "", "on-screen keyboard before flows: hidden or shown (default: leave as is)")
	fl.BoolVar(&f.clearProxy, "clear-proxy", false, "remove a device-wide HTTP proxy (Proxyman/Charles) instead of refusing the device")
}

func loadProject(g *globals) (*project.Project, error) {
	dir := g.projectDir
	if dir == "" {
		dir = "."
	}
	root, err := project.Find(dir)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			return nil, projectInitHint(g.projectDir, err)
		}
		return nil, err
	}
	p, err := project.Load(root)
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(err, os.ErrNotExist) && pathErr.Path == filepath.Join(root, ".maestro", "mockstro.yaml") {
		return nil, projectInitHint(root, err)
	}
	if p != nil {
		for _, w := range p.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
	}
	return p, err
}

func projectInitHint(projectDir string, err error) error {
	command := shellQuote(filepath.Base(os.Args[0]))
	if projectDir != "" {
		command += " --project " + shellQuote(projectDir)
	}
	return fmt.Errorf("%w\n\nTo set up a new suite, see: %s init --help\nProvide --android-app-id and/or --ios-app-id from your app's repository root.", err, command)
}

func buildOptions(cmd string, g *globals, f *runFlags, p *project.Project, args []string) (engine.Options, error) {
	o := engine.DefaultOptions()
	o.Platform = g.platform
	selected, err := p.Selected(g.platform)
	if err != nil {
		return o, err
	}
	o.Lite = selected[0].Lite()
	if cmd != "run" && o.Platform == "" {
		o.Platform = selected[0].Platform
	}
	if cmd == "mock" && f.attach && len(selected) > 1 {
		return o, fmt.Errorf("mock --attach requires --platform when multiple platforms are configured")
	}
	o.EnvFiles = append([]string(nil), f.envFiles...)
	o.Variables = project.Variables{}
	for _, entry := range f.variables {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return o, fmt.Errorf("--env requires KEY=VALUE")
		}
		if err := project.ValidateVariable(key); err != nil {
			return o, err
		}
		o.Variables[key] = value
	}
	o.Owner = g.owner
	o.Tags = splitTags(f.tags)
	o.FlowArgs = args
	o.DeviceArg = f.device
	o.ServerPort = f.serverPort
	o.PortPinned = f.serverPort != project.ReservedMockPort
	o.ProfileArg = f.profile
	for _, kv := range f.fbrc {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return o, fmt.Errorf("--fbrc requires a non-empty key in key=value")
		}
		o.FBRC[k] = v
	}
	o.Parallel = f.parallel
	switch f.boot {
	case "":
	case bootAny:
		o.BootRequest = true
	default:
		o.BootAVD = f.boot
	}
	o.DeviceClass = f.devices
	o.MapRemote = f.mapRemote
	o.SkipBuild = f.skipBuild
	o.FailFast = f.failFast
	o.Isolate = f.isolate
	o.Record = f.record
	o.RecordDir = f.recordDir
	o.Optimize = f.optimize
	o.Attach = f.attach
	o.JUnitPath = f.junit
	o.ClearProxy = f.clearProxy
	o.Keyboard = f.keyboard
	o.Headless = f.headless
	o.EmuCores, o.EmuMemoryMB, o.EmuGPU = f.emuCores, f.emuMemory, f.emuGPU
	o.EmuSnapshot = f.emuSnapshot
	switch o.DeviceClass {
	case "", "emulator", "physical", "any":
	default:
		return o, fmt.Errorf("--devices requires emulator, physical or any")
	}
	switch o.Owner {
	case "", "human", "agent", "ci":
	default:
		return o, fmt.Errorf("--owner requires human, agent or ci")
	}
	if cmd == "run" {
		return o, nil
	}
	return o, o.Validate(cmd)
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func newEngine(p *project.Project, o engine.Options, printer *Printer) *engine.Engine {
	e := engine.New(p, o, execx.Real{}, printer.Handle)
	if engine.ResolveOwner(o.Owner) == "human" {
		e.ChooseAVD = chooseAVDOnTTY
	}
	return e
}

func runCmd(ctx context.Context, g *globals, f *runFlags, args []string) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o, err := buildOptions("run", g, f, p, args)
	if err != nil {
		return err
	}
	pr := NewPrinter()
	pr.Parallel = o.Parallel != ""
	pr.Quiet = f.quiet
	if f.rerunFailed {
		if len(args) > 0 {
			return fmt.Errorf("--rerun-failed cannot be combined with flow paths")
		}
		_, _, results, err := engine.LoadResults(p.LastRunPath())
		if err != nil {
			return err
		}
		o = engine.FailedPairs(o, results, p.Root)
	}
	s := engine.RunSuite(ctx, p, o, execx.Real{}, pr.Handle)
	if len(s.Results) > 0 {
		if o.Parallel != "" {
			fmt.Println("\n===== Parallel session summary =====")
		}
		PrintSummary(os.Stdout, s)
		PrintFailedReruns(os.Stdout, p, s)
		PrintRecordings(os.Stdout, p, s)
		if s.Dir != "" {
			fmt.Printf("Run dir: %s\n", p.Rel(s.Dir))
		}
	}
	switch {
	case s.Err != nil:
		return s.Err
	case s.Cancelled || ctx.Err() != nil:
		return ExitError{130}
	case s.Failed > 0:
		return ExitError{1}
	}
	return nil
}

func checkCmd(ctx context.Context, g *globals, install bool) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	targets, err := p.Selected(g.platform)
	if err != nil {
		return err
	}
	printer := NewPrinter()
	e := newEngine(targets[0], engine.DefaultOptions(), printer)
	if err := e.CheckEnvironment("check"); err != nil {
		return err
	}
	counts, err := e.Store.ValidateAll(true)
	if err != nil {
		return err
	}
	fmt.Println(counts)
	confirm := func() bool {
		if install {
			return true
		}
		if !isTerminal(os.Stdin) {
			return false
		}
		answer := prompt(os.Stdout, bufio.NewReader(os.Stdin), "Maestro is not installed. Install it now? [Y/n] ")
		return answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
	}
	if err := e.EnsureMaestro(ctx, confirm); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, target := range targets {
		child := newEngine(target, engine.DefaultOptions(), printer)
		if target != targets[0] {
			if _, err := child.Store.ValidateAll(true); err != nil {
				return err
			}
		}
		if seen[target.WireMockVersion] {
			continue
		}
		jar, err := child.EnsureJar(ctx)
		if err != nil {
			return err
		}
		seen[target.WireMockVersion] = true
		fmt.Printf("WireMock is ready at %s (SHA-256 verified).\n", jar)
	}
	return nil
}

func statusCmd(ctx context.Context, g *globals, f *runFlags) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o := engine.DefaultOptions()
	o.Owner = g.owner
	o.Platform = g.platform
	st, err := engine.SuiteStatus(ctx, p, o, execx.Real{})
	if err != nil {
		return err
	}
	switch {
	case f.jsonOut:
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(b))
	case !f.quietStatus:
		printStatus(st)
	}
	if st.ActiveCount == 0 {
		return nil
	}
	if len(st.Available.Eligible) == 0 {
		return ExitError{11}
	}
	return ExitError{10}
}

func printStatus(st *engine.Status) {
	if st.ActiveCount == 0 {
		fmt.Println("No Maestro run is active.")
	} else {
		fmt.Printf("%-30s %-6s %-7s %-16s %-6s %-10s %s\n", "RUN", "OWNER", "PID", "DEVICE", "PORT", "STAGE", "FLOW")
		for _, r := range st.Runs {
			fmt.Printf("%-30s %-6s %-7d %-16s %-6d %-10s %s\n", r.RunID, d(r.Owner), r.PID, d(r.Device), r.Port, d(r.Stage), d(r.Flow))
			if r.Worktree != "" {
				fmt.Printf("  %s\n", r.Worktree)
			}
		}
	}
	for _, dv := range st.Devices {
		if dv.Proxy != "" {
			fmt.Printf("\nWARNING: %s has a device-wide proxy: %s (mockstro proxy --clear --device %s)", dv.Serial, dv.Proxy, dv.Serial)
		}
	}
	fmt.Printf("\nFree: emulators=[%s] physical=[%s]\n", strings.Join(st.Available.Emulator, ","), strings.Join(st.Available.Physical, ","))
	fmt.Printf("Eligible for this owner (%s): [%s]\n", st.Owner, strings.Join(st.Available.Eligible, ","))
	if len(st.Bootable) > 0 {
		fmt.Printf("Bootable: [%s]\n", strings.Join(st.Bootable, ","))
	}
}

func d(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sweepCmd(ctx context.Context, g *globals, f *runFlags) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	e := newEngine(p, engine.DefaultOptions(), NewPrinter())
	suffix := ""
	if f.dryRun {
		suffix = " (dry run)"
	}
	fmt.Printf("Sweeping stale Maestro runs%s\n", suffix)
	actions := e.Sweep(ctx, f.dryRun)
	for _, a := range actions {
		run := a.RunID
		if run == "" {
			run = "unknown"
		}
		fmt.Printf("  %s (run %s): %s\n", a.Name, run, a.Note)
	}
	if len(actions) == 0 {
		fmt.Println("  no safely reclaimable locks found")
	}
	fmt.Println("Done.")
	return nil
}

func lastRunCmd(g *globals) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	at, total, results, err := engine.LoadResults(p.LastRunPath())
	if err != nil {
		return fmt.Errorf("no saved Maestro run found. Run `mockstro run` first")
	}
	if len(results) == 0 {
		return fmt.Errorf("last Maestro run contains no flow results")
	}
	fmt.Printf("Last run: %s\n", at.Format("2006-01-02T15:04:05Z"))
	s := engine.NewSummary(results, total, "")
	PrintSummary(os.Stdout, s)
	PrintFailedReruns(os.Stdout, p, s)
	PrintRecordings(os.Stdout, p, s)
	return nil
}

func mockCmd(ctx context.Context, g *globals, f *runFlags, args []string) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o, err := buildOptions("mock", g, f, p, nil)
	if err != nil {
		return err
	}
	e := newEngine(p, o, NewPrinter())
	if err := e.CheckEnvironment("mock"); err != nil {
		return err
	}
	counts, err := e.Store.ValidateAll(false)
	if err != nil {
		return err
	}
	fmt.Println(counts)
	t, err := e.ResolveMockTarget(args)
	if err != nil {
		return err
	}
	m, err := e.StartMock(ctx, t)
	if err != nil {
		return err
	}
	defer m.Stop()
	fmt.Println(e.MockBanner(m))
	fmt.Println("Press Ctrl-C to stop WireMock.")
	return m.Serve(ctx)
}

func recordAPICmd(ctx context.Context, g *globals, f *runFlags) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o := engine.DefaultOptions()
	o.ServerPort = f.serverPort
	e := newEngine(p, o, NewPrinter())
	if err := e.CheckEnvironment("record-api"); err != nil {
		return err
	}
	target := f.target
	if target == "" {
		target = p.ProxyBaseURL
	}
	return e.RecordAPI(ctx, target, f.output, f.recordPaths)
}

func playbackCmd(ctx context.Context, g *globals, f *runFlags, dir string) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o := engine.DefaultOptions()
	o.ServerPort = f.serverPort
	e := newEngine(p, o, NewPrinter())
	if err := e.CheckEnvironment("playback"); err != nil {
		return err
	}
	return e.Playback(ctx, dir)
}

func bootCmd(ctx context.Context, g *globals, args []string, headless bool) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o := engine.DefaultOptions()
	o.Owner = g.owner
	o.Headless = headless
	if g.platform != "" {
		o.Platform = g.platform
	}
	e := newEngine(p, o, NewPrinter())
	candidates := e.BootCandidates(ctx)
	if len(args) == 0 {
		if len(candidates) == 0 {
			return fmt.Errorf("nothing to boot: no designated AVD/simulator is installed")
		}
		fmt.Println("Bootable:")
		for _, c := range candidates {
			fmt.Println("  " + c)
		}
		return nil
	}
	if !slices.Contains(candidates, args[0]) {
		return fmt.Errorf("'%s' is not bootable for owner %s (candidates: %s)", args[0], e.Owner, strings.Join(candidates, ", "))
	}
	serial, err := e.Plat.Boot(ctx, args[0], o.Headless, func(s string) { fmt.Println(s) })
	if err != nil {
		return err
	}
	fmt.Println(serial)
	return nil
}

func runTUI(g *globals, f *runFlags) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o, err := buildOptions("run", g, f, p, nil)
	if err != nil {
		return err
	}
	o.Owner = "human"
	if g.owner != "" {
		o.Owner = g.owner
	}
	return TUIFunc(p, o)
}

// chooseAVDOnTTY asks on the controlling terminal; never used unattended.
func chooseAVDOnTTY(avds []string) (string, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", false
	}
	defer tty.Close()
	fmt.Fprintln(tty, "\nNo free emulator is available. Bootable on this machine:")
	for i, a := range avds {
		fmt.Fprintf(tty, "  %d) %s\n", i+1, a)
	}
	fmt.Fprintf(tty, "Boot one? [1-%d, or n to cancel]: ", len(avds))
	line, _ := bufio.NewReader(tty).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(avds) {
		return "", false
	}
	return avds[n-1], true
}

func pruneCmd(ctx context.Context, g *globals, keep int) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	e := newEngine(p, engine.DefaultOptions(), NewPrinter())
	before := engine.DirSize(p.RunsRoot())
	removed, freed := e.Prune(ctx, keep)
	fmt.Printf("Removed %d run dir(s), freed %s; %s left in %s\n", removed, engine.HumanSize(freed), engine.HumanSize(before-freed), p.Rel(p.RunsRoot()))
	return nil
}

func proxyCmd(ctx context.Context, g *globals, device string, clear bool) error {
	p, err := loadProject(g)
	if err != nil {
		return err
	}
	o := engine.DefaultOptions()
	o.Owner = g.owner
	e := newEngine(p, o, NewPrinter())
	devs, err := e.Plat.Devices(ctx)
	if err != nil {
		return err
	}
	found, failed := false, false
	for _, d := range devs {
		if device != "" && d.Serial != device {
			continue
		}
		found = true
		cur := e.Plat.DeviceProxy(ctx, d.Serial)
		switch {
		case cur == "":
			fmt.Printf("%-16s no proxy\n", d.Serial)
		case !clear:
			fmt.Printf("%-16s PROXY %s\n", d.Serial, cur)
		default:
			if e.DeviceLocked(d.Serial) {
				fmt.Printf("%-16s PROXY %s (left alone: a run holds this device)\n", d.Serial, cur)
				failed = true
				continue
			}
			if err := e.Plat.ClearProxy(ctx, d.Serial); err != nil {
				fmt.Printf("%-16s %v\n", d.Serial, err)
				failed = true
				continue
			}
			fmt.Printf("%-16s cleared %s (verified)\n", d.Serial, cur)
		}
	}
	if device != "" && !found {
		return fmt.Errorf("device %s is not attached", device)
	}
	if failed {
		return ExitError{1}
	}
	return nil
}
