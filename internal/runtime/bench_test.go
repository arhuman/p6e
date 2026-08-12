package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

// These benchmarks measure the engine, not the nodes. Every node body here is
// as close to free as Go allows, so what the numbers show is the latency the
// runtime adds between one step finishing and the next one starting.

// benchRegistry provides nodes that do nothing and allocate nothing, so the
// measurement is not diluted by their work.
func benchRegistry(payload *box) *node.Registry {
	r := node.NewRegistry()
	r.MustRegister(node.Static("source", node.NewSource("source",
		func(context.Context, *node.ExecutionContext) node.Result[*box] {
			return node.Ok(payload)
		})))
	r.MustRegister(node.Static("noop", node.NewTypedNode("noop",
		func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
			return node.Ok(b)
		})))
	r.MustRegister(node.Static("merge", node.NewTypedNode2("merge",
		func(_ context.Context, _ *node.ExecutionContext, l *box, _ *box) node.Result[*box] {
			return node.Ok(l)
		})))
	return r
}

func benchPlan(b *testing.B, src string) *pipeline.ExecutionPlan {
	b.Helper()

	f, err := pipeline.Parse(strings.NewReader(src))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	plan, err := pipeline.Compile(f, benchRegistry(&box{N: 1}), "bench")
	if err != nil {
		b.Fatalf("Compile: %v", err)
	}
	return plan
}

// runPlan is the measured loop, shared so every benchmark reports the same
// per-step figure.
func runPlan(b *testing.B, plan *pipeline.ExecutionPlan) {
	runPlanWith(b, plan, Options{})
}

func runPlanWith(b *testing.B, plan *pipeline.ExecutionPlan, opts Options) {
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if ex := Run(ctx, plan, opts); ex.Failed() {
			b.Fatalf("execution failed: %v", ex.Err())
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(plan.Len()), "ns/step")
}

// BenchmarkChainInlined is BenchmarkChain with the inline fast path on, which is
// the shape it was built for: every step is the only one ready.
func BenchmarkChainInlined(b *testing.B) {
	runPlanWith(b, benchPlan(b, chainSource(5)), Options{InlineSoloSteps: true})
}

// BenchmarkSequential100Inlined is where inlining should pay most, since all 100
// steps qualify.
func BenchmarkSequential100Inlined(b *testing.B) {
	runPlanWith(b, benchPlan(b, chainSource(100)), Options{InlineSoloSteps: true})
}

// BenchmarkFanOut100Inlined confirms inlining costs nothing where it does not
// apply: only the root qualifies.
func BenchmarkFanOut100Inlined(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("version: 1\nsteps:\n  root:\n    uses: source\n")
	for i := range 100 {
		fmt.Fprintf(&sb, "  leaf%d:\n    uses: noop\n    needs: [root]\n", i)
	}
	runPlanWith(b, benchPlan(b, sb.String()), Options{InlineSoloSteps: true})
}

// chainSource builds source -> noop -> ... -> noop.
func chainSource(steps int) string {
	var sb strings.Builder
	sb.WriteString("version: 1\nsteps:\n  s0:\n    uses: source\n")
	for i := 1; i < steps; i++ {
		fmt.Fprintf(&sb, "  s%d:\n    uses: noop\n    needs: [s%d]\n", i, i-1)
	}
	return sb.String()
}

// BenchmarkChain is the handoff's source/noop/noop/noop/sink shape: the
// smallest thing that measures step-to-step handoff.
func BenchmarkChain(b *testing.B) {
	runPlan(b, benchPlan(b, chainSource(5)))
}

// BenchmarkSequential100 amortizes per-run setup so the per-step figure is
// close to the true marginal cost of a step.
func BenchmarkSequential100(b *testing.B) {
	runPlan(b, benchPlan(b, chainSource(100)))
}

// BenchmarkFanOut100 runs 100 steps that are all ready at once, which is the
// scheduler's best case and the executor's worst case for contention.
func BenchmarkFanOut100(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("version: 1\nsteps:\n  root:\n    uses: source\n")
	for i := range 100 {
		fmt.Fprintf(&sb, "  leaf%d:\n    uses: noop\n    needs: [root]\n", i)
	}
	runPlan(b, benchPlan(b, sb.String()))
}

// BenchmarkFanIn64 reduces 64 producers through a binary tree of two-input
// merges, exercising the input gathering the compiler laid out.
func BenchmarkFanIn64(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("version: 1\nsteps:\n  root:\n    uses: source\n")

	level := make([]string, 64)
	for i := range level {
		name := fmt.Sprintf("leaf%d", i)
		fmt.Fprintf(&sb, "  %s:\n    uses: noop\n    needs: [root]\n", name)
		level[i] = name
	}
	for round := 0; len(level) > 1; round++ {
		next := make([]string, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			name := fmt.Sprintf("merge%d_%d", round, i)
			fmt.Fprintf(&sb, "  %s:\n    uses: merge\n    needs: [%s, %s]\n", name, level[i], level[i+1])
			next = append(next, name)
		}
		level = next
	}
	runPlan(b, benchPlan(b, sb.String()))
}

// BenchmarkConcurrentExecutions runs one compiled plan from many goroutines,
// the shape a daemon would see. It is also the strongest statement that a plan
// carries no per-run state.
func BenchmarkConcurrentExecutions(b *testing.B) {
	plan := benchPlan(b, chainSource(5))
	ctx := context.Background()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if ex := Run(ctx, plan, Options{}); ex.Failed() {
				b.Fatalf("execution failed: %v", ex.Err())
			}
		}
	})
}

// BenchmarkLargePayloadFanOut sends a 16 MiB payload to 32 consumers. The
// allocation figure is the point: it must not scale with the payload, because
// fan-out shares one reference rather than copying.
func BenchmarkLargePayloadFanOut(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("version: 1\nsteps:\n  root:\n    uses: source\n")
	for i := range 32 {
		fmt.Fprintf(&sb, "  leaf%d:\n    uses: noop\n    needs: [root]\n", i)
	}

	f, err := pipeline.Parse(strings.NewReader(sb.String()))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	payload := &box{N: 1, Data: make([]byte, 16<<20)}
	plan, err := pipeline.Compile(f, benchRegistry(payload), "bench")
	if err != nil {
		b.Fatalf("Compile: %v", err)
	}
	runPlan(b, plan)
}

// BenchmarkGoroutineHandoff measures the floor the current scheduler design
// sits on: one goroutine spawned per step, one channel send back to the
// coordinator. Nothing about a step can cost less than this until the executor
// learns to run a solitary ready step inline.
func BenchmarkGoroutineHandoff(b *testing.B) {
	done := make(chan completion, 1)

	b.ReportAllocs()
	for b.Loop() {
		go func() { done <- completion{index: 0} }()
		<-done
	}
}

// BenchmarkCompile measures the work that happens once per plan rather than
// once per run, to keep the two costs from being confused.
func BenchmarkCompile(b *testing.B) {
	src := chainSource(100)
	f, err := pipeline.Parse(strings.NewReader(src))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	reg := benchRegistry(&box{N: 1})

	b.ReportAllocs()
	for b.Loop() {
		if _, err := pipeline.Compile(f, reg, "bench"); err != nil {
			b.Fatal(err)
		}
	}
}
