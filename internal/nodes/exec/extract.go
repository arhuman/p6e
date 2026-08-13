package exec

import (
	"context"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// The capabilities that read one field of a CommandResult onto an edge.
const (
	StdoutName   = "exec.stdout"
	StderrName   = "exec.stderr"
	ExitCodeName = "exec.exit_code"
)

// StdoutDefinition registers exec.stdout: *types.CommandResult in,
// *types.Bytes out.
//
// A node has one output, so exec returns everything a process did as a single
// CommandResult. Without an extractor nothing can consume that value, which
// makes exec a dead end: these three nodes are what connect it to the rest of a
// pipeline.
//
// The bytes are shared, not copied: the Bytes this produces points at the same
// backing array as the result, which is safe because values are immutable.
func StdoutDefinition() node.Definition {
	return extractor(StdoutName, func(r *types.CommandResult) *types.Bytes {
		return &types.Bytes{Value: r.Stdout}
	})
}

// StderrDefinition registers exec.stderr: *types.CommandResult in,
// *types.Bytes out. See StdoutDefinition.
func StderrDefinition() node.Definition {
	return extractor(StderrName, func(r *types.CommandResult) *types.Bytes {
		return &types.Bytes{Value: r.Stderr}
	})
}

// ExitCodeDefinition registers exec.exit_code: *types.CommandResult in,
// *types.Int out.
//
// A non-zero exit code is data, not a failure, and this is what makes that
// claim usable: a workflow reads the code and decides for itself what it means.
func ExitCodeDefinition() node.Definition {
	return extractor(ExitCodeName, func(r *types.CommandResult) *types.Int {
		return &types.Int{Value: int64(r.ExitCode)}
	})
}

// extractor builds a field reader. None of them takes configuration, and a with
// block is rejected rather than ignored, so a misplaced key fails at check time
// instead of quietly doing nothing.
func extractor[O any](name string, read func(*types.CommandResult) O) node.Definition {
	return node.Static(name, node.NewTypedNode(name,
		func(_ context.Context, _ *node.ExecutionContext, result *types.CommandResult) node.Result[O] {
			return node.Ok(read(result))
		}))
}
