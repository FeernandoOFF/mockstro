package engine

import (
	"context"
	"strings"
	"time"

	"github.com/feernandooff/mockstro/internal/execx"
	"github.com/feernandooff/mockstro/internal/project"

	"github.com/feernandooff/mockstro/internal/lock"
	"github.com/feernandooff/mockstro/internal/paths"
	"github.com/feernandooff/mockstro/internal/platform"
	"github.com/feernandooff/mockstro/internal/registry"
)

type StatusDevice struct {
	Platform string `json:"platform"`
	platform.Device
	LockedBy   *string `json:"lockedBy"`
	Designated bool    `json:"designated"`
	Eligible   bool    `json:"eligible"`
	Proxy      string  `json:"proxy,omitempty"`    // device-wide HTTP proxy, "" when none
	Keyboard   string  `json:"keyboard,omitempty"` // shown | hidden
}

type Available struct {
	Emulator []string `json:"emulator"`
	Physical []string `json:"physical"`
	Eligible []string `json:"eligible"`
}

// Status is the `status --json` payload.
type Status struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	RepoRoot      string         `json:"repoRoot"`
	Platform      string         `json:"platform"`
	Owner         string         `json:"owner"`
	ActiveCount   int            `json:"activeCount"`
	Runs          []registry.Run `json:"runs"`
	Devices       []StatusDevice `json:"devices"`
	Available     Available      `json:"available"`
	Bootable      []string       `json:"bootable"`
}

func (e *Engine) Status(ctx context.Context) *Status {
	runs := registry.Active(ctx, e.Exec, paths.LocksRoot(), paths.RegistryRoot())
	if runs == nil {
		runs = []registry.Run{}
	}
	st := &Status{
		SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339), RepoRoot: e.P.Root,
		Platform: e.Opts.Platform, Owner: e.Owner, ActiveCount: len(runs), Runs: runs,
		Devices: []StatusDevice{}, Available: Available{Emulator: []string{}, Physical: []string{}, Eligible: []string{}},
	}
	busy := map[string]string{}
	for _, r := range runs {
		if r.Device != "" {
			busy[r.Device] = r.RunID
		}
	}
	tool := "adb"
	if e.Opts.Platform == "ios" {
		tool = "xcrun"
	}
	if _, err := e.Exec.LookPath(tool); err == nil {
		devs, _ := e.Plat.Devices(ctx)
		for _, d := range devs {
			if e.Opts.Platform == "ios" && d.State != "Booted" {
				continue
			}
			d.Name = e.Plat.DeviceName(ctx, d.Serial)
			sd := StatusDevice{Platform: e.Opts.Platform, Device: d, Designated: e.isDesignated(ctx, d.Serial), Proxy: e.Plat.DeviceProxy(ctx, d.Serial), Keyboard: e.Plat.Keyboard(ctx, d.Serial)}
			if id, ok := busy[d.Serial]; ok {
				sd.LockedBy = &id
			} else if e.DeviceLocked(d.Serial) {
				held := "unknown"
				sd.LockedBy = &held
			}
			sd.Eligible = sd.LockedBy == nil && e.Allowed(ctx, d.Serial)
			if sd.LockedBy == nil {
				if d.Class == "emulator" {
					st.Available.Emulator = append(st.Available.Emulator, d.Serial)
				} else {
					st.Available.Physical = append(st.Available.Physical, d.Serial)
				}
			}
			if sd.Eligible {
				st.Available.Eligible = append(st.Available.Eligible, d.Serial)
			}
			st.Devices = append(st.Devices, sd)
		}
		st.Bootable = e.BootCandidates(ctx)
	}
	if st.Bootable == nil {
		st.Bootable = []string{}
	}
	return st
}

func (e *Engine) Sweep(ctx context.Context, dryRun bool) []lock.SweepAction {
	return lock.Sweep(ctx, e.Exec, paths.LocksRoot(), dryRun)
}

// SuiteStatus shows devices for every selected target, without duplicating the global runs.
func SuiteStatus(ctx context.Context, p *project.Project, o Options, ex execx.Runner) (*Status, error) {
	targets, err := p.Selected(o.Platform)
	if err != nil {
		return nil, err
	}
	var out *Status
	var names []string
	for _, target := range targets {
		child := o
		child.Platform = target.Platform
		st := New(target, child, ex, nil).Status(ctx)
		names = append(names, target.Platform)
		if out == nil {
			copy := *st
			copy.Devices = nil
			copy.Bootable = nil
			copy.Available = Available{Emulator: []string{}, Physical: []string{}, Eligible: []string{}}
			out = &copy
		}
		out.Devices = append(out.Devices, st.Devices...)
		out.Available.Emulator = append(out.Available.Emulator, st.Available.Emulator...)
		out.Available.Physical = append(out.Available.Physical, st.Available.Physical...)
		out.Available.Eligible = append(out.Available.Eligible, st.Available.Eligible...)
		for _, name := range st.Bootable {
			if len(targets) > 1 {
				name = target.Platform + "/" + name
			}
			out.Bootable = append(out.Bootable, name)
		}
	}
	out.Platform = strings.Join(names, ",")
	return out, nil
}
