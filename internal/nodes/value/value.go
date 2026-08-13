// Package value provides the "value" node: a typed constant declared entirely
// in a step's with block, mainly for tests and examples.
package value

import (
	"context"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

// acceptedTypes is what a with block may name, rendered for error messages.
const acceptedTypes = "Bytes, Text, Bool, Int"

// config is the with block: a type name and the literal to produce.
//
// The literal stays a yaml.Node because its shape is only known once the type
// name has been read.
type config struct {
	Type  string    `yaml:"type"`
	Value yaml.Node `yaml:"value"`
}

// Definition is the "value" capability: a source node producing the constant
// its with block declares.
//
// The configured type name decides the node's output type, so the descriptor a
// step exposes depends on its configuration. That is legal because New runs at
// compile time, before the graph is type checked. A missing or unknown type
// name, a missing value, or a literal that does not fit the declared type is a
// configuration error, reported by New and never at execution.
func Definition() node.Definition {
	return node.Definition{Name: "value", New: newNode}
}

func newNode(cfg node.Config) (node.RuntimeNode, error) { //nolint:gocyclo // One branch per supported constant type: a table, not logic.
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}
	if c.Value.IsZero() {
		return nil, node.Errf(node.KindInvalidInput, "missing_value",
			"value node requires a value")
	}

	switch c.Type {
	case "Bytes":
		s, err := decodeLiteral[string](&c.Value, c.Type)
		if err != nil {
			return nil, err
		}
		return constant(&types.Bytes{Value: []byte(s)}), nil
	case "Text":
		s, err := decodeLiteral[string](&c.Value, c.Type)
		if err != nil {
			return nil, err
		}
		return constant(&types.Text{Value: s}), nil
	case "Bool":
		b, err := decodeLiteral[bool](&c.Value, c.Type)
		if err != nil {
			return nil, err
		}
		return constant(&types.Bool{Value: b}), nil
	case "Int":
		n, err := decodeLiteral[int64](&c.Value, c.Type)
		if err != nil {
			return nil, err
		}
		return constant(&types.Int{Value: n}), nil
	case "":
		return nil, node.Errf(node.KindInvalidInput, "missing_type",
			"value node requires a type (one of: %s)", acceptedTypes)
	default:
		return nil, node.Errf(node.KindInvalidInput, "unknown_type",
			"unknown value type %q (accepted: %s)", c.Type, acceptedTypes)
	}
}

func decodeLiteral[T any](n *yaml.Node, typeName string) (T, error) {
	var v T
	if err := n.Decode(&v); err != nil {
		return v, node.Wrap(err, node.KindInvalidInput, "bad_literal",
			"value is not a valid %s", typeName)
	}
	return v, nil
}

// constant builds the source. The payload is allocated once and handed to every
// execution: outputs are immutable by convention, so one instance is enough and
// the node stays allocation free.
func constant[T any](v T) node.RuntimeNode {
	return node.NewSource("value", func(context.Context, *node.ExecutionContext) node.Result[T] {
		return node.Ok(v)
	})
}
