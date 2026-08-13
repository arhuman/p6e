package runtime

import (
	"context"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/pipeline"
)

// scheduler is one run's coordinator. Every field here was a local of Run's
// loop and every method a closure over them; naming the thing they belonged to
// is the whole of this type.
//
// **Nothing here is safe for concurrent use.** All of it is owned by the single
// goroutine inside loop, which is what lets prepare write ex.Steps with no
// synchronization: those writes happen before the go statement that reads them.
// The only fields a step's goroutine touches are runCtx, done and slots, each
// written once before any step starts.
type scheduler struct {
	ex   *Execution
	plan *pipeline.ExecutionPlan

	workflowID     string
	maxConcurrency int
	abandonAfter   time.Duration
	inline         bool

	runCtx context.Context
	cancel context.CancelFunc

	// inputs is one buffer for every step's inputs, carved up by offsets the
	// compiler computed. Each step's slice is disjoint, so goroutines never
	// share.
	inputs []node.Value
	// contexts holds one ExecutionContext per step, mutated only by that step's
	// goroutine as its attempts progress.
	contexts []node.ExecutionContext
	// unmet counts each step's outstanding dependencies, counted down instead of
	// re-walking the graph.
	unmet []int

	// done is buffered to hold every step's completion, so the send never
	// blocks and an abandoned goroutine finishing later cannot leak on it.
	done chan completion
	// ready holds steps whose dependencies are met, waiting for a concurrency
	// slot. launched is a cursor into it, which avoids reslicing so this
	// allocates once.
	ready []int
	slots chan struct{}

	launched int
	inflight int
	stopped  bool

	// abandonTimer is armed only once the run is winding down, so a healthy
	// long-running pipeline is never cut short.
	abandonTimer *time.Timer
	abandon      <-chan time.Time

	guard *mutationGuard
}

func newScheduler(ctx context.Context, ex *Execution, plan *pipeline.ExecutionPlan, opts Options) *scheduler {
	// Cancelling on the first failure stops work that can no longer matter.
	runCtx, cancel := context.WithCancel(ctx)

	// Written out rather than routed through a helper taking a fallback
	// function: such a closure captures plan and escapes, which is a heap
	// allocation per run on the path ADR 0003 measures.
	workflowID := opts.WorkflowID
	if workflowID == "" {
		workflowID = plan.Name
	}

	s := &scheduler{
		ex:             ex,
		plan:           plan,
		workflowID:     workflowID,
		maxConcurrency: opts.MaxConcurrency,
		abandonAfter:   opts.AbandonAfter,
		inline:         opts.InlineSoloSteps,
		runCtx:         runCtx,
		cancel:         cancel,
		inputs:         make([]node.Value, plan.TotalInputs),
		contexts:       make([]node.ExecutionContext, len(plan.Steps)),
		unmet:          make([]int, len(plan.Steps)),
		done:           make(chan completion, len(plan.Steps)),
		ready:          make([]int, 0, len(plan.Steps)),
		slots:          opts.Slots,
		guard:          newMutationGuard(opts.DetectMutation, len(plan.Steps)),
	}
	if s.maxConcurrency <= 0 {
		s.maxConcurrency = DefaultMaxConcurrency
	}
	if s.abandonAfter <= 0 {
		s.abandonAfter = DefaultAbandonAfter
	}
	for i := range plan.Steps {
		s.unmet[i] = len(plan.Steps[i].Deps)
	}
	return s
}

// close releases what the run holds. Cancelling is what stops steps that are
// still going once Run has returned.
func (s *scheduler) close() {
	s.cancel()
	if s.abandonTimer != nil {
		s.abandonTimer.Stop()
	}
}

// prepare gathers a step's inputs and execution context, and marks it running.
func (s *scheduler) prepare(i int) []node.Value {
	step := &s.plan.Steps[i]
	in := s.inputs[step.InputOffset : step.InputOffset+len(step.Deps)]
	for port, dep := range step.Deps {
		in[port] = s.ex.Steps[dep].Value
	}
	s.contexts[i] = node.ExecutionContext{
		WorkflowID:  s.workflowID,
		ExecutionID: s.ex.ID,
		StepID:      step.ID,
	}
	s.ex.Steps[i].State = StateRunning
	return in
}

