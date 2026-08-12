package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

// payloadSource produces one shared payload, the way the value node does.
func payloadSource(payload *box) node.Definition {
	return node.Static("payload", node.NewSource("payload",
		func(context.Context, *node.ExecutionContext) node.Result[*box] {
			return node.Ok(payload)
		}))
}

const fanOutToTwo = `
version: 1
steps:
  payload:
    uses: payload
  greedy:
    uses: greedy
    needs: [payload]
  reader:
    uses: bump
    needs: [payload]
`

// Fan-out hands both consumers the same pointer, so a consumer that writes
// through it corrupts its sibling's input. Nothing in Go prevents this, so the
// engine detects it on request instead.
func TestDetectMutationCatchesFanOutCorruption(t *testing.T) {
	plan := compile(t, fanOutToTwo, func(r *node.Registry) {
		r.MustRegister(payloadSource(&box{N: 1, Data: []byte("original")}))
		r.MustRegister(node.Static("greedy", node.NewTypedNode("greedy",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				b.Data[0] = 'X'
				return node.Ok(b)
			})))
	})

	ex := Run(context.Background(), plan, Options{DetectMutation: true})

	if len(ex.Mutations) == 0 {
		t.Fatal("mutating a shared payload went undetected")
	}
	got := ex.Mutations[0]
	if got.Step != "payload" {
		t.Errorf("violation names step %q, want the producer %q", got.Step, "payload")
	}
	if !strings.Contains(got.String(), "greedy") {
		t.Errorf("report should name the consumers that could be responsible:\n%s", got)
	}
	if got.Before == got.After {
		t.Error("the report should show what changed")
	}
}

// The detector must not cry wolf. A pipeline whose nodes obey the rule has to
// come back clean, otherwise nobody will leave the flag on long enough to find
// a real bug.
func TestDetectMutationIsQuietOnWellBehavedNodes(t *testing.T) {
	plan := compile(t, fanOutToTwo, func(r *node.Registry) {
		r.MustRegister(payloadSource(&box{N: 1, Data: []byte("original")}))
		r.MustRegister(node.Static("greedy", node.NewTypedNode("greedy",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				return node.Ok(&box{N: b.N + 1, Data: []byte("copy")})
			})))
	})

	ex := Run(context.Background(), plan, Options{DetectMutation: true})

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if len(ex.Mutations) != 0 {
		t.Errorf("clean pipeline reported violations: %v", ex.Mutations)
	}
}

func TestDetectMutationIsOffByDefault(t *testing.T) {
	plan := compile(t, fanOutToTwo, func(r *node.Registry) {
		r.MustRegister(payloadSource(&box{N: 1, Data: []byte("original")}))
		r.MustRegister(node.Static("greedy", node.NewTypedNode("greedy",
			func(_ context.Context, _ *node.ExecutionContext, b *box) node.Result[*box] {
				b.Data[0] = 'X'
				return node.Ok(b)
			})))
	})

	if ex := Run(context.Background(), plan, Options{}); len(ex.Mutations) != 0 {
		t.Error("detection should cost nothing and report nothing unless asked for")
	}
}

// Retry hands the same input to every attempt. A node that mutates its input
// before failing therefore retries against its own corrupted data, which makes
// Retryable unsound for such a node. This test pins the hazard: the second
// attempt sees the damage from the first.
func TestRetryReusesTheSameInputPointer(t *testing.T) {
	var seen []string
	var attempts atomic.Int32

	plan := compile(t, `
version: 1
steps:
  payload:
    uses: payload
  consume:
    uses: consume
    needs: [payload]
    retry:
      max_attempts: 2
      backoff: 1ms
`, func(r *node.Registry) {
		r.MustRegister(payloadSource(&box{N: 1, Data: []byte("abc")}))
		r.MustRegister(node.Static("consume", node.NewTypedNode("consume",
			func(_ context.Context, ec *node.ExecutionContext, b *box) node.Result[*box] {
				seen = append(seen, string(b.Data))
				if attempts.Add(1) == 1 {
					b.Data[0] = 'Z'
					return node.Fail[*box](node.Errf(node.KindTransient, "again", "retry me"))
				}
				return node.Ok(b)
			})))
	})

	ex := Run(context.Background(), plan, Options{DetectMutation: true})

	if ex.Failed() {
		t.Fatalf("execution failed: %v", ex.Err())
	}
	if len(seen) != 2 {
		t.Fatalf("node ran %d times, want 2", len(seen))
	}
	if seen[0] != "abc" {
		t.Errorf("first attempt saw %q, want %q", seen[0], "abc")
	}
	// This is the hazard, asserted rather than hoped away: the retry does not
	// get a fresh input.
	if seen[1] != "Zbc" {
		t.Errorf("second attempt saw %q, want %q: retry reuses the same pointer", seen[1], "Zbc")
	}
	if len(ex.Mutations) == 0 {
		t.Error("the detector should have caught the input mutation")
	}
}
