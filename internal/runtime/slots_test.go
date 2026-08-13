package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
)

// fanOut is four independent steps, so a run can want four slots at once.
const fanOut = `
version: 1
steps:
  a:
    uses: watched
  b:
    uses: watched
  c:
    uses: watched
  d:
    uses: watched
`

// watcher counts how many of its steps are inside Execute at the same time,
// which is the only thing a concurrency bound is really claiming.
type watcher struct {
	live atomic.Int64
	peak atomic.Int64
	hold time.Duration
	// gate blocks every step until the test releases it, so a bound can be
	// observed rather than raced against.
	gate chan struct{}
}

func (w *watcher) register(reg *node.Registry) {
	reg.MustRegister(node.Static("watched", node.NewSource("watched",
		func(ctx context.Context, _ *node.ExecutionContext) node.Result[*box] {
			live := w.live.Add(1)
			for {
				peak := w.peak.Load()
				if live <= peak || w.peak.CompareAndSwap(peak, live) {
					break
				}
			}
			if w.gate != nil {
				select {
				case <-w.gate:
				case <-ctx.Done():
				}
			}
			if w.hold > 0 {
				select {
				case <-time.After(w.hold):
				case <-ctx.Done():
				}
			}
			w.live.Add(-1)
			return node.Ok(&box{N: 1})
		})))
}

func TestSlotsBoundStepsAcrossRuns(t *testing.T) {
	w := &watcher{hold: 5 * time.Millisecond}
	plan := compile(t, fanOut, w.register)

	slots := NewSlots(2)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ex := Run(t.Context(), plan, Options{Slots: slots})
			if ex.Failed() {
				t.Errorf("run failed: %v", ex.Err())
			}
		}()
	}
	wg.Wait()

	// Sixteen steps were runnable at once across four runs; the pool allowed
	// two. Without it each run would have started all four of its own.
	if peak := w.peak.Load(); peak > 2 {
		t.Errorf("%d steps ran at once, want at most the pool's 2", peak)
	}
	if w.peak.Load() == 0 {
		t.Error("no step ever ran")
	}
}

// Every slot must come back, or a daemon leaks its own budget one run at a time
// until nothing can start.
func TestSlotsAreReturnedAfterARun(t *testing.T) {
	w := &watcher{}
	plan := compile(t, fanOut, w.register)

	slots := NewSlots(3)
	for range 5 {
		if ex := Run(t.Context(), plan, Options{Slots: slots}); ex.Failed() {
			t.Fatalf("run failed: %v", ex.Err())
		}
	}

	if held := len(slots); held != 0 {
		t.Errorf("%d slot(s) still held after every run finished, want 0", held)
	}
}

// A run that cannot get a slot is waiting, not finished. Reporting its steps as
// skipped would be the worst possible failure: a silent no-op that looks like
// success.
func TestSlotsExhaustedStillRunsEveryStep(t *testing.T) {
	w := &watcher{}
	plan := compile(t, fanOut, w.register)

	slots := NewSlots(1)
	ex := Run(t.Context(), plan, Options{Slots: slots})

	if ex.Failed() {
		t.Fatalf("run failed: %v", ex.Err())
	}
	for _, step := range ex.Steps {
		if step.State != StateSucceeded {
			t.Errorf("step %q is %s, want succeeded", step.ID, step.State)
		}
	}
}

// The timing guarantee has to survive the pool: a run whose caller gave up must
// not sit waiting for a slot another pipeline holds. This is why the claim is
// an arm of the main select rather than a blocking take.
func TestSlotsDoNotDelayCancellation(t *testing.T) {
	w := &watcher{gate: make(chan struct{})}
	plan := compile(t, fanOut, w.register)

	// One slot, already spoken for by a run that will not finish on its own.
	slots := NewSlots(1)
	blocker := compile(t, fanOut, w.register)
	blocked := make(chan struct{})
	go func() {
		Run(context.Background(), blocker, Options{Slots: slots, AbandonAfter: time.Second})
		close(blocked)
	}()

	// Wait until the blocking run owns the slot.
	deadline := time.After(2 * time.Second)
	for len(slots) == 0 {
		select {
		case <-deadline:
			t.Fatal("the blocking run never claimed the slot")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan *Execution, 1)
	go func() { returned <- Run(ctx, plan, Options{Slots: slots, AbandonAfter: 50 * time.Millisecond}) }()

	cancel()
	select {
	case ex := <-returned:
		if !ex.Cancelled {
			t.Error("a run cancelled while waiting for a slot should report itself cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a run waiting for a slot ignored its cancelled context")
	}

	close(w.gate)
	<-blocked
}

// The inline fast path runs on the coordinator goroutine, so it has to take and
// return a slot itself rather than relying on the launch path.
func TestSlotsCoverTheInlinePath(t *testing.T) {
	w := &watcher{}
	plan := compile(t, "version: 1\nsteps:\n  a:\n    uses: watched\n", w.register)

	slots := NewSlots(1)
	ex := Run(t.Context(), plan, Options{Slots: slots, InlineSoloSteps: true})

	if ex.Failed() {
		t.Fatalf("run failed: %v", ex.Err())
	}
	if held := len(slots); held != 0 {
		t.Errorf("the inline path left %d slot(s) held, want 0", held)
	}
}

// A nil pool is the single-run case, and must cost nothing and change nothing.
func TestNilSlotsLeavesRunsUnbounded(t *testing.T) {
	w := &watcher{hold: 5 * time.Millisecond}
	plan := compile(t, fanOut, w.register)

	if ex := Run(t.Context(), plan, Options{}); ex.Failed() {
		t.Fatalf("run failed: %v", ex.Err())
	}
	if peak := w.peak.Load(); peak < 2 {
		t.Errorf("peak concurrency was %d, want an unbounded run to overlap its four steps", peak)
	}
}

func TestNewSlotsRejectsAnEmptyPool(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a pool that lets nothing run should be rejected")
		}
	}()
	NewSlots(0)
}

// The plan is shared and immutable, so the pool must be the only thing two
// concurrent runs contend on.
func TestSlotsSharedPlanStaysCorrect(t *testing.T) {
	w := &watcher{}
	plan := compile(t, fanOut, w.register)

	slots := NewSlots(2)
	var wg sync.WaitGroup
	results := make([]*Execution, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = Run(t.Context(), plan, Options{Slots: slots})
		}()
	}
	wg.Wait()

	for i, ex := range results {
		if ex.Failed() {
			t.Errorf("run %d failed: %v", i, ex.Err())
		}
		if len(ex.Steps) != len(plan.Steps) {
			t.Errorf("run %d reported %d steps, want %d", i, len(ex.Steps), len(plan.Steps))
		}
	}
}