// tryTake claims a slot in the shared pool without waiting. Failing is
// ordinary: it means the process is busy, and the run waits on the pool inside
// the main select rather than blocking here.
func (s *scheduler) tryTake() bool {
	if s.slots == nil {
		return true
	}
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// launch starts a step that already holds a slot, and returns that slot once
// the step is finished rather than when the run moves on.
//
// The goroutine captures s, i and in, and reaches everything else through s.
// That is deliberate: the closure escapes, so each captured variable is paid
// for per step on the path ADR 0003 measures, and one pointer is cheaper than
// the four fields it stands in for.
//
// Releasing here rather than from the coordinator when the completion arrives
// is cheaper the other way and wrong: an abandoned step is still running, and
// handing its slot back would let a wedged node quietly raise the ceiling for
// every other pipeline.
func (s *scheduler) launch(i int) {
	in := s.prepare(i)
	s.inflight++
	go func() {
		s.done <- completion{index: i, result: runStep(s.runCtx, &s.plan.Steps[i], &s.contexts[i], in)}
		if s.slots != nil {
			<-s.slots
		}
	}()
}

// pump starts as much ready work as the caps allow, stopping at the first step
// it cannot claim a slot for.
func (s *scheduler) pump() {
	for s.launched < len(s.ready) && s.inflight < s.maxConcurrency {
		if !s.tryTake() {
			return
		}
		s.launch(s.ready[s.launched])
		s.launched++
	}
}

// windDown cancels the run and arms the abandonment deadline.
func (s *scheduler) windDown() {
	s.cancel()
	if s.abandonTimer == nil {
		s.abandonTimer = time.NewTimer(s.abandonAfter)
		s.abandon = s.abandonTimer.C
	}
}

// handle records a completion and releases whatever it unblocked. Both the
// asynchronous and the inline path go through it, so they cannot drift.
func (s *scheduler) handle(c completion) {
	if s.ex.record(c) && !s.stopped {
		s.stopped = true
		s.ex.FailedStep = c.index
		s.windDown()
	}
	s.guard.record(c.index, s.ex.Steps[c.index].Value)
	// Steps still in flight are allowed to finish and be recorded, but nothing
	// new starts once the run has stopped.
	if s.stopped {
		return
	}
	for _, dependent := range s.plan.Steps[c.index].Dependents {
		s.unmet[dependent]--
		if s.unmet[dependent] == 0 {
			s.ready = append(s.ready, dependent)
		}
	}
}

// supplyInputs records every declared input before anything is scheduled.
//
// An input carries a value rather than a computation, so it never enters the
// ready queue. Routing it through handle is what makes a missing input behave
// like any other failed step: the run stops and everything downstream is
// reported as skipped.
func (s *scheduler) supplyInputs(supplied map[string]node.Value) {
	for _, in := range s.plan.Inputs {
		s.handle(completion{index: in.Step, result: supply(supplied, in)})
	}
	s.ready = append(s.ready, s.plan.Roots...)
}

// tryInline runs a solitary ready step on this goroutine, and reports whether
// it did.
//
// With nothing else running and exactly one step ready, the goroutine and the
// channel round trip buy nothing, and they are most of what a step costs. The
// cost is that the coordinator is inside the node while it runs and cannot
// abandon it, which is why this is opt-in.
func (s *scheduler) tryInline() bool {
	if !s.inline || s.stopped || s.inflight != 0 || len(s.ready)-s.launched != 1 {
		return false
	}
	if !s.tryTake() {
		return false
	}
	i := s.ready[s.launched]
	s.launched++
	in := s.prepare(i)
	result := runStep(s.runCtx, &s.plan.Steps[i], &s.contexts[i], in)
	if s.slots != nil {
		<-s.slots
	}
	s.handle(completion{index: i, result: result})
	return true
}

// waitingOn is the shared pool arm of the select, enabled only when this run
// has work it could start but could not claim a slot for. A nil channel
// disables the arm, which is how a run that needs nothing from the pool never
// touches it.
//
// The claim belongs in the select rather than in a blocking take: a run whose
// caller has given up must not sit waiting for a slot some other pipeline might
// hold for minutes. Cancellation and abandonment stay reachable at every moment.
func (s *scheduler) waitingOn() chan<- struct{} {
	if !s.stopped && s.launched < len(s.ready) && s.inflight < s.maxConcurrency {
		return s.slots
	}
	return nil
}

// loop drives the run to completion and reports whether it gave up on steps
// that were still going.
//
// callerDone is cleared after it fires: a closed channel stays ready, and
// selecting on it again would spin.
func (s *scheduler) loop(callerDone <-chan struct{}) (abandoned bool) {
	for {
		if s.tryInline() {
			continue
		}
		if !s.stopped {
			s.pump()
		}

		waiting := s.waitingOn()
		// Nothing running and nothing startable means the graph is finished.
		// With a slot outstanding it means the opposite: there is work to do and
		// the process is full, so this run waits rather than declaring itself
		// done and skipping the rest.
		if s.inflight == 0 && waiting == nil {
			return false
		}

		select {
		case waiting <- struct{}{}:
			s.launch(s.ready[s.launched])
			s.launched++

		case c := <-s.done:
			s.inflight--
			s.handle(c)

		case <-callerDone:
			callerDone = nil
			if !s.stopped {
				s.stopped = true
				s.ex.Cancelled = true
			}
			s.windDown()

		case <-s.abandon:
			return true
		}
	}
}

// finalize settles the states the loop could not, and collects any immutability
// violations the guard saw.
func (s *scheduler) finalize(abandoned bool) {
	if abandoned {
		for i := range s.ex.Steps {
			if s.ex.Steps[i].State == StateRunning {
				s.ex.Steps[i].State = StateCancelled
				s.ex.Steps[i].Err = node.Errf(node.KindCancelled, "abandoned",
					"step %q was still running %s after the execution stopped",
					s.ex.Steps[i].ID, s.abandonAfter)
				s.ex.Abandoned++
			}
		}
		if !s.ex.Failed() {
			s.ex.Cancelled = true
		}
	}

	// Whatever never started could not: a dependency failed, or the run stopped
	// first.
	for i := range s.ex.Steps {
		if s.ex.Steps[i].State == StatePending {
			s.ex.Steps[i].State = StateSkipped
		}
	}

	s.ex.Mutations = s.guard.check(s.ex)
}
