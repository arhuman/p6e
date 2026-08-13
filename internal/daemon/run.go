package daemon

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/runtime"
	"github.com/arhuman/p6e/internal/trigger"
)

// Error codes the daemon itself produces, as opposed to a node's. They are what
// the HTTP layer maps to a status, so they are named rather than matched on
// message text.
const (
	codeOverlapped  = "overlapped"
	codeQuarantined = "quarantined"
	codeDraining    = "draining"
)

// QuarantineAfter is how many consecutive runs may abandon a step before the
// daemon stops firing a pipeline.
//
// An abandoned step is one still running after its execution gave up on it,
// which happens when a node ignores its context. Go cannot kill the goroutine,
// so it runs until it decides to stop. In a CLI that is harmless: the process
// exits seconds later. In a daemon those goroutines accumulate for the lifetime
// of the process, at whatever rate the trigger fires, and nothing else in the
// engine stops them (ADR 0004). Quarantine is that stop.
const QuarantineAfter = 3

// state is a served pipeline's mutable bookkeeping, kept separate from the
// immutable plan it decorates.
type state struct {
	// inflight counts runs in progress, which is what the overlap policy acts
	// on.
	inflight atomic.Int64
	// consecutiveAbandoned counts runs in a row that left a step running. It
	// resets on any run that does not, because the quarantine policy is about a
	// pipeline that is persistently wedged rather than one that once was.
	consecutiveAbandoned atomic.Int64
	// abandonedRuns counts every run that left a step running, and never
	// resets. It is what a monitor should alert on: the streak drives policy,
	// this records how often it happened at all.
	abandonedRuns atomic.Int64
	// quarantined stops this pipeline firing at all.
	quarantined atomic.Bool
	// runs and failures are counters for reporting.
	runs     atomic.Int64
	failures atomic.Int64
}

// Quarantined reports whether this pipeline has been taken out of service.
func (p *Pipeline) Quarantined() bool { return p.quarantined.Load() }

// Runs reports how many runs this pipeline has started.
func (p *Pipeline) Runs() int64 { return p.runs.Load() }

// fire builds the callback a trigger uses to start a run. It is safe for
// concurrent use, because a trigger firing again before the last run finished
// is ordinary and is exactly what the overlap policy is for.
func (d *Daemon) fire(p *Pipeline) trigger.Fire {
	return func(ctx context.Context, values map[string]node.Value) trigger.Outcome {
		if p.quarantined.Load() {
			return refuse(codeQuarantined, node.KindPermanent,
				"pipeline %q is quarantined after %d consecutive runs left a step running",
				p.Name, QuarantineAfter)
		}

		// Registering the run under the read lock is what makes the drain
		// reliable: once Drain holds the write lock no new run can slip past
		// the check and be missed by the wait.
		d.mu.RLock()
		if d.draining {
			d.mu.RUnlock()
			return refuse(codeDraining, node.KindTransient, "the daemon is shutting down")
		}
		if !p.claimSlot() {
			d.mu.RUnlock()
			return refuse(codeOverlapped, node.KindTransient,
				"a run of %q is already in progress and its policy is %s",
				p.Name, pipeline.OverlapDrop)
		}
		d.wg.Add(1)
		d.inflight.Add(1)
		d.mu.RUnlock()

		defer func() {
			p.inflight.Add(-1)
			d.inflight.Add(-1)
			d.wg.Done()
		}()

		if timeout := p.Plan.Trigger.Timeout; timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		p.runs.Add(1)
		started := time.Now()
		ex := runtime.Run(ctx, p.Plan, runtime.Options{
			WorkflowID:   p.Name,
			Inputs:       values,
			Slots:        d.slots,
			AbandonAfter: d.abandon,
		})

		out := outcomeOf(ex, p.Plan.Trigger.RespondStep)
		d.record(p, ex, time.Since(started))
		// ex is not retained anywhere. A daemon that kept executions would grow
		// without bound, and every one of them pins a step's output alive.
		return out
	}
}

// claimSlot admits a run under the pipeline's overlap policy. The
// compare-and-swap is what makes drop actually exclusive: checking a counter
// and then incrementing it would let two simultaneous events both find it zero.
func (p *Pipeline) claimSlot() bool {
	if p.Plan.Trigger.Overlap == pipeline.OverlapDrop {
		return p.inflight.CompareAndSwap(0, 1)
	}
	p.inflight.Add(1)
	return true
}

// outcomeOf reduces an execution to what a trigger needs: whether it worked,
// and the one value that answers the caller.
func outcomeOf(ex *runtime.Execution, respondStep int) trigger.Outcome {
	if ex.Failed() {
		return trigger.Outcome{Err: ex.Err()}
	}
	if respondStep < 0 {
		return trigger.Outcome{}
	}
	return trigger.Outcome{Value: ex.Steps[respondStep].Value}
}

// record logs a finished run and applies the quarantine policy.
func (d *Daemon) record(p *Pipeline, ex *runtime.Execution, took time.Duration) {
	attrs := []any{
		slog.String("pipeline", p.Name),
		slog.String("execution", ex.ID),
		slog.Duration("took", took),
	}

	if ex.Abandoned > 0 {
		// Counted per run rather than per step: one run that wedges five steps
		// is one incident, and what matters is whether it keeps happening.
		p.abandonedRuns.Add(1)
		streak := p.consecutiveAbandoned.Add(1)
		d.log.Error("run abandoned a step, which leaks a goroutine for the life of the process",
			append(attrs, slog.Int("abandoned", ex.Abandoned), slog.Int64("streak", streak))...)
		if streak >= QuarantineAfter && p.quarantined.CompareAndSwap(false, true) {
			d.log.Error("pipeline quarantined and will not fire again until the daemon restarts",
				slog.String("pipeline", p.Name),
				slog.String("path", p.Path),
				slog.Int64("consecutive", streak))
		}
	} else {
		p.consecutiveAbandoned.Store(0)
	}

	if ex.Failed() {
		p.failures.Add(1)
		d.log.Warn("run failed", append(attrs, slog.String("error", ex.Err().Error()))...)
		return
	}
	d.log.Info("run finished", attrs...)
}

func refuse(code string, kind node.ErrorKind, format string, args ...any) trigger.Outcome {
	return trigger.Outcome{Err: node.Errf(kind, code, format, args...)}
}
