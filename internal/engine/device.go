package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/feernandooff/trun/internal/lock"
	"github.com/feernandooff/trun/internal/platform"
	"github.com/feernandooff/trun/internal/project"
	"github.com/feernandooff/trun/internal/wiremock"
)

// Device rules:
//   - agents and CI may only take an emulator/simulator that is explicitly designated
//     (MAESTRO_AGENT_AVDS, .maestro/agent-avds.txt, IOS_SIMULATOR_NAMES or ~/.config/trun/designated.txt);
//   - a human may take any emulator, designated ones first;
//   - a physical device is only driven with --map-remote.

func (e *Engine) isDesignated(ctx context.Context, serial string) bool {
	name := e.Plat.DeviceName(ctx, serial)
	if name == "" {
		return false
	}
	return slices.Contains(e.Plat.Designated(), name)
}

// Allowed reports whether this owner may drive serial at all.
func (e *Engine) Allowed(ctx context.Context, serial string) bool {
	if e.Opts.MapRemote {
		return true
	}
	if e.Plat.Class(serial) != "emulator" {
		return false
	}
	if e.Owner == "human" {
		return true
	}
	return e.isDesignated(ctx, serial)
}

func (e *Engine) wantedClass() string {
	if e.Opts.DeviceClass != "" {
		return e.Opts.DeviceClass
	}
	if v := os.Getenv("MAESTRO_DEVICE_CLASS"); v != "" {
		return v
	}
	if e.Owner == "human" {
		return "any"
	}
	return "emulator"
}

func (e *Engine) requireDesignation() error {
	if e.Opts.MapRemote || e.Owner == "human" || len(e.Plat.Designated()) > 0 {
		return nil
	}
	if e.Opts.Platform == "ios" {
		return errors.New("no simulators designated. Set IOS_SIMULATOR_NAMES in .maestro/project.env or list names in ~/.config/trun/designated.txt")
	}
	return errors.New("no reusable agent AVDs configured. Set MAESTRO_AGENT_AVDS=name[,name], create .maestro/agent-avds.txt, or list names in ~/.config/trun/designated.txt")
}

func (e *Engine) DeviceLocked(serial string) bool {
	return lock.Held(e.LockRoots(), "device", serial)
}

// candidates orders eligible devices: designated first, so a human run does not grab a
// personal emulator while a designated one is free.
func (e *Engine) candidates(ctx context.Context, devs []platform.Device) []string {
	wanted := e.wantedClass()
	var first, rest []string
	for _, d := range devs {
		if wanted != "any" && d.Class != wanted {
			continue
		}
		if !e.Allowed(ctx, d.Serial) {
			continue
		}
		designated := e.isDesignated(ctx, d.Serial)
		// A shut-down simulator is only picked (and booted) when designated; otherwise a
		// human run could boot an arbitrary iPad.
		if e.Opts.Platform == "ios" && d.State != "Booted" && !designated {
			continue
		}
		if designated {
			first = append(first, d.Serial)
		} else {
			rest = append(rest, d.Serial)
		}
	}
	return append(first, rest...)
}

// BootCandidates: designated installed AVDs/simulators, plus (for humans) every other one.
func (e *Engine) BootCandidates(ctx context.Context) []string {
	candidates := e.Plat.Bootable(ctx)
	if e.Owner == "human" {
		switch p := e.Plat.(type) {
		case *platform.Android:
			candidates = append(candidates, p.InstalledAVDs(ctx)...)
		case *platform.IOS:
			devs, _ := p.Devices(ctx)
			for _, d := range devs {
				candidates = append(candidates, d.Name)
			}
		}
	}
	// Already running: nothing to boot (the emulator refuses a second instance anyway).
	running := e.runningNames(ctx)
	var out []string
	for _, n := range candidates {
		if !slices.Contains(out, n) && !slices.Contains(running, n) {
			out = append(out, n)
		}
	}
	return out
}

// offerBoot boots something when no eligible device is free. Unattended runs must pass
// --boot-emulator so they can never hang waiting for an answer.
func (e *Engine) offerBoot(ctx context.Context) bool {
	avds := e.BootCandidates(ctx)
	if len(avds) == 0 {
		e.Warnf("No installed AVD is designated for Maestro. Configure MAESTRO_AGENT_AVDS or .maestro/agent-avds.txt.")
		return false
	}
	pick := ""
	switch {
	case e.Opts.BootAVD != "":
		if !slices.Contains(avds, e.Opts.BootAVD) {
			e.Warnf("AVD '%s' is not designated (MAESTRO_AGENT_AVDS, .maestro/agent-avds.txt, ~/.config/trun/designated.txt).", e.Opts.BootAVD)
			return false
		}
		pick = e.Opts.BootAVD
	case e.Opts.BootRequest:
		pick = avds[0]
	case e.ChooseAVD != nil:
		var ok bool
		if pick, ok = e.ChooseAVD(avds); !ok {
			return false
		}
	default:
		e.Warnf("No free emulator. Available AVDs: %s", strings.Join(avds, " "))
		return false
	}
	if _, err := e.Plat.Boot(ctx, pick, func(s string) { e.Logf("%s", s) }); err != nil {
		e.Warnf("%v", err)
		return false
	}
	return true
}

func (e *Engine) noDeviceHint() string {
	h := "  Boot a designated emulator: trun run --boot-emulator [avd] ..."
	if e.Opts.Platform != "ios" {
		h += fmt.Sprintf("\n  Or run on a connected phone via your Proxyman Map Remote rule (port %d):\n    trun run --map-remote [--device <serial>] ...", project.ReservedMockPort)
	}
	return h
}

