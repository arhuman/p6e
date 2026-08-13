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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes"
	"github.com/arhuman/p6e/internal/nodes/types"
	"github.com/arhuman/p6e/internal/pipeline"
	"github.com/arhuman/p6e/internal/runtime"
	"github.com/arhuman/p6e/internal/trigger"
)

const usage = `p6e compiles and runs typed pipelines.

Usage:
  p6e check <pipeline.yaml>   compile and validate without running
  p6e run   <pipeline.yaml>   compile, then execute
  p6e nodes                   list the available node capabilities

Options for run:
  --input NAME=VALUE          supply a value the pipeline declared under inputs.
                              NAME=@FILE reads the value from a file. Repeat the
                              option once per declared input.
  --detect-mutation           report nodes that mutate a value they do not own.
                              Expensive: for debugging, not production.
  --inline                    run a solitary ready step on the main goroutine.
                              Much faster on sequential pipelines, but a node
                              that ignores cancellation will wedge the run
                              instead of being abandoned.
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
		files, runOpts, rawInputs, unknown := splitRunArgs(args[1:])
		if unknown != "" {
			fmt.Fprintf(stderr, "unknown option %q\n\n%s", unknown, usage)
			return exitUsage
		}
		return withFile(append(args[:1], files...), stderr, func(path string) int {
			return runCommand(ctx, path, stdout, stderr, runOpts, rawInputs)
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

// splitRunArgs separates run's options from its file argument. It is a hand
// rolled parser rather than a flag.FlagSet because the CLI takes the verb
// first, which the standard package does not model.
//
// Input assignments are returned unparsed: what they mean depends on the types
// the pipeline declares, which are not known until it has compiled.
func splitRunArgs(args []string) (files []string, opts runtime.Options, inputs []string, unknown string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--detect-mutation":
			opts.DetectMutation = true
		case arg == "--inline":
			opts.InlineSoloSteps = true
		case arg == "--input":
			// The separated form, "--input name=value".
			if i+1 >= len(args) {
				return nil, runtime.Options{}, nil, arg
			}
			i++
			inputs = append(inputs, args[i])
		case strings.HasPrefix(arg, "--input="):
			inputs = append(inputs, strings.TrimPrefix(arg, "--input="))
		case strings.HasPrefix(arg, "-"):
			return nil, runtime.Options{}, nil, arg
		default:
			files = append(files, arg)
		}
	}
	return files, opts, inputs, ""
}

// buildInputs turns the command line's assignments into typed values, using the
// types the compiled plan declares. An assignment the pipeline did not ask for
// is an error rather than something ignored, because a misspelled name would
// otherwise look like it worked and leave the real input unsupplied.
//
// A missing input is deliberately not reported here. The run reports it as a
// failed input step, alongside every other input, which says more than stopping
// at the first one.
func buildInputs(plan *pipeline.ExecutionPlan, assignments []string) (map[string]node.Value, error) {
	declared := make(map[string]node.TypeID, len(plan.Inputs))
	names := make([]string, 0, len(plan.Inputs))
	for _, in := range plan.Inputs {
		declared[in.Name] = in.Type
		names = append(names, in.Name)
	}

	values := make(map[string]node.Value, len(assignments))
	for _, assignment := range assignments {
		name, literal, ok := strings.Cut(assignment, "=")
		if !ok {
			return nil, fmt.Errorf("--input %q is not of the form NAME=VALUE", assignment)
		}
		typ, ok := declared[name]
		if !ok {
			if len(names) == 0 {
				return nil, fmt.Errorf("--input %q: this pipeline declares no inputs", name)
			}
			return nil, fmt.Errorf("--input %q: this pipeline declares no such input (it declares: %s)",
				name, strings.Join(names, ", "))
		}
		if _, duplicate := values[name]; duplicate {
			return nil, fmt.Errorf("--input %q was supplied more than once", name)
		}

		if path, fromFile := strings.CutPrefix(literal, "@"); fromFile {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("--input %q: %w", name, err)
			}
			literal = string(data)
		}
		value, err := convertInput(typ, literal)
		if err != nil {
			return nil, fmt.Errorf("--input %q: %w", name, err)
		}
		values[name] = value
	}
	return values, nil
}

// convertInput builds a value of the declared type from the command line's text.
//
// The CLI supplies the four types it can read from a string. The engine itself
// accepts an input of any registered type, which an embedder can supply
// directly; a pipeline wanting a document from the command line declares Bytes
// and decodes it with a step, which is the same explicit conversion every other
// edge makes.
func convertInput(typ node.TypeID, literal string) (node.Value, error) {
	switch typ {
	case "Text":
		return node.NewValue(&types.Text{Value: literal}), nil
	case "Bytes":
		return node.NewValue(&types.Bytes{Value: []byte(literal)}), nil
	case "Bool":
		b, err := strconv.ParseBool(literal)
		if err != nil {
			return node.Value{}, fmt.Errorf("%q is not a Bool such as \"true\"", literal)
		}
		return node.NewValue(&types.Bool{Value: b}), nil
	case "Int":
		n, err := strconv.ParseInt(literal, 10, 64)
		if err != nil {
			return node.Value{}, fmt.Errorf("%q is not a whole number", literal)
		}
		return node.NewValue(&types.Int{Value: n}), nil
	default:
		return node.Value{}, fmt.Errorf(
			"the command line cannot build a %s; it supplies Text, Bytes, Bool and Int", typ)
	}
}

// registries are the capabilities a pipeline may name. Nodes and triggers are
// resolved separately, because a pipeline must not be able to use one where the
// other belongs.
func registries() *pipeline.Registries {
	return &pipeline.Registries{
		Nodes:    nodes.Registry(),
		Triggers: trigger.Builtins(),
	}
}

// nameOf is how a pipeline file is identified everywhere: its base name without
// the extension, which is also the workflow ID a run reports.
func nameOf(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
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
	// Execution begins at everything with no dependency, which includes the
	// inputs: they are where a parameterized pipeline starts.
	counts := fmt.Sprintf("%d steps, %d starting",
		plan.Len()-len(plan.Inputs), len(plan.Roots)+len(plan.Inputs))
	if n := len(plan.Inputs); n > 0 {
		counts = fmt.Sprintf("%d inputs, %s", n, counts)
	}
	fmt.Fprintf(stdout, "ok: %s compiles (%s)\n", path, counts)
	return exitOK
}

func runCommand(ctx context.Context, path string, stdout, stderr io.Writer, opts runtime.Options, assignments []string) int {
	plan, code := compile(path, stderr)
	if plan == nil {
		return code
	}

	inputs, err := buildInputs(plan, assignments)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	opts.Inputs = inputs

	// Ctrl-C cancels the execution rather than killing the process, so
	// in-flight steps see a cancelled context and report what they did.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	execution := runtime.Run(ctx, plan, opts)
	report(execution, stdout, stderr)

	if execution.Failed() {
		return exitFailure
	}
	// A detected mutation means the pipeline produced its answer by breaking a
	// rule, so it is a failure even though every step reported success.
	if len(execution.Mutations) > 0 {
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

	plan, err := pipeline.Compile(file, registries(), nameOf(path))
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
	for _, violation := range ex.Mutations {
		fmt.Fprintf(stderr, "\nimmutability violation: %s\n", violation)
	}
	if ex.Abandoned > 0 {
		fmt.Fprintf(stderr, "\n%d step(s) were abandoned still running: a node is ignoring its context\n", ex.Abandoned)
	}

	switch {
	case ex.FailedStep >= 0:
		failed := ex.Steps[ex.FailedStep]
		fmt.Fprintf(stderr, "\nfailed at step %q: %v\n", failed.ID, failed.Err)
	case ex.Cancelled:
		fmt.Fprintf(stderr, "\ncancelled\n")
	case len(ex.Mutations) > 0:
		fmt.Fprintf(stderr, "\nevery step succeeded, but the run broke the immutability rule\n")
	default:
		fmt.Fprintf(stdout, "\nok: %d steps\n", len(ex.Steps))
	}
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
