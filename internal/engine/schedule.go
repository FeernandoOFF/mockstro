package engine

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/feernandooff/mockstro/internal/fixtures"
)

// historySamples caps how many past durations per flow feed its estimate.
const historySamples = 5

// FlowEstimates returns each flow's median duration over its most recent runs, keyed
// feature/file. Passed runs are preferred: failures often stop early or hit a timeout.
func (e *Engine) FlowEstimates(ctx context.Context) map[string]time.Duration {
	passed, any := map[string][]time.Duration{}, map[string][]time.Duration{}
	for _, rd := range e.RunDirs(ctx, false) {
		_, _, results, err := LoadResults(filepath.Join(rd.Path, "result.tsv"))
		if err != nil {
			continue
		}
		for _, r := range results {
			if r.Duration <= 0 || r.Stage == "UNSTARTED" || r.Stage == "INTERRUPTED" || r.Stage == "CANCELLED" {
				continue
			}
			k := r.Feature + "/" + r.File
			if r.Passed() && len(passed[k]) < historySamples {
				passed[k] = append(passed[k], r.Duration)
			}
			if len(any[k]) < historySamples {
				any[k] = append(any[k], r.Duration)
			}
		}
	}
	est := map[string]time.Duration{}
	for k, ds := range any {
		if p := passed[k]; len(p) > 0 {
			ds = p
		}
		est[k] = median(ds)
	}
	return est
}

// longestFirst orders queue claims by descending estimate (LPT), so a long flow never starts
// last while the other workers sit idle. Flows without history get the median estimate; with
// no history at all the original order is kept.
func longestFirst(flows []*fixtures.Flow, est map[string]time.Duration) (order []int, known int) {
	order = make([]int, len(flows))
	var ds []time.Duration
	for i, f := range flows {
		order[i] = i
		if d, ok := est[f.Feature+"/"+f.File]; ok {
			ds = append(ds, d)
			known++
		}
	}
	if known == 0 {
		return order, 0
	}
	fallback := median(ds)
	of := func(i int) time.Duration {
		if d, ok := est[flows[i].Feature+"/"+flows[i].File]; ok {
			return d
		}
		return fallback
	}
	sort.SliceStable(order, func(a, b int) bool { return of(order[a]) > of(order[b]) })
	return order, known
}

func median(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}
