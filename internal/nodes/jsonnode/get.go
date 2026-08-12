package jsonnode

import (
	"context"
	"math"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/jsonpath"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

// GetName is the capability a pipeline references with "uses: json.get".
const GetName = "json.get"

// acceptedTypes is what a with block may name, rendered for error messages.
const acceptedTypes = "Text, Bytes, Bool, Int"

// getConfig is a json.get step's with block.
type getConfig struct {
	// Path addresses the value, dot separated, as condition's does.
	Path string `yaml:"path"`
	// As names the type to produce, which becomes this step's output type.
	As string `yaml:"as"`
	// Default is produced when the path is absent. Declaring none makes an
	// absent path an error.
	//
	// It stays a yaml.Node because its shape is only known once "as" has been
	// read, and because a zero Node is how "not declared" stays distinct from a
	// default that is deliberately empty.
	Default yaml.Node `yaml:"default"`
}

// GetDefinition is the "json.get" capability: JSONDocument to the type its with
// block names.
//
// It is what lets a document be used as data rather than only tested. condition
// answers a question about a document; this reads a value out of one so a later
// step can consume it.
//
// The declared type becomes the node's output type, which is legal because New
// runs at compile time, before the graph is type checked. That is the same
// mechanism the value node uses, and it is what keeps extraction statically
// typed without a structural type system: the pipeline states the type it
// expects, and the compiler checks every use of it.
//
// Conversion is explicit and never coerces. A JSON string does not satisfy
// as: Int, and a JSON number does not satisfy as: Text. A JSON number does
// satisfy as: Int when it has no fractional part, because JSON has no integer
// type of its own.
//
// An absent path is an error unless the with block declares a default, for the
// same reason http.header works that way: there is no optional type, so a zero
// value would be indistinguishable from a real one and would flow on unnoticed.
func GetDefinition() node.Definition {
	return node.Definition{Name: GetName, New: newGet}
}

func newGet(cfg node.Config) (node.RuntimeNode, error) {
	var c getConfig
	if err := cfg.Decode(&c); err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
			"invalid %s configuration", GetName)
	}
	path, err := jsonpath.Parse(GetName, c.Path)
	if err != nil {
		return nil, err
	}

	switch c.As {
	case "Text":
		return getter(path, &c.Default, toText)
	case "Bytes":
		return getter(path, &c.Default, toBytes)
	case "Bool":
		return getter(path, &c.Default, toBool)
	case "Int":
		return getter(path, &c.Default, toInt)
	case "":
		return nil, node.Errf(node.KindInvalidInput, "missing_type",
			"%s requires an \"as\" naming the type to read (one of: %s)", GetName, acceptedTypes)
	default:
		return nil, node.Errf(node.KindInvalidInput, "unknown_type",
			"unknown type %q for %s (accepted: %s)", c.As, GetName, acceptedTypes)
	}
}

// getter builds the reader for one output type.
//
// The default is decoded and converted here, at compile time, by the same
// function the document's value goes through. A default that does not fit the
// declared type is therefore a configuration error rather than a surprise on
// the first run that needs it.
func getter[P any](path []string, raw *yaml.Node, convert func(any) (P, *node.NodeError)) (node.RuntimeNode, error) {
	var fallback P
	hasDefault := !raw.IsZero()
	if hasDefault {
		var literal any
		if err := raw.Decode(&literal); err != nil {
			return nil, node.Wrap(err, node.KindInvalidInput, "bad_default",
				"default is not a usable scalar")
		}
		v, err := convert(literal)
		if err != nil {
			return nil, node.Errf(node.KindInvalidInput, "bad_default", "default is invalid: %s", err.Message)
		}
		fallback = v
	}

	return node.NewTypedNode(GetName,
		func(_ context.Context, _ *node.ExecutionContext, doc *types.Document) node.Result[P] {
			found, ok := jsonpath.Lookup(doc.Root, path)
			if !ok {
				if hasDefault {
					return node.Ok(fallback)
				}
				return node.Fail[P](node.Errf(node.KindInvalidInput, "path_absent",
					"document has no value at the configured path, and no default is configured"))
			}
			v, err := convert(found)
			if err != nil {
				return node.Fail[P](err)
			}
			return node.Ok(v)
		}), nil
}

func toText(v any) (*types.Text, *node.NodeError) {
	s, ok := v.(string)
	if !ok {
		return nil, mismatch("Text", "a string", v)
	}
	return &types.Text{Value: s}, nil
}

func toBytes(v any) (*types.Bytes, *node.NodeError) {
	s, ok := v.(string)
	if !ok {
		return nil, mismatch("Bytes", "a string", v)
	}
	return &types.Bytes{Value: []byte(s)}, nil
}

func toBool(v any) (*types.Bool, *node.NodeError) {
	b, ok := v.(bool)
	if !ok {
		return nil, mismatch("Bool", "a boolean", v)
	}
	return &types.Bool{Value: b}, nil
}

// toInt accepts the several shapes a whole number arrives in: encoding/json
// decodes every number as a float64, while a YAML default decodes as an int.
func toInt(v any) (*types.Int, *node.NodeError) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, node.Errf(node.KindInvalidInput, "type_mismatch",
				"as: Int wants a whole number, but the value is %v", n)
		}
		return &types.Int{Value: int64(n)}, nil
	case int:
		return &types.Int{Value: int64(n)}, nil
	case int64:
		return &types.Int{Value: n}, nil
	case uint64:
		return &types.Int{Value: int64(n)}, nil
	default:
		return nil, mismatch("Int", "a number", v)
	}
}

func mismatch(as, want string, got any) *node.NodeError {
	return node.Errf(node.KindInvalidInput, "type_mismatch",
		"as: %s wants %s, but the value is %s", as, want, jsonpath.Describe(got))
}