// SelectDevice picks and locks a device for this run.
func (e *Engine) SelectDevice(ctx context.Context, locks *lock.Manager) (string, error) {
	if err := e.requireDesignation(); err != nil {
		return "", err
	}
	devs, err := e.Plat.Devices(ctx)
	if err != nil {
		return "", err
	}
	if e.Opts.DeviceArg != "" {
		for _, d := range devs {
			if d.Serial != e.Opts.DeviceArg {
				continue
			}
			if !e.Allowed(ctx, d.Serial) {
				return "", fmt.Errorf("device '%s' is not an allowlisted reusable AVD for owner %s (physical devices need --map-remote)", d.Serial, e.Owner)
			}
			if err := locks.Acquire("device", d.Serial); err != nil {
				return "", fmt.Errorf("device '%s' is already owned; run status/sweep", d.Serial)
			}
			return d.Serial, nil
		}
		return "", fmt.Errorf("device '%s' is not connected and ready", e.Opts.DeviceArg)
	}

	if len(devs) == 0 {
		if e.offerBoot(ctx) {
			devs, _ = e.Plat.Devices(ctx)
		}
		if len(devs) == 0 {
			return "", fmt.Errorf("no ready devices found.\n%s", e.noDeviceHint())
		}
	}
	for _, s := range e.candidates(ctx, devs) {
		if locks.Acquire("device", s) == nil {
			return s, nil
		}
	}
	if e.wantedClass() != "physical" && e.offerBoot(ctx) {
		devs, _ = e.Plat.Devices(ctx)
		for _, s := range e.candidates(ctx, devs) {
			if locks.Acquire("device", s) == nil {
				return s, nil
			}
		}
	}
	if e.Opts.MapRemote {
		return "", errors.New("--map-remote: no free device. See: trun status")
	}
	return "", fmt.Errorf("no %s device eligible for owner %s is free. See: trun status\n%s", e.wantedClass(), e.Owner, e.noDeviceHint())
}

// FreeEligible lists devices this owner may take that no run holds.
func (e *Engine) FreeEligible(ctx context.Context) []string {
	devs, err := e.Plat.Devices(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range e.candidates(ctx, devs) {
		if !e.DeviceLocked(s) {
			out = append(out, s)
		}
	}
	return out
}

// runningDesignated: designated names that already have a device attached (never boot twice).
func (e *Engine) runningNames(ctx context.Context) []string {
	devs, _ := e.Plat.Devices(ctx)
	var out []string
	for _, d := range devs {
		if e.Opts.Platform == "ios" && d.State != "Booted" {
			continue
		}
		if n := e.Plat.DeviceName(ctx, d.Serial); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// EnsureParallelDevices provides `wanted` free devices before any worker starts.
func (e *Engine) EnsureParallelDevices(ctx context.Context, wanted int) error {
	free := len(e.FreeEligible(ctx))
	if free >= wanted {
		return nil
	}
	running := e.runningNames(ctx)
	var toBoot []string
	for _, n := range e.BootCandidates(ctx) {
		if !slices.Contains(running, n) {
			toBoot = append(toBoot, n)
		}
	}
	if free+len(toBoot) < wanted {
		return fmt.Errorf("--parallel %d needs %d devices: %d free now and %d more designated AVD(s) to boot.\n  Designate another AVD in MAESTRO_AGENT_AVDS, .maestro/agent-avds.txt or ~/.config/trun/designated.txt, or lower --parallel", wanted, wanted, free, len(toBoot))
	}
	e.Logf("Need %d more device(s) for %d worker(s); %d free now.", wanted-free, wanted, free)
	booted := 0
	for _, n := range toBoot {
		if free+booted >= wanted {
			break
		}
		if _, err := e.Plat.Boot(ctx, n, func(s string) { e.Logf("%s", s) }); err != nil {
			return fmt.Errorf("could not boot designated device '%s' for --parallel %d: %w", n, wanted, err)
		}
		booted++
	}
	e.Logf("Booted %d device(s).", booted)
	if len(e.FreeEligible(ctx)) < wanted {
		return fmt.Errorf("booted %d device(s) but fewer than %d are free; another run may have taken one", booted, wanted)
	}
	return nil
}

// AcquirePort locks a port. An emulator/simulator run never takes the reserved mock port
// unless pinned, so it cannot push a later `mock` off the port its Map Remote rule names.
func (e *Engine) AcquirePort(ctx context.Context, locks *lock.Manager, command, serial string) (int, error) {
	port := e.Opts.ServerPort
	if e.Opts.PortPinned {
		if err := locks.Acquire("port", fmt.Sprint(port)); err != nil {
			return 0, fmt.Errorf("port %d is locked by another Maestro run. See: trun status", port)
		}
		return port, nil
	}
	base := port
	if command == "run" && (e.Opts.Platform == "ios" || e.isEmulator(ctx, serial)) {
		base = project.EmulatorPortBase
		if base != port {
			e.Logf("Emulator run: leaving port %d free for mock; scanning from %d.", project.ReservedMockPort, base)
		}
	}
	for c := base; c < base+20; c++ {
		if c == project.ReservedMockPort && base != project.ReservedMockPort {
			continue
		}
		if wiremock.PortListener(ctx, e.Exec, c) != "" {
			continue
		}
		if locks.Acquire("port", fmt.Sprint(c)) == nil {
			if c != base {
				e.Logf("Port %d busy; using %d instead.", base, c)
			}
			return c, nil
		}
	}
	return 0, fmt.Errorf("no free port in %d-%d. See: trun status", base, base+19)
}

func (e *Engine) isEmulator(ctx context.Context, serial string) bool {
	if serial == "" {
		return false
	}
	if a, ok := e.Plat.(*platform.Android); ok {
		return a.IsEmulator(ctx, serial)
	}
	return true
}
