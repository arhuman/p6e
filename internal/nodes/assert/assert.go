// Package assert turns a computed fact into the run's outcome.
//
// The engine has no branching: a Bool on an edge is data, and nothing consumes
// it as control flow. What the engine does have is "a failed step stops the
// run", which is already a decision the whole pipeline observes. An assertion
// is the bridge between the two, and it needs nothing from the executor.
//
// That covers the shape a monitor or a contract check wants, which is "fail if
// this is not so". It deliberately does not cover "carry on quietly if this is
// not so": suppressing the rest of a branch without failing needs a skipped
// terminal state that the scheduler honours, and that is engine work.
package assert

import (
	"context"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// TrueName is the capability a pipeline references with "uses: assert.true".
const TrueName = "assert.true"

// trueConfig is an assert.true step's with block.
type trueConfig struct {
	// Message replaces the default failure text. It should say what was
	// expected, since it is what a person reads when the run fails.
	Message string `yaml:"message"`
}

// TrueDefinition is the "assert.true" capability: Bool in, the same Bool out.
//
// A false verdict fails the step, which stops the run and gives the process a
// non-zero exit code. That is what makes a pipeline usable from cron or CI,
// where the exit code is the whole interface.
//
// The verdict passes through rather than being consumed, which is what lets a
// later step depend on this one. Since needs means "consumes the output of", a
// step that must run only after an assertion holds takes that Bool as its
// input. A failed assertion stops the run before any of them start, so this is
// the one form of conditional execution available without engine support.
//
// There is no assert.false. A negative test belongs in the node that produced
// the verdict, where condition can already express it with equals or with
// exists: false, rather than in a second assertion node.
func TrueDefinition() node.Definition {
	return node.Definition{
		Name: TrueName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c trueConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", TrueName)
			}
			message := c.Message
			if message == "" {
				message = "assertion failed: the verdict is false"
			}

			return node.NewTypedNode(TrueName,
				func(_ context.Context, _ *node.ExecutionContext, verdict *types.Bool) node.Result[*types.Bool] {
					if !verdict.Value {
						// Permanent, because the verdict is a fact about what
						// was measured. Retrying this step would re-test the
						// same value and reach the same answer; retrying the
						// work that produced it is that step's own policy.
						return node.Fail[*types.Bool](node.Errf(node.KindPermanent,
							"assertion_failed", "%s", message))
					}
					return node.Ok(verdict)
				}), nil
		},
	}
}
