// Command p6e compiles and runs pipeline files.
//
//	p6e check pipeline.yaml   validate without running
//	p6e run   pipeline.yaml   compile, then execute
//
// check does everything run does except execute: if check passes, the pipeline
// resolves, its graph is acyclic, its types line up, and every node accepted
// its configuration.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/arhuman/p6e/internal/nodes"
	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/runtime"
)

const usage = `p6e compiles and runs typed pipelines.

Usage:
  p6e check <pipeline.yaml>   compile and validate without running
  p6e run   <pipeline.yaml>   compile, then execute
  p6e nodes                   list the available node capabilities
`

// Exit codes, so a caller can tell a broken pipeline from a broken invocation.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "check":
		return withFile(args, stderr, func(path string) int {
			return checkCommand(path, stdout, stderr)
		})
	case "run":
		return withFile(args, stderr, func(path string) int {
			return runCommand(ctx, path, stdout, stderr)
		})
	case "nodes":
		for _, name := range nodes.Registry().Names() {
			fmt.Fprintln(stdout, name)
		}
		return exitOK
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

func withFile(args []string, stderr io.Writer, fn func(path string) int) int {
	if len(args) != 2 {
		fmt.Fprintf(stderr, "%s takes exactly one pipeline file\n\n%s", args[0], usage)
		return exitUsage
	}
	return fn(args[1])
}

func checkCommand(path string, stdout, stderr io.Writer) int {
	plan, code := compile(path, stderr)
	if plan == nil {
		return code
	}
	fmt.Fprintf(stdout, "ok: %s compiles (%d steps, %d starting)\n", path, plan.Len(), len(plan.Roots))
	return exitOK
}

func runCommand(ctx context.Context, path string, stdout, stderr io.Writer) int {
	plan, code := compile(path, stderr)
	if plan == nil {
		return code
	}

	// Ctrl-C cancels the execution rather than killing the process, so
	// in-flight steps see a cancelled context and report what they did.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	execution := runtime.Run(ctx, plan, runtime.Options{})
	report(execution, stdout, stderr)

	if execution.Failed() {
		return exitFailure
	}
	return exitOK
}

// compile parses and compiles, printing any problem in a form that points at
// the step responsible. It returns nil when compilation failed.
func compile(path string, stderr io.Writer) (*pipeline.ExecutionPlan, int) {
	file, err := pipeline.ParseFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return nil, exitFailure
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	plan, err := pipeline.Compile(file, nodes.Registry(), name)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", path, err)
		return nil, exitFailure
	}
	return plan, exitOK
}

// report prints one line per step, in plan order, then the outcome.
func report(ex *runtime.Execution, stdout, stderr io.Writer) {
	for _, step := range ex.Steps {
		fmt.Fprintf(stdout, "  %-9s %-24s %s\n", step.State, step.ID, detail(step))
	}
	if ex.Failed() {
		failed := ex.Steps[ex.FailedStep]
		fmt.Fprintf(stderr, "\nfailed at step %q: %v\n", failed.ID, failed.Err)
		return
	}
	fmt.Fprintf(stdout, "\nok: %d steps\n", len(ex.Steps))
}

func detail(step runtime.StepResult) string {
	switch {
	case step.Err != nil:
		return step.Err.Error()
	case step.Meta.Attempt > 1:
		return fmt.Sprintf("%s (attempt %d)", round(step.Meta.Duration), step.Meta.Attempt)
	case step.Meta.Duration > 0:
		return round(step.Meta.Duration)
	default:
		return ""
	}
}

// round keeps durations readable: nobody needs nanosecond precision on a step
// that took a second and a half.
func round(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}
