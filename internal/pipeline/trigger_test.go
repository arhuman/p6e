package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"github.com/arhuman/p6e/internal/trigger"
)

// triggerRegistry is testRegistry plus a node that both consumes and produces a
// payload, which is what a triggered pipeline needs: something to take the
// event's body and something whose output can be written back.
func triggerRegistry(t *testing.T) *node.Registry {
	t.Helper()
	r := testRegistry(t)

	r.MustRegister(node.Static("bytes.echo", node.NewTypedNode("bytes.echo",
		func(_ context.Context, _ *node.ExecutionContext, b *types.Bytes) node.Result[*types.Bytes] {
			return node.Ok(b)
		})))

	return r
}

func compileTrigger(t *testing.T, src string) (*ExecutionPlan, error) {
	t.Helper()
	f, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Compile(f, &Registries{Nodes: triggerRegistry(t), Triggers: trigger.Builtins()}, "test")
}

func mustCompileTrigger(t *testing.T, src string) *ExecutionPlan {
	t.Helper()
	p, err := compileTrigger(t, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return p
}

// triggerProblems returns the compile error's messages, failing if compilation
// unexpectedly succeeded.
func triggerProblems(t *testing.T, src string) string {
	t.Helper()
	_, err := compileTrigger(t, src)
	if err == nil {
		t.Fatal("expected compilation to fail")
	}
	compileErr, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("err is %T, want *CompileError", err)
	}
	var sb strings.Builder
	for _, p := range compileErr.Problems {
		sb.WriteString(p.String())
		sb.WriteString("\n")
	}
	return sb.String()
}

const webhookPipeline = `
version: 1
inputs:
  body: Bytes
trigger:
  uses: trigger.webhook
  with:
    path: /hooks/deploy
  timeout: 30s
  respond_with: reply
steps:
  reply:
    uses: bytes.echo
    needs: [body]
`

func TestTriggerCompiles(t *testing.T) {
	p := mustCompileTrigger(t, webhookPipeline)

	if p.Trigger == nil {
		t.Fatal("a pipeline declaring a trigger should compile one into its plan")
	}
	if p.Trigger.Uses != trigger.WebhookName {
		t.Errorf("Uses = %q, want %q", p.Trigger.Uses, trigger.WebhookName)
	}
	if p.Trigger.Timeout != 30*time.Second {
		t.Errorf("Timeout = %s, want 30s", p.Trigger.Timeout)
	}

	want, ok := p.StepIndex("reply")
	if !ok {
		t.Fatal("the pipeline should have a step named reply")
	}
	if p.Trigger.RespondStep != want {
		t.Errorf("RespondStep = %d, want %d (the reply step)", p.Trigger.RespondStep, want)
	}
}

// A pipeline without a trigger is still perfectly valid: it is run by hand
// rather than served, and that is what the daemon uses to decide what to load.
func TestNoTriggerLeavesPlanUntriggered(t *testing.T) {
	p := mustCompile(t, "version: 1\nsteps:\n  a:\n    uses: make.alpha\n")

	if p.Trigger != nil {
		t.Errorf("a pipeline declaring no trigger compiled one: %+v", p.Trigger)
	}
}

func TestTriggerRejectsUnknownName(t *testing.T) {
	got := triggerProblems(t, `
version: 1
trigger:
  uses: trigger.webook
  timeout: 5s
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, "unknown trigger") || !strings.Contains(got, trigger.WebhookName) {
		t.Errorf("problems = %q, want an unknown-trigger error naming the known ones", got)
	}
}

func TestTriggerRejectsBadConfiguration(t *testing.T) {
	got := triggerProblems(t, `
version: 1
trigger:
  uses: trigger.webhook
  with:
    method: POST
  timeout: 5s
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, "invalid configuration") || !strings.Contains(got, "path") {
		t.Errorf("problems = %q, want the trigger's own config error", got)
	}
}

// The load-bearing check: the compiler already proved every step consumes its
// inputs at the declared types, so proving the trigger produces those same
// types is what extends the proof out to the event itself.
func TestTriggerMustSupplyEveryDeclaredInput(t *testing.T) {
	got := triggerProblems(t, `
version: 1
inputs:
  payload: Bytes
trigger:
  uses: trigger.webhook
  with:
    path: /x
  timeout: 5s
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, `input "payload" is not supplied`) {
		t.Errorf("problems = %q, want the unsupplied input named", got)
	}
	if !strings.Contains(got, "body") {
		t.Errorf("problems = %q, want it to list what the trigger does supply", got)
	}
}

func TestTriggerMustSupplyInputAtDeclaredType(t *testing.T) {
	got := triggerProblems(t, `
version: 1
inputs:
  body: Text
trigger:
  uses: trigger.webhook
  with:
    path: /x
  timeout: 5s
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, `input "body" is declared Text`) || !strings.Contains(got, "Bytes") {
		t.Errorf("problems = %q, want the declared and supplied types compared", got)
	}
}

