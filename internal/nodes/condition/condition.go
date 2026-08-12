// Package condition provides the "condition" node: it tests a path in a decoded
// JSON document and reports a verdict.
//
// A verdict is data, like any other value on an edge. The node does not branch:
// V0 has no branching semantics, and adding them here would put control flow
// inside a node instead of in the graph.
package condition

import (
	"context"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/jsonpath"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

// Name is the capability a pipeline references with "uses: condition".
const Name = "condition"

// config is the with block: a dot-separated path plus exactly one test.
//
// Exists is a pointer and Equals a yaml.Node so that "set to false" and "not
// set at all" stay distinguishable: exists: false is a meaningful test.
type config struct {
	Path   string    `yaml:"path"`
	Equals yaml.Node `yaml:"equals"`
	Exists *bool     `yaml:"exists"`
}

// Definition is the "condition" capability: JSONDocument to Bool.
//
// The with block names a dot-separated path and exactly one test, either equals
// or exists. Anything else is a configuration error: no path, an empty path
// segment, both tests, neither test, or a non-scalar equals.
//
// A path that does not exist is not an error. It is the false verdict for
// exists, and a false verdict for equals: asking whether something is there is
// the point of the node.
func Definition() node.Definition {
	return node.Definition{Name: "condition", New: newNode}
}

func newNode(cfg node.Config) (node.RuntimeNode, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}
	path, err := jsonpath.Parse(Name, c.Path)
	if err != nil {
		return nil, err
	}

	hasEquals := !c.Equals.IsZero()
	switch {
	case hasEquals && c.Exists != nil:
		return nil, node.Errf(node.KindInvalidInput, "ambiguous_test",
			"condition takes equals or exists, not both")
	case !hasEquals && c.Exists == nil:
		return nil, node.Errf(node.KindInvalidInput, "missing_test",
			"condition requires either equals or exists")
	case c.Exists != nil:
		want := *c.Exists
		return verdict(func(root any) bool {
			_, found := jsonpath.Lookup(root, path)
			return found == want
		}), nil
	}

	// Restricting equals to a scalar is what keeps the comparison total: two
	// maps compared with == would panic at execution.
	if c.Equals.Kind != yaml.ScalarNode {
		return nil, node.Errf(node.KindInvalidInput, "unsupported_equals",
			"equals must be a scalar such as a string, a number or a boolean")
	}
	var want any
	if err := c.Equals.Decode(&want); err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "bad_equals",
			"equals is not a usable scalar")
	}
	return verdict(func(root any) bool {
		got, found := jsonpath.Lookup(root, path)
		return found && equal(want, got)
	}), nil
}

func verdict(test func(root any) bool) node.RuntimeNode {
	return node.NewTypedNode("condition",
		func(_ context.Context, _ *node.ExecutionContext, doc *types.Document) node.Result[*types.Bool] {
			return node.Ok(&types.Bool{Value: test(doc.Root)})
		})
}

// equal compares the configured literal with the value found in the document.
// encoding/json decodes every number as float64, so the YAML integer 3 has to
// match the JSON number 3. Types are never coerced across that boundary: the
// string "3" does not match the number 3.
func equal(want, got any) bool {
	if w, ok := asFloat(want); ok {
		g, ok := asFloat(got)
		return ok && w == g
	}
	switch got.(type) {
	case string, bool, nil:
		return want == got
	default:
		return false
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}
