package scheduler_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/feernandooff/mockstro/internal/scheduler"
)

type worker struct {
	execute func(context.Context, []int, func() bool, func(int, bool))
	close   func()
	retire  bool
}

func (w worker) Execute(ctx context.Context, jobs []int, stopped func() bool, done func(int, bool)) bool {
	w.execute(ctx, jobs, stopped, done)
	return w.retire
}
func (w worker) Close() {
	if w.close != nil {
		w.close()
	}
}

func TestOrderAndOriginalResultIndexes(t *testing.T) {
	for _, batched := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batched"}[batched], func(t *testing.T) {
			order := []int{4, 0, 3, 1, 2}
			var got []int
			closed := 0
			report, err := scheduler.Run(context.Background(), scheduler.Plan{Jobs: 5, Workers: 1, Order: order, Batched: batched}, func(context.Context, int) (scheduler.Worker, error) {
				return worker{execute: func(_ context.Context, jobs []int, _ func() bool, done func(int, bool)) {
					got = append(got, jobs...)
					for _, i := range jobs {
						if i != 3 {
							done(i, false)
						}
					}
				}, close: func() { closed++ }}, nil
			})
			if err != nil || !reflect.DeepEqual(got, order) || closed != 1 {
				t.Fatalf("order %v closed %d err %v", got, closed, err)
			}
			want := []scheduler.State{scheduler.Completed, scheduler.Completed, scheduler.Completed, scheduler.Pending, scheduler.Completed}
			if !reflect.DeepEqual(report.States, want) {
				t.Fatalf("states %v, want %v", report.States, want)
			}
		})
	}
}

func TestWorkersExecuteConcurrentlyAndCloseBeforeReturn(t *testing.T) {
	const count = 3
	entered := make(chan int, count)
	release := make(chan struct{})
	var mu sync.Mutex
	closed := 0
	type result struct {
		report scheduler.Report
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		report, err := scheduler.Run(context.Background(), scheduler.Plan{Jobs: count, Workers: count}, func(_ context.Context, id int) (scheduler.Worker, error) {
			return worker{execute: func(_ context.Context, jobs []int, _ func() bool, done func(int, bool)) {
				entered <- id
				<-release
				done(jobs[0], false)
			}, close: func() { mu.Lock(); closed++; mu.Unlock() }}, nil
		})
		resultCh <- result{report, err}
	}()
	seen := map[int]bool{}
	for range count {
		seen[<-entered] = true
	}
	close(release)
	r := <-resultCh
	if r.err != nil || len(seen) != count || closed != count {
		t.Fatalf("workers %v closed %d err %v", seen, closed, r.err)
	}
	for i, state := range r.report.States {
		if state != scheduler.Completed {
			t.Errorf("job %d: %v", i, state)
		}
	}
}

func TestFailFastStopsClaimsAndIgnoresDuplicateOrUnclaimedCompletion(t *testing.T) {
	var got []int
	report, err := scheduler.Run(context.Background(), scheduler.Plan{Jobs: 4, Workers: 1, FailFast: true}, func(context.Context, int) (scheduler.Worker, error) {
		return worker{execute: func(_ context.Context, jobs []int, stopped func() bool, done func(int, bool)) {
			got = append(got, jobs...)
			done(99, true) // Another job's result cannot stop this worker.
			done(-1, true)
			done(jobs[0], false)
			done(jobs[0], true) // A repeated completion cannot change a successful job.
			if stopped() {
				t.Error("invalid or duplicate completion halted scheduler")
			}
		}}, nil
	})
	if err != nil || !reflect.DeepEqual(got, []int{0, 1, 2, 3}) {
		t.Fatalf("jobs %v err %v", got, err)
	}
	for _, s := range report.States {
		if s != scheduler.Completed {
			t.Fatalf("states %v", report.States)
		}
	}
	// A first, failing completion halts future claims.
	got = nil
	report, err = scheduler.Run(context.Background(), scheduler.Plan{Jobs: 3, Workers: 1, FailFast: true}, func(context.Context, int) (scheduler.Worker, error) {
		return worker{execute: func(_ context.Context, jobs []int, stopped func() bool, done func(int, bool)) {
			got = append(got, jobs...)
			done(jobs[0], true)
			if !stopped() {
				t.Error("failure did not halt scheduler")
			}
		}}, nil
	})
	if err != nil || !reflect.DeepEqual(got, []int{0}) || !reflect.DeepEqual(report.States, []scheduler.State{scheduler.Completed, scheduler.Pending, scheduler.Pending}) {
		t.Fatalf("jobs %v states %v err %v", got, report.States, err)
	}
}

