package runtime

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

// hangs forever, ignoring its context, the way a node making a synchronous call
// into a library that does not accept a context would.
func hangingSource() node.Definition {
	return node.Static("hang", node.NewSource("hang",
		func(context.Context, *node.ExecutionContext) node.Result[*box] {
			<-make(chan struct{})
			return node.Ok(&box{})
		}))
}

func failingSource() node.Definition {
	return node.Static("boom", node.NewSource("boom",
		func(context.Context, *node.ExecutionContext) node.Result[*box] {
			return node.Fail[*box](node.Errf(node.KindPermanent, "boom", "exploded"))
		}))
}

const twoRoots = `
version: 1
steps:
  boom:
    uses: boom
  hang:
    uses: hang
`

// Go cannot stop a goroutine, so a node that ignores cancellation used to block
// the coordinator forever: the loop's only exit was every step reporting. One
// such node would wedge the calling goroutine permanently.
func TestFailureDoesNotWedgeRunWhenANodeIgnoresCancellation(t *testing.T) {
	plan := compile(t, twoRoots, func(r *node.Registry) {
		r.MustRegister(failingSource())
		r.MustRegister(hangingSource())
	})

	started := time.Now()
	ex := Run(context.Background(), plan, Options{AbandonAfter: 50 * time.Millisecond})
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v: it should give up after AbandonAfter", elapsed)
	}
	if !ex.Failed() {
		t.Error("the execution should be reported as failed")
	}
	if ex.Abandoned != 1 {
		t.Errorf("Abandoned = %d, want 1: the hanging step should be counted", ex.Abandoned)
	}
	if got := mustResult(t, ex, "hang").State; got != StateCancelled {
		t.Errorf("hang is %s, want cancelled", got)
	}
	if got := mustResult(t, ex, "hang").Err; got == nil || got.Code != "abandoned" {
		t.Errorf("hang error = %v, want an abandoned error", got)
	}
}

// A caller's deadline used to be unable to bound Run: the context would end, a
// well-behaved node would return, and Run would keep waiting on the one that
// did not.
func TestCallerContextBoundsRun(t *testing.T) {
	plan := compile(t, "version: 1\nsteps:\n  hang:\n    uses: hang\n", func(r *node.Registry) {
		r.MustRegister(hangingSource())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	ex := Run(ctx, plan, Options{AbandonAfter: 50 * time.Millisecond})
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v: a caller deadline must bound it", elapsed)
	}
	if !ex.Cancelled {
		t.Error("the execution should report that it was cancelled")
	}
	if !ex.Failed() {
		t.Error("a cancelled execution has not succeeded, so Failed should be true")
	}
	if ex.Err() == nil || ex.Err().Kind != node.KindCancelled {
		t.Errorf("Err = %v, want a cancelled error", ex.Err())
	}
}

// The abandon timer must be armed only when the execution is winding down. A
// pipeline whose steps are legitimately slower than AbandonAfter must still run
// to completion, because slow and stuck are indistinguishable from outside.
func TestHealthySlowPipelineIsNotCutShort(t *testing.T) {
	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  slow:
    uses: slow
    needs: [a]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("slow", node.NewTypedNode("slow",
			func(ctx context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				select {
				case <-time.After(120 * time.Millisecond):
					return node.Ok(b)
				case <-ctx.Done():
					return node.Fail[*box](node.Normalize(ctx.Err(), "cancelled"))
				}
			})))
	})

	ex := Run(context.Background(), plan, Options{AbandonAfter: 10 * time.Millisecond})

	if ex.Failed() {
		t.Fatalf("a healthy pipeline was cut short: %v", ex.Err())
	}
	if ex.Abandoned != 0 {
		t.Errorf("Abandoned = %d, want 0", ex.Abandoned)
	}
}

// peakTracker records the highest number of steps running at once.
type peakTracker struct {
	current atomic.Int32
	peak    atomic.Int32
}

func (p *peakTracker) enter() {
	c := p.current.Add(1)
	for {
		old := p.peak.Load()
		if c <= old || p.peak.CompareAndSwap(old, c) {
			return
		}
	}
}

func (p *peakTracker) leave() { p.current.Add(-1) }

func fanOutPlan(t *testing.T, leaves int, tracker *peakTracker) *pipeline.ExecutionPlan {
	t.Helper()

	src := "version: 1\nsteps:\n  root:\n    uses: source\n"
	for i := range leaves {
		src += "  leaf" + strconv.Itoa(i) + ":\n    uses: watched\n    needs: [root]\n"
	}
	return compile(t, src, func(r *node.Registry) {
		r.MustRegister(node.Static("watched", node.NewTypedNode("watched",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				tracker.enter()
				defer tracker.leave()
				time.Sleep(2 * time.Millisecond)
				return node.Ok(b)
			})))
	})
}

// Without a cap, a wide fan-out spawns one goroutine per ready step, so a
// 10,000-way fan-out across several concurrent executions creates goroutines
// without limit.
func TestMaxConcurrencyIsRespected(t *testing.T) {
	var tracker peakTracker
	plan := fanOutPlan(t, 40, &tracker)

	ex := Run(context.Background(), plan, Options{MaxConcurrency: 4})

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if peak := tracker.peak.Load(); peak > 4 {
		t.Errorf("peak concurrency was %d, want at most 4", peak)
	}
	for i := range ex.Steps {
		if ex.Steps[i].State != StateSucceeded {
			t.Fatalf("step %q is %s, want succeeded: capping concurrency must not drop work",
				ex.Steps[i].ID, ex.Steps[i].State)
		}
	}
}

// MaxConcurrency 1 is a useful debugging mode, so it must actually serialize
// rather than deadlock on a queued step.
func TestMaxConcurrencyOneSerializes(t *testing.T) {
	var tracker peakTracker
	plan := fanOutPlan(t, 8, &tracker)

	ex := Run(context.Background(), plan, Options{MaxConcurrency: 1})

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if peak := tracker.peak.Load(); peak != 1 {
		t.Errorf("peak concurrency was %d, want exactly 1", peak)
	}
}

func TestDefaultConcurrencyAllowsFanOut(t *testing.T) {
	var tracker peakTracker
	plan := fanOutPlan(t, 16, &tracker)

	if ex := Run(context.Background(), plan, Options{}); ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if peak := tracker.peak.Load(); peak < 2 {
		t.Errorf("peak concurrency was %d: the default must not serialize independent steps", peak)
	}
}
