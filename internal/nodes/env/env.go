// Package env reads an environment variable into a pipeline.
//
// It exists so that a pipeline can reach a token, an endpoint or a deployment
// setting without either hardcoding it in the YAML or shelling out to read it.
package env

import (
	"context"
	"os"
	"strconv"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// Name is the capability a pipeline references with "uses: env.get".
const Name = "env.get"

// acceptedTypes is what a with block may name, rendered for error messages.
const acceptedTypes = "Text, Bytes, Bool, Int"

// config is an env.get step's with block.
type config struct {
	// Name is the environment variable to read.
	Name string `yaml:"name"`
	// As names the type to produce, which becomes this step's output type.
	As string `yaml:"as"`
	// Default is produced when the variable is unset. Declaring none makes an
	// unset variable an error.
	Default *string `yaml:"default"`
}

// Definition is the "env.get" capability: a source producing the environment
// variable its with block names, in the type it names.
//
// The variable is read at execution, not at compile time. That is deliberate
// and matters twice: `p6e check` stays runnable without a machine's secrets
// present, and one compiled plan run in two environments sees each one's
// values rather than whichever was in scope when it compiled. It also keeps
// secrets out of the plan itself.
//
// The declared type becomes the output type, the same mechanism the value node
// uses. Configuration, including whether the default parses, is still validated
// at compile time.
//
// An unset variable is a permanent error unless a default is declared: the
// environment is a fact about the world rather than a bug in the pipeline, and
// no retry will conjure the variable.
func Definition() node.Definition {
	return node.Definition{Name: Name, New: newNode}
}

func newNode(cfg node.Config) (node.RuntimeNode, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
			"invalid %s configuration", Name)
	}
	if c.Name == "" {
		return nil, node.Errf(node.KindInvalidInput, "missing_name",
			"%s requires a name: the environment variable to read", Name)
	}

	switch c.As {
	case "Text":
		return source(c, parseText)
	case "Bytes":
		return source(c, parseBytes)
	case "Bool":
		return source(c, parseBool)
	case "Int":
		return source(c, parseInt)
	case "":
		return nil, node.Errf(node.KindInvalidInput, "missing_type",
			"%s requires an \"as\" naming the type to produce (one of: %s)", Name, acceptedTypes)
	default:
		return nil, node.Errf(node.KindInvalidInput, "unknown_type",
			"unknown type %q for %s (accepted: %s)", c.As, Name, acceptedTypes)
	}
}

// source builds the reader for one output type. A declared default is parsed
// here, at compile time, by the same function the variable's value goes
// through, so a default that cannot parse fails p6e check rather than the first
// run that needs it.
func source[P any](c config, parse func(string) (P, *node.NodeError)) (node.RuntimeNode, error) {
	var fallback P
	if c.Default != nil {
		v, err := parse(*c.Default)
		if err != nil {
			return nil, node.Errf(node.KindInvalidInput, "bad_default",
				"default %q is invalid: %s", *c.Default, err.Message)
		}
		fallback = v
	}

	name, hasDefault := c.Name, c.Default != nil
	return node.NewSource(Name, func(context.Context, *node.ExecutionContext) node.Result[P] {
		raw, set := os.LookupEnv(name)
		if !set {
			if hasDefault {
				return node.Ok(fallback)
			}
			return node.Fail[P](node.Errf(node.KindPermanent, "env_absent",
				"environment variable %q is not set, and no default is configured", name))
		}
		v, err := parse(raw)
		if err != nil {
			return node.Fail[P](err)
		}
		return node.Ok(v)
	}), nil
}

func parseText(s string) (*types.Text, *node.NodeError) { return &types.Text{Value: s}, nil }

func parseBytes(s string) (*types.Bytes, *node.NodeError) {
	return &types.Bytes{Value: []byte(s)}, nil
}

func parseBool(s string) (*types.Bool, *node.NodeError) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return nil, node.Wrap(err, node.KindPermanent, "bad_value",
			"as: Bool wants a boolean such as \"true\", but the value is %q", s)
	}
	return &types.Bool{Value: b}, nil
}

func parseInt(s string) (*types.Int, *node.NodeError) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, node.Wrap(err, node.KindPermanent, "bad_value",
			"as: Int wants a whole number, but the value is %q", s)
	}
	return &types.Int{Value: n}, nil
}
