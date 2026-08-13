// Package exec runs a local process as a pipeline step.
//
// A non-zero exit code is data, not a failure: only the workflow knows whether
// a command that exits 1 is a problem, so it arrives as CommandResult.ExitCode
// on a successful edge. A node error means the process never ran, or was killed
// before it could finish.
package exec

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// Name is the capability a pipeline references with "uses: exec".
const Name = "exec"

// killGrace bounds the wait after a kill. Without it a backgrounded grandchild
// still holding the output pipe keeps Wait blocked, and a step's timeout would
// bound nothing.
const killGrace = 500 * time.Millisecond

// Definition registers the exec capability: *types.Command in,
// *types.CommandResult out.
//
// It takes no configuration. A with block is rejected rather than ignored, so a
// typo in a pipeline file fails at check time instead of quietly doing nothing.
func Definition() node.Definition {
	return node.Static(Name, runner)
}

// runner holds nothing, so one instance serves every step and every concurrent
// execution.
var runner = node.NewTypedNode(Name, run)

func run(ctx context.Context, _ *node.ExecutionContext, cmd *types.Command) node.Result[*types.CommandResult] {
	if cmd.Name == "" {
		return node.Fail[*types.CommandResult](node.Errf(node.KindInvalidInput, "no_command",
			"command has no name"))
	}

	outer := ctx
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(outer, cmd.Timeout)
		defer cancel()
	}

	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdout = &stdout
	c.Stderr = &stderr
	c.WaitDelay = killGrace

	exitCode := 0
	if err := c.Run(); err != nil {
		code, nerr := classify(outer, ctx, cmd, err)
		if nerr != nil {
			return node.Fail[*types.CommandResult](nerr)
		}
		exitCode = code
	}

	return node.Ok(&types.CommandResult{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	})
}

// classify separates the two things a failed Run can mean: a process that ran
// and exited non-zero, which is data and comes back as an exit code, and a
// process that never started or was killed, which is a node error.
//
// outer is the execution's own context and inner adds Command.Timeout. Checking
// outer first is what distinguishes a cancelled execution, which no retry can
// help, from a command that outran its own budget, which one might.
func classify(outer, inner context.Context, cmd *types.Command, err error) (int, *node.NodeError) {
	if outer.Err() != nil {
		return 0, node.Normalize(outer.Err(), "cancelled")
	}
	if inner.Err() != nil {
		return 0, node.Errf(node.KindTransient, "timeout",
			"command %q was killed after %s", cmd.Name, cmd.Timeout)
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return 0, node.Wrap(err, node.KindPermanent, "not_found",
			"command %q was not found", cmd.Name)
	}
	return 0, node.Wrap(err, node.KindPermanent, "start_failed",
		"command %q could not be started", cmd.Name)
}