// An unbounded run holds the caller's connection open for as long as it takes,
// and no default is safe enough to choose on the pipeline's behalf.
func TestWebhookRequiresTimeout(t *testing.T) {
	got := triggerProblems(t, `
version: 1
trigger:
  uses: trigger.webhook
  with:
    path: /x
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, "needs a timeout") {
		t.Errorf("problems = %q, want a missing-timeout error", got)
	}
}

// Nobody waits on a schedule, so it may take as long as it takes.
func TestScheduleNeedsNoTimeout(t *testing.T) {
	p := mustCompileTrigger(t, `
version: 1
trigger:
  uses: trigger.schedule
  with:
    every: 30s
steps:
  a:
    uses: make.alpha
`)
	if p.Trigger.Timeout != 0 {
		t.Errorf("Timeout = %s, want no bound", p.Trigger.Timeout)
	}
}

func TestRespondWithMustNameAStep(t *testing.T) {
	got := triggerProblems(t, strings.Replace(webhookPipeline, "respond_with: reply", "respond_with: absent", 1))

	if !strings.Contains(got, `respond_with names "absent"`) {
		t.Errorf("problems = %q, want the missing step named", got)
	}
}

func TestRespondWithRejectsAnInput(t *testing.T) {
	got := triggerProblems(t, strings.Replace(webhookPipeline, "respond_with: reply", "respond_with: body", 1))

	if !strings.Contains(got, "an input rather than a step") {
		t.Errorf("problems = %q, want an input to be refused as a response", got)
	}
}

// The trigger says what it can write, so the check needs no knowledge of domain
// types inside the compiler.
func TestRespondWithRejectsUnwritableType(t *testing.T) {
	got := triggerProblems(t, `
version: 1
trigger:
  uses: trigger.webhook
  with:
    path: /x
  timeout: 5s
  respond_with: a
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, "which produces Alpha") {
		t.Errorf("problems = %q, want the unwritable output type named", got)
	}
	if !strings.Contains(got, "Bytes") || !strings.Contains(got, "Text") {
		t.Errorf("problems = %q, want it to say what the trigger can answer with", got)
	}
}

func TestRespondWithRejectsTriggerWithNobodyToAnswer(t *testing.T) {
	got := triggerProblems(t, `
version: 1
trigger:
  uses: trigger.schedule
  with:
    every: 30s
  respond_with: a
steps:
  a:
    uses: make.alpha
`)
	if !strings.Contains(got, "nobody to answer") {
		t.Errorf("problems = %q, want respond_with refused on a schedule", got)
	}
}

// The kind of trigger is a fact; the default policy is the engine's reading of
// it. A caller waiting on its own event should not be refused because an
// unrelated one is in flight, while a schedule that piles up is a meltdown.
func TestOverlapDefaultsToTheTriggerKind(t *testing.T) {
	webhook := mustCompileTrigger(t, webhookPipeline)
	if webhook.Trigger.Overlap != OverlapAllow {
		t.Errorf("webhook overlap = %q, want %q", webhook.Trigger.Overlap, OverlapAllow)
	}

	schedule := mustCompileTrigger(t, `
version: 1
trigger:
  uses: trigger.schedule
  with:
    every: 30s
steps:
  a:
    uses: make.alpha
`)
	if schedule.Trigger.Overlap != OverlapDrop {
		t.Errorf("schedule overlap = %q, want %q", schedule.Trigger.Overlap, OverlapDrop)
	}
}

func TestOverlapCanBeDeclared(t *testing.T) {
	p := mustCompileTrigger(t, strings.Replace(
		webhookPipeline, "timeout: 30s", "timeout: 30s\n  on_overlap: drop", 1))

	if p.Trigger.Overlap != OverlapDrop {
		t.Errorf("overlap = %q, want the declared %q", p.Trigger.Overlap, OverlapDrop)
	}
}

func TestParserRejectsBadTriggerBlock(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"no uses": {`
version: 1
trigger:
  timeout: 5s
steps:
  a:
    uses: make.alpha
`, "missing uses"},
		"unknown overlap": {`
version: 1
trigger:
  uses: trigger.webhook
  on_overlap: queue
steps:
  a:
    uses: make.alpha
`, "unknown on_overlap"},
		"unknown field": {`
version: 1
trigger:
  uses: trigger.webhook
  respond: a
steps:
  a:
    uses: make.alpha
`, "respond"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.src))
			if err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// A triggered pipeline still runs by hand, which is what makes one testable
// without standing up a daemon and sending it traffic.
func TestTriggeredPipelineStillDeclaresItsInputs(t *testing.T) {
	p := mustCompileTrigger(t, webhookPipeline)

	if len(p.Inputs) != 1 || p.Inputs[0].Name != "body" {
		t.Fatalf("Inputs = %+v, want the single declared body input", p.Inputs)
	}
	if p.Inputs[0].Type != "Bytes" {
		t.Errorf("body is %s, want Bytes", p.Inputs[0].Type)
	}
}
