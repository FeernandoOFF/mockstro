// Package scheduler distributes indexed jobs across owned workers. It has no
// knowledge of devices, test tools, files, or result formats.
package scheduler

import (
	"context"
	"fmt"
	"sync"
)

type State uint8

const (
	Pending State = iota
	Claimed
	Completed
)

type Plan struct {
	Jobs     int
	Workers  int
	Order    []int
	Batched  bool
	FailFast bool
}

// Worker executes the supplied job indexes. done reports each completed job once.
// Execute returns true when the worker must retire without claiming more jobs.
// An omitted job is returned to Pending when Execute returns. Close releases all
// resources, including after cancellation or an execution failure.
type Worker interface {
	Execute(ctx context.Context, jobs []int, stopped func() bool, done func(index int, failed bool)) (retire bool)
	Close()
}

type WorkerError struct {
	Worker int
	Err    error
}

func (e WorkerError) Error() string { return fmt.Sprintf("worker %d: %v", e.Worker, e.Err) }

type Report struct {
	States []State
	Errors []WorkerError
}

type queue struct {
	mu     sync.Mutex
	plan   Plan
	next   int
	states []State
	stop   bool
}

func (p Plan) validate() error {
	if p.Jobs < 0 || p.Workers < 1 {
		return fmt.Errorf("scheduler requires non-negative jobs and at least one worker")
	}
	if p.Order == nil {
		return nil
	}
	if len(p.Order) != p.Jobs {
		return fmt.Errorf("scheduler order must include every job")
	}
	seen := make([]bool, p.Jobs)
	for _, i := range p.Order {
		if i < 0 || i >= p.Jobs || seen[i] {
			return fmt.Errorf("scheduler order must be a permutation of job indexes")
		}
		seen[i] = true
	}
	return nil
}

func (q *queue) claim(ctx context.Context) []int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stop || ctx.Err() != nil || q.next >= q.plan.Jobs {
		return nil
	}
	n := 1
	if q.plan.Batched {
		n = q.plan.Jobs - q.next
		if q.plan.Workers > 1 {
			// Smaller shares let other workers claim work while this batch runs.
			n = max(1, (n+2*q.plan.Workers-1)/(2*q.plan.Workers))
		}
	}
	jobs := make([]int, 0, n)
	for range n {
		if q.next >= q.plan.Jobs {
			break
		}
		i := q.next
		if q.plan.Order != nil {
			i = q.plan.Order[q.next]
		}
		q.next++
		q.states[i] = Claimed
		jobs = append(jobs, i)
	}
	return jobs
}

func (q *queue) halted() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.stop }

// Run opens workers concurrently and waits for every accepted worker to close.
// open must clean up partial acquisition on error. Job order and report order are
// independent: States always refers to the original job indexes.
func Run(ctx context.Context, p Plan, open func(context.Context, int) (Worker, error)) (Report, error) {
	if err := p.validate(); err != nil {
		return Report{}, err
	}
	q := &queue{plan: p, states: make([]State, p.Jobs)}
	var wg sync.WaitGroup
	var failures []WorkerError
	for w := 1; w <= min(p.Workers, p.Jobs); w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			worker, err := open(ctx, w)
			if err != nil {
				q.mu.Lock()
				failures = append(failures, WorkerError{w, err})
				if p.FailFast {
					q.stop = true
				}
				q.mu.Unlock()
				return
			}
			defer worker.Close()
			for {
				jobs := q.claim(ctx)
				if len(jobs) == 0 {
					return
				}
				allowed := make(map[int]bool, len(jobs))
				for _, i := range jobs {
					allowed[i] = true
				}
				retire := worker.Execute(ctx, jobs, q.halted, func(i int, failed bool) {
					q.mu.Lock()
					defer q.mu.Unlock()
					if !allowed[i] || q.states[i] != Claimed {
						return
					}
					q.states[i] = Completed
					if failed && p.FailFast {
						q.stop = true
					}
				})
				q.mu.Lock()
				for _, i := range jobs {
					if q.states[i] == Claimed {
						q.states[i] = Pending
					}
				}
				q.mu.Unlock()
				if retire {
					return
				}
			}
		}(w)
	}
	wg.Wait()
	return Report{States: q.states, Errors: failures}, nil
}
