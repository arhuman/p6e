package exec

import (
	"context"
	"testing"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func buildCommand(t *testing.T, with string) (*types.Command, error) {
	t.Helper()

	n, err := CommandDefinition().New(yamlConfig(with))
	if err != nil {
		return nil, err
	}
	result := n.Execute(context.Background(), testEC(), nil)
	if result.Failed() {
		t.Fatalf("Execute: %v", result.Err)
	}
	return result.Value.Interface().(*types.Command), nil
}

func TestCommandDefinitionBuildsFromConfig(t *testing.T) {
	cmd, err := buildCommand(t, "name: /bin/echo\nargs: [hello, world]\ndir: /tmp\ntimeout: 5s\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if cmd.Name != "/bin/echo" {
		t.Errorf("Name = %q, want /bin/echo", cmd.Name)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != "hello" || cmd.Args[1] != "world" {
		t.Errorf("Args = %v, want [hello world]", cmd.Args)
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("Dir = %q, want /tmp", cmd.Dir)
	}
	if cmd.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cmd.Timeout)
	}
}

func TestCommandDefinitionProducesCommandType(t *testing.T) {
	n, err := CommandDefinition().New(yamlConfig("name: /bin/echo\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	desc := n.Descriptor()
	if desc.Arity() != 0 {
		t.Errorf("Arity = %d, want 0: this is a source", desc.Arity())
	}
	if desc.Output.Type != "Command" {
		t.Errorf("output type = %q, want Command", desc.Output.Type)
	}
}

// A command with no program can never run, so it fails p6e check rather than
// the pipeline.
func TestCommandDefinitionRequiresAName(t *testing.T) {
	if _, err := CommandDefinition().New(yamlConfig("args: [hello]\n")); err == nil {
		t.Error("expected a configuration error when name is missing")
	}
}

func TestCommandDefinitionRejectsBadTimeout(t *testing.T) {
	cases := map[string]string{
		"unparseable": "name: /bin/echo\ntimeout: soon\n",
		"zero":        "name: /bin/echo\ntimeout: 0s\n",
		"negative":    "name: /bin/echo\ntimeout: -1s\n",
	}
	for name, with := range cases {
		if _, err := CommandDefinition().New(yamlConfig(with)); err == nil {
			t.Errorf("%s timeout: expected a configuration error", name)
		}
	}
}

func TestCommandDefinitionRejectsUnknownField(t *testing.T) {
	if _, err := CommandDefinition().New(yamlConfig("name: /bin/echo\nargz: [x]\n")); err == nil {
		t.Error("a typo in the with block should fail at check time")
	}
}

func TestCommandWithoutTimeoutIsUnbounded(t *testing.T) {
	cmd, err := buildCommand(t, "name: /bin/echo\n")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cmd.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 meaning bounded only by the execution", cmd.Timeout)
	}
}

// The command is built once at compile time and shared, which is safe only
// because values on edges are immutable. A change that starts allocating per
// execution should have to notice this test.
func TestCommandIsSharedAcrossExecutions(t *testing.T) {
	n, err := CommandDefinition().New(yamlConfig("name: /bin/echo\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := n.Execute(context.Background(), testEC(), nil)
	second := n.Execute(context.Background(), testEC(), nil)

	if first.Value.Interface().(*types.Command) != second.Value.Interface().(*types.Command) {
		t.Error("two executions received different Command pointers")
	}
}

// The producer and the runner must agree on the type, or no pipeline could
// connect them.
func TestCommandFeedsExec(t *testing.T) {
	producer, err := CommandDefinition().New(yamlConfig("name: /bin/echo\nargs: [connected]\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runner, err := Definition().New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if producer.Descriptor().Output.Type != runner.Descriptor().Inputs[0].Type {
		t.Fatalf("exec.command produces %s but exec consumes %s",
			producer.Descriptor().Output.Type, runner.Descriptor().Inputs[0].Type)
	}

	built := producer.Execute(context.Background(), testEC(), nil)
	ran := runner.Execute(context.Background(), testEC(), []node.Value{built.Value})
	if ran.Failed() {
		t.Fatalf("exec failed: %v", ran.Err)
	}
	result := ran.Value.Interface().(*types.CommandResult)
	if string(result.Stdout) != "connected\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "connected\n")
	}
}
