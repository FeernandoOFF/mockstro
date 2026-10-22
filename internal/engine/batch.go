package engine

import (
	"context"
	"path/filepath"

	"github.com/feernandooff/mockstro/internal/fixtures"
	"github.com/feernandooff/mockstro/internal/maestro"
)

func (r *Run) maestro() *maestro.Executor {
	return &maestro.Executor{Exec: r.e.Exec, Device: r.Device, Workspace: maestro.Workspace{
		Dir: r.Dir, FlowsRoot: r.flowsRoot, SharedRoot: r.e.P.SharedRoot(), ConfigPath: r.configPath,
	}, Output: func(flow, line string) {
		r.e.emit(Event{Kind: EvOutput, Worker: r.Worker, RunID: r.ID, Flow: flow, Text: line})
	},
		Log: func(line string) { r.logf("%s", line) }}
}

// RunBatch supplies mock activation and result verification to the Maestro executor.
func (r *Run) RunBatch(ctx context.Context, flows []*fixtures.Flow, stopped func() bool, done func(int, *Result)) bool {
	requests := make([]maestro.Flow, len(flows))
	active := make([]*flowRun, len(flows))
	for i, f := range flows {
		requests[i] = maestro.Flow{Source: f, Variables: r.e.flowVariables(f, r.Port), LogPath: filepath.Join(r.Dir, "logs", FlowSlug(f.Feature, f.File)+".log")}
	}
	return r.maestro().Batch(ctx, requests, maestro.Hooks{
		Stopped: stopped,
		Initialize: func(i int) {
			fr := r.beginFlow(flows[i])
			fr.held = true
			active[i] = fr
		},
		Start: func(i int) error {
			fr := active[i]
			r.logf("Running %s/%s [%s]", fr.f.Feature, fr.f.File, fr.profile)
			return r.activateFlow(ctx, fr)
		},
		Complete: func(i int, out maestro.Outcome) bool {
			fr := active[i]
			if out.Err != nil {
				fr.res.Message = out.Err.Error()
			}
			switch {
			case out.Cancelled:
				r.finishFlow(fr, "CANCELLED", out.Exit)
			case out.Setup:
				r.finishFlow(fr, "SETUP", out.Exit)
			case out.Unstarted:
				r.finishFlow(fr, "TEST", out.Exit)
			default:
				r.concludeFlow(ctx, fr, "TEST", out.Exit)
			}
			if !fr.res.Passed() && r.e.Opts.FailFast && !out.Cancelled {
				r.logf("Stopping after %s/%s failed.", fr.f.Feature, fr.f.File)
			}
			return fr.res.Passed()
		},
		Publish: func(i int, a maestro.Artifacts) {
			fr := active[i]
			fr.res.Artifacts, fr.res.Shots = a.Dir, a.Shots
			r.publishFlow(fr)
			done(i, fr.res)
		},
	}, r.e.Opts.FailFast)
}