func TestCancelReturnsIncompleteClaimsAndClosesWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := false
	report, err := scheduler.Run(ctx, scheduler.Plan{Jobs: 6, Workers: 1, Batched: true}, func(context.Context, int) (scheduler.Worker, error) {
		return worker{execute: func(ctx context.Context, jobs []int, _ func() bool, done func(int, bool)) {
			done(jobs[0], false)
			cancel()
			<-ctx.Done()
		}, close: func() { closed = true }}, nil
	})
	if err != nil || !closed {
		t.Fatalf("closed %v err %v", closed, err)
	}
	if report.States[0] != scheduler.Completed {
		t.Fatalf("states %v", report.States)
	}
	for _, s := range report.States[1:] {
		if s != scheduler.Pending {
			t.Fatalf("incomplete job not pending: %v", report.States)
		}
	}
}

func TestOpenFailuresAndEmptyOrCancelledPlans(t *testing.T) {
	boom := errors.New("acquire worker")
	report, err := scheduler.Run(context.Background(), scheduler.Plan{Jobs: 2, Workers: 1, FailFast: true}, func(context.Context, int) (scheduler.Worker, error) { return nil, boom })
	if err != nil || len(report.Errors) != 1 || report.Errors[0].Worker != 1 || !errors.Is(report.Errors[0].Err, boom) {
		t.Fatalf("report %+v err %v", report, err)
	}
	for _, state := range report.States {
		if state != scheduler.Pending {
			t.Fatalf("states %v", report.States)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		jobs int
	}{{context.Background(), 0}, {ctx, 2}} {
		_, err := scheduler.Run(tc.ctx, scheduler.Plan{Jobs: tc.jobs, Workers: 3}, func(context.Context, int) (scheduler.Worker, error) {
			t.Error("unexpected worker acquisition")
			return nil, boom
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvalidPlansDoNotAcquireWorkers(t *testing.T) {
	for _, plan := range []scheduler.Plan{{Jobs: -1, Workers: 1}, {Jobs: 1}, {Jobs: 2, Workers: 1, Order: []int{0}}, {Jobs: 2, Workers: 1, Order: []int{0, 0}}, {Jobs: 2, Workers: 1, Order: []int{0, 2}}} {
		if _, err := scheduler.Run(context.Background(), plan, func(context.Context, int) (scheduler.Worker, error) {
			t.Fatal("opened worker for invalid plan")
			return nil, nil
		}); err == nil {
			t.Errorf("accepted %+v", plan)
		}
	}
}

func TestSingleWorkerSharesOneBatchAcrossAllJobs(t *testing.T) {
	var batches [][]int
	report, err := scheduler.Run(context.Background(), scheduler.Plan{Jobs: 4, Workers: 1, Batched: true}, func(context.Context, int) (scheduler.Worker, error) {
		return worker{execute: func(_ context.Context, jobs []int, _ func() bool, done func(int, bool)) {
			batches = append(batches, append([]int(nil), jobs...))
			for _, i := range jobs {
				done(i, false)
			}
		}}, nil
	})
	if err != nil || !reflect.DeepEqual(batches, [][]int{{0, 1, 2, 3}}) {
		t.Fatalf("batches %v report %+v err %v", batches, report, err)
	}
}

func TestRetiredWorkerDoesNotClaimAnotherJob(t *testing.T) {
	ctx := context.Background()
	var got []int
	closed := 0
	report, err := scheduler.Run(ctx, scheduler.Plan{Jobs: 3, Workers: 1}, func(context.Context, int) (scheduler.Worker, error) {
		return worker{retire: true, execute: func(_ context.Context, jobs []int, _ func() bool, done func(int, bool)) {
			got = append(got, jobs...)
			done(jobs[0], false)
		}, close: func() { closed++ }}, nil
	})
	if err != nil || ctx.Err() != nil || closed != 1 || !reflect.DeepEqual(got, []int{0}) ||
		!reflect.DeepEqual(report.States, []scheduler.State{scheduler.Completed, scheduler.Pending, scheduler.Pending}) {
		t.Fatalf("jobs %v, states %v, closed %d, err %v", got, report.States, closed, err)
	}
}
