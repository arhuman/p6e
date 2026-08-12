package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

type box struct{ N int }

type label struct{ S string }

func init() {
	node.RegisterType[*box]("Box")
	node.RegisterType[*label]("Label")
}

// compile builds a plan from YAML, letting each test add the nodes it needs.
func compile(t *testing.T, src string, register func(*node.Registry)) *pipeline.ExecutionPlan {
	t.Helper()

	reg := node.NewRegistry()
	reg.MustRegister(node.Static("source", node.NewSource("source",
		func(context.Context, *node.ExecutionContext) node.Result[*box] {
			return node.Ok(&box{N: 1})
		})))
	reg.MustRegister(node.Static("bump", node.NewTypedNode("bump",
		func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
			return node.Ok(&box{N: b.N + 1})
		})))
	if register != nil {
		register(reg)
	}

	f, err := pipeline.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := pipeline.Compile(f, reg, "test")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

func run(t *testing.T, plan *pipeline.ExecutionPlan) *Execution {
	t.Helper()
	return Run(t.Context(), plan, Options{})
}

func mustResult(t *testing.T, ex *Execution, id string) StepResult {
	t.Helper()
	r, ok := ex.Result(id)
	if !ok {
		t.Fatalf("no step %q in execution", id)
	}
	return r
}

func boxOf(t *testing.T, r StepResult) *box {
	t.Helper()
	b, ok := r.Value.Interface().(*box)
	if !ok {
		t.Fatalf("step %q holds %T, want *box", r.ID, r.Value.Interface())
	}
	return b
}

const chain = `
version: 1
steps:
  a:
    uses: source
  b:
    uses: bump
    needs: [a]
  c:
    uses: bump
    needs: [b]
`

func TestRunLinearChain(t *testing.T) {
	ex := run(t, compile(t, chain, nil))

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if got := boxOf(t, mustResult(t, ex, "c")).N; got != 3 {
		t.Errorf("c produced %d, want 3", got)
	}
	for _, id := range []string{"a", "b", "c"} {
		if got := mustResult(t, ex, id).State; got != StateSucceeded {
			t.Errorf("step %q is %s, want succeeded", id, got)
		}
	}
}

// Independent branches must actually overlap in time. Each branch blocks until
// the other has arrived, so the test deadlocks (and fails) if they are
// serialized.
func TestIndependentBranchesRunConcurrently(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  left:
    uses: rendezvous
    needs: [a]
  right:
    uses: rendezvous
    needs: [a]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("rendezvous", node.NewTypedNode("rendezvous",
			func(ctx context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				arrived.Done()
				done := make(chan struct{})
				go func() { arrived.Wait(); close(done) }()
				select {
				case <-done:
					return node.Ok(b)
				case <-time.After(2 * time.Second):
					return node.Fail[*box](node.Errf(node.KindInternal, "serialized",
						"the other branch never started: steps are not running concurrently"))
				case <-ctx.Done():
					return node.Fail[*box](node.Normalize(ctx.Err(), "cancelled"))
				}
			})))
	})

	ex := run(t, plan)
	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
}

func TestFanInReceivesInputsInNeedsOrder(t *testing.T) {
	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  tag:
    uses: tag
    needs: [a]
  joined:
    uses: join
    needs: [a, tag]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("tag", node.NewTypedNode("tag",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*label] {
				return node.Ok(&label{S: "tagged"})
			})))
		r.MustRegister(node.Static("join", node.NewTypedNode2("join",
			func(_ context.Context, _ *node.ExecutionContext, b *box, l *label) node.Result[*label] {
				return node.Ok(&label{S: l.S + "+box"})
			})))
	})

	ex := run(t, plan)
	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	got := mustResult(t, ex, "joined").Value.Interface().(*label).S
	if got != "tagged+box" {
		t.Errorf("joined produced %q, want %q", got, "tagged+box")
	}
}

// Fan-out must hand the same reference to every dependent. Copying here is what
// would make large payloads expensive.
func TestFanOutSharesOneValue(t *testing.T) {
	var seen [2]*box
	var mu sync.Mutex
	var idx atomic.Int32

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  left:
    uses: observe
    needs: [a]
  right:
    uses: observe
    needs: [a]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("observe", node.NewTypedNode("observe",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				mu.Lock()
				seen[idx.Add(1)-1] = b
				mu.Unlock()
				return node.Ok(b)
			})))
	})

	if ex := run(t, plan); ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if seen[0] != seen[1] {
		t.Error("fan-out delivered two different pointers: the payload was copied")
	}
}

func TestFailureSkipsDownstreamSteps(t *testing.T) {
	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  boom:
    uses: boom
    needs: [a]
  after:
    uses: bump
    needs: [boom]
  later:
    uses: bump
    needs: [after]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("boom", node.NewTypedNode("boom",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				return node.Fail[*box](node.Errf(node.KindPermanent, "boom", "exploded"))
			})))
	})

	ex := run(t, plan)

	if !ex.Failed() {
		t.Fatal("expected the execution to fail")
	}
	if got := mustResult(t, ex, "boom").State; got != StateFailed {
		t.Errorf("boom is %s, want failed", got)
	}
	for _, id := range []string{"after", "later"} {
		if got := mustResult(t, ex, id).State; got != StateSkipped {
			t.Errorf("step %q is %s, want skipped", id, got)
		}
	}
	if ex.Err().Code != "boom" {
		t.Errorf("execution error is %v, want the boom error", ex.Err())
	}
}

func TestCancellationStopsExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  slow:
    uses: slow
    needs: [a]
  after:
    uses: bump
    needs: [slow]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("slow", node.NewTypedNode("slow",
			func(ctx context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				cancel()
				<-ctx.Done()
				return node.Fail[*box](node.Normalize(ctx.Err(), "cancelled"))
			})))
	})

	ex := Run(ctx, plan, Options{})

	if !ex.Failed() {
		t.Fatal("expected a cancelled execution to be reported as failed")
	}
	if got := mustResult(t, ex, "slow").State; got != StateCancelled {
		t.Errorf("slow is %s, want cancelled", got)
	}
	if got := mustResult(t, ex, "after").State; got != StateSkipped {
		t.Errorf("after is %s, want skipped", got)
	}
}

// A node that panics has broken its contract, but it must not take the engine
// with it.
func TestPanicBecomesInternalError(t *testing.T) {
	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  bad:
    uses: panics
    needs: [a]
`, func(r *node.Registry) {
		r.MustRegister(node.Static("panics", node.NewTypedNode("panics",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				panic("node author's bad day")
			})))
	})

	ex := run(t, plan)

	err := mustResult(t, ex, "bad").Err
	if err == nil {
		t.Fatal("expected the panic to surface as an error")
	}
	if err.Kind != node.KindInternal || err.Code != "panic" {
		t.Errorf("error is %+v, want an internal panic error", err)
	}
	if !strings.Contains(err.Message, "node author's bad day") {
		t.Errorf("message %q should carry the panic value", err.Message)
	}
}

// The node reports whether a failure is retryable; the workflow says how many
// times; the engine does it.
func TestRetryUntilSuccess(t *testing.T) {
	var attempts atomic.Int32

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  flaky:
    uses: flaky
    needs: [a]
    retry:
      max_attempts: 3
      backoff: 1ms
`, func(r *node.Registry) {
		r.MustRegister(node.Static("flaky", node.NewTypedNode("flaky",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				if attempts.Add(1) < 3 {
					return node.Fail[*box](node.Errf(node.KindTransient, "flaky", "not yet"))
				}
				return node.Ok(b)
			})))
	})

	ex := run(t, plan)

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	result := mustResult(t, ex, "flaky")
	if result.Meta.Attempt != 3 {
		t.Errorf("Meta.Attempt = %d, want 3", result.Meta.Attempt)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("node ran %d times, want 3", got)
	}
}

func TestRetryStopsAtMaxAttempts(t *testing.T) {
	var attempts atomic.Int32

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  flaky:
    uses: flaky
    needs: [a]
    retry:
      max_attempts: 2
      backoff: 1ms
`, func(r *node.Registry) {
		r.MustRegister(node.Static("flaky", node.NewTypedNode("flaky",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				attempts.Add(1)
				return node.Fail[*box](node.Errf(node.KindTransient, "flaky", "always fails"))
			})))
	})

	ex := run(t, plan)

	if !ex.Failed() {
		t.Fatal("expected the execution to fail")
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("node ran %d times, want 2", got)
	}
}

// A permanent failure is not retried however generous the policy: retrying it
// only wastes time and multiplies side effects.
func TestPermanentFailureIsNotRetried(t *testing.T) {
	var attempts atomic.Int32

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  hard:
    uses: hard
    needs: [a]
    retry:
      max_attempts: 5
      backoff: 1ms
`, func(r *node.Registry) {
		r.MustRegister(node.Static("hard", node.NewTypedNode("hard",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				attempts.Add(1)
				return node.Fail[*box](node.Errf(node.KindPermanent, "hard", "will never work"))
			})))
	})

	if ex := run(t, plan); !ex.Failed() {
		t.Fatal("expected the execution to fail")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("node ran %d times, want 1", got)
	}
}

func TestAttemptIsVisibleToTheNode(t *testing.T) {
	var seen []int
	var mu sync.Mutex

	plan := compile(t, `
version: 1
steps:
  a:
    uses: source
  watcher:
    uses: watcher
    needs: [a]
    retry:
      max_attempts: 3
      backoff: 1ms
`, func(r *node.Registry) {
		r.MustRegister(node.Static("watcher", node.NewTypedNode("watcher",
			func(_ context.Context, ec *node.ExecutionContext, b *box) node.Result[*box] {
				mu.Lock()
				seen = append(seen, ec.Attempt)
				mu.Unlock()
				if ec.Attempt < 3 {
					return node.Fail[*box](node.Errf(node.KindTransient, "again", "retry me"))
				}
				return node.Ok(b)
			})))
	})

	if ex := run(t, plan); ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	want := []int{1, 2, 3}
	for i := range want {
		if i >= len(seen) || seen[i] != want[i] {
			t.Fatalf("attempts seen = %v, want %v", seen, want)
		}
	}
}

// One compiled plan, many concurrent runs. This is the property that lets a
// plan be compiled once and served repeatedly.
func TestPlanIsSafeForConcurrentExecutions(t *testing.T) {
	plan := compile(t, chain, nil)

	const runs = 32
	results := make(chan int, runs)
	for range runs {
		go func() {
			ex := Run(context.Background(), plan, Options{})
			if ex.Failed() {
				results <- -1
				return
			}
			r, _ := ex.Result("c")
			results <- r.Value.Interface().(*box).N
		}()
	}
	for range runs {
		if got := <-results; got != 3 {
			t.Fatalf("concurrent run produced %d, want 3", got)
		}
	}
}

func TestExecutionsHaveDistinctIDs(t *testing.T) {
	plan := compile(t, chain, nil)

	first := run(t, plan)
	second := run(t, plan)

	if first.ID == second.ID {
		t.Errorf("two executions share the ID %q", first.ID)
	}
}

func TestMetaRecordsDuration(t *testing.T) {
	ex := run(t, compile(t, chain, nil))

	if got := mustResult(t, ex, "a").Meta.Duration; got <= 0 {
		t.Errorf("Duration = %v, want a positive measurement", got)
	}
}
