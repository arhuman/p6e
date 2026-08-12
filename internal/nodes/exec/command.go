package exec

import (
	"context"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// CommandName is the capability that builds a Command from a with block.
const CommandName = "exec.command"

// commandConfig is an exec.command step's with block.
type commandConfig struct {
	// Name is the program to run. It is not resolved through a shell: to use
	// shell syntax, run a shell explicitly with args.
	Name string `yaml:"name"`
	// Args are passed to the program verbatim, with no word splitting.
	Args []string `yaml:"args"`
	// Dir is the working directory, empty for the engine's own.
	Dir string `yaml:"dir"`
	// Timeout bounds the run, as a duration string such as "5s". Empty means
	// no bound beyond the execution's context.
	Timeout string `yaml:"timeout"`
}

// CommandDefinition registers the exec.command capability: a source producing
// the *types.Command its with block declares.
//
// It exists because the engine performs no implicit conversion. The exec node
// consumes a Command, so a pipeline that runs a fixed program says so in two
// steps: one that describes the command, one that runs it. That keeps a
// dynamically built command (from a future node) and a configured one on
// exactly the same footing.
func CommandDefinition() node.Definition {
	return node.Definition{
		Name: CommandName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c commandConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", CommandName)
			}
			if c.Name == "" {
				return nil, node.Errf(node.KindInvalidInput, "missing_name",
					"%s requires a name: the program to run", CommandName)
			}

			var timeout time.Duration
			if c.Timeout != "" {
				parsed, err := time.ParseDuration(c.Timeout)
				if err != nil {
					return nil, node.Wrap(err, node.KindInvalidInput, "bad_timeout",
						"invalid timeout %q, want a duration such as \"5s\"", c.Timeout)
				}
				if parsed <= 0 {
					return nil, node.Errf(node.KindInvalidInput, "bad_timeout",
						"timeout %q must be positive", c.Timeout)
				}
				timeout = parsed
			}

			// Built once at compile time and shared by every execution, which
			// is safe because values on edges are immutable.
			command := &types.Command{Name: c.Name, Args: c.Args, Dir: c.Dir, Timeout: timeout}
			return node.NewSource(CommandName,
				func(context.Context, *node.ExecutionContext) node.Result[*types.Command] {
					return node.Ok(command)
				}), nil
		},
	}
}
