package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

const (
	shell = "/bin/sh"
	echo  = "/bin/echo"
)

// TestMain skips the package where the POSIX tools these tests shell out to are
// missing, so a stripped environment reports itself instead of looking like a
// node bug.
func TestMain(m *testing.M) {
	for _, bin := range []string{shell, echo} {
		if _, err := os.Stat(bin); err != nil {
			fmt.Fprintf(os.Stderr, "skipping: %s is not available\n", bin)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// yamlConfig is a with block, decoded exactly as the compiler decodes one:
// strictly, so a test can check that an unknown field is rejected.
type yamlConfig string

func (c yamlConfig) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(c)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func testEC() *node.ExecutionContext {
	return &node.ExecutionContext{WorkflowID: "w", ExecutionID: "e", StepID: "s", Attempt: 1}
}

// newNode builds the node the way the compiler does, so the tests exercise the
// registered definition rather than reaching past it.
func newNode(t *testing.T, with string) node.RuntimeNode {
	t.Helper()
	n, err := Definition().New(yamlConfig(with))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func execute(t *testing.T, ctx context.Context, cmd *types.Command) node.ResultValue {
	t.Helper()
	return newNode(t, "").Execute(ctx, testEC(), []node.Value{node.NewValue(cmd)})
}

func succeeds(t *testing.T, r node.ResultValue) *types.CommandResult {
	t.Helper()
	if r.Failed() {
		t.Fatalf("execution failed: %v", r.Err)
	}
	res, ok := r.Value.Interface().(*types.CommandResult)
	if !ok {
		t.Fatalf("result holds %T, want *types.CommandResult", r.Value.Interface())
	}
	return res
}

func fails(t *testing.T, r node.ResultValue) *node.NodeError {
	t.Helper()
	if !r.Failed() {
		t.Fatalf("expected a failure, got %+v", r.Value.Interface())
	}
	return r.Err
}

func TestCapturesStdoutOfASuccessfulCommand(t *testing.T) {
	res := succeeds(t, execute(t, t.Context(), &types.Command{Name: echo, Args: []string{"hello"}}))

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := string(res.Stdout); got != "hello\n" {
		t.Errorf("Stdout = %q, want %q", got, "hello\n")
	}
}

// The point of the node: a command that exits non-zero has told the workflow
// something, and only the workflow knows whether that is a failure. Reporting
// it as a node error would let retry policy act on an answer.
func TestNonZeroExitIsDataNotAnError(t *testing.T) {
	res := succeeds(t, execute(t, t.Context(), &types.Command{Name: shell, Args: []string{"-c", "exit 3"}}))

	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
}

// A pipeline that greps stderr for a diagnostic must not have it interleaved
// with the output it is parsing.
func TestStdoutAndStderrAreCapturedSeparately(t *testing.T) {
	res := succeeds(t, execute(t, t.Context(), &types.Command{
		Name: shell,
		Args: []string{"-c", "echo out; echo err >&2"},
	}))

	if got := string(res.Stdout); got != "out\n" {
		t.Errorf("Stdout = %q, want %q", got, "out\n")
	}
	if got := string(res.Stderr); got != "err\n" {
		t.Errorf("Stderr = %q, want %q", got, "err\n")
	}
}

func TestRunsInTheRequestedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res := succeeds(t, execute(t, t.Context(), &types.Command{
		Name: shell,
		Args: []string{"-c", "ls"},
		Dir:  dir,
	}))

	if got := string(res.Stdout); !strings.Contains(got, "marker") {
		t.Errorf("Stdout = %q, want the contents of %s", got, dir)
	}
}

// A binary that is not there will not be there on the next attempt either, so
// the kind must keep the engine from retrying.
func TestMissingBinaryIsPermanent(t *testing.T) {
	err := fails(t, execute(t, t.Context(), &types.Command{Name: "p6e-no-such-binary"}))

	if err.Kind != node.KindPermanent {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindPermanent)
	}
	if err.Retryable {
		t.Error("a missing binary must not be retryable")
	}
	if !strings.Contains(err.Message, "p6e-no-such-binary") {
		t.Errorf("message %q should name the command", err.Message)
	}
}

// A command killed for outrunning its own budget may well fit in it next time,
// unlike one whose execution was abandoned.
func TestTimeoutIsTransient(t *testing.T) {
	err := fails(t, execute(t, t.Context(), &types.Command{
		Name:    shell,
		Args:    []string{"-c", "sleep 5"},
		Timeout: 50 * time.Millisecond,
	}))

	if err.Kind != node.KindTransient {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindTransient)
	}
	if err.Code != "timeout" {
		t.Errorf("Code = %q, want %q", err.Code, "timeout")
	}
	if !err.Retryable {
		t.Error("a timeout should be retryable")
	}
}

// A backgrounded grandchild inherits the output pipe and outlives the kill. The
// step must still return when its timeout expires rather than wait for it.
func TestTimeoutReturnsDespiteALingeringGrandchild(t *testing.T) {
	start := time.Now()

	fails(t, execute(t, t.Context(), &types.Command{
		Name:    shell,
		Args:    []string{"-c", "sleep 5 & wait"},
		Timeout: 50 * time.Millisecond,
	}))

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the step took %v: the timeout did not bound it", elapsed)
	}
}

// Cancelling the execution is not a condition in the world: retrying it would
// only be killed again.
func TestCancelledContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := fails(t, execute(t, ctx, &types.Command{Name: shell, Args: []string{"-c", "sleep 5"}}))

	if err.Kind != node.KindCancelled {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindCancelled)
	}
	if err.Retryable {
		t.Error("a cancelled execution must not be retryable")
	}
}

// The command comes from the graph, so an empty one is a pipeline bug rather
// than something the world did.
func TestEmptyCommandNameIsInvalidInput(t *testing.T) {
	err := fails(t, execute(t, t.Context(), &types.Command{}))

	if err.Kind != node.KindInvalidInput {
		t.Errorf("Kind = %q, want %q", err.Kind, node.KindInvalidInput)
	}
}

// The node reads no configuration, so a with block is a typo. Accepting it
// silently would let a pipeline pass check and then not do what it says.
func TestDefinitionRejectsAWithBlock(t *testing.T) {
	if _, err := Definition().New(yamlConfig("timeout: 5s\n")); err == nil {
		t.Fatal("expected a with block to be rejected")
	}
}

func TestDefinitionAcceptsNoConfiguration(t *testing.T) {
	if _, err := Definition().New(node.EmptyConfig); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestDescriptorTypesTheEdges(t *testing.T) {
	d := newNode(t, "").Descriptor()

	if d.Name != Name {
		t.Errorf("Name = %q, want %q", d.Name, Name)
	}
	if d.Arity() != 1 || d.Inputs[0].Type != "Command" {
		t.Errorf("inputs = %s, want (Command)", d.InputTypes())
	}
	if d.Output.Type != "CommandResult" {
		t.Errorf("output = %q, want %q", d.Output.Type, "CommandResult")
	}
}
