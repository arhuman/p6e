package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

func extract(t *testing.T, def node.Definition, result *types.CommandResult) node.ResultValue {
	t.Helper()
	n, err := def.New(node.EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := []node.Value{node.NewValue(result)}
	return n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
}

var sample = &types.CommandResult{
	ExitCode: 3,
	Stdout:   []byte("out"),
	Stderr:   []byte("err"),
}

func TestStdoutAndStderrReadTheirOwnStream(t *testing.T) {
	out := extract(t, StdoutDefinition(), sample)
	if out.Failed() {
		t.Fatalf("exec.stdout: %v", out.Err)
	}
	if got := string(out.Value.Interface().(*types.Bytes).Value); got != "out" {
		t.Errorf("stdout = %q, want %q", got, "out")
	}

	errStream := extract(t, StderrDefinition(), sample)
	if errStream.Failed() {
		t.Fatalf("exec.stderr: %v", errStream.Err)
	}
	if got := string(errStream.Value.Interface().(*types.Bytes).Value); got != "err" {
		t.Errorf("stderr = %q, want %q", got, "err")
	}
}

// A non-zero exit code is data. This is the node that makes that usable, so it
// must report the code rather than treat it as a failure.
func TestExitCodeReportsANonZeroCodeAsAValue(t *testing.T) {
	r := extract(t, ExitCodeDefinition(), sample)

	if r.Failed() {
		t.Fatalf("a non-zero exit code must arrive as data, not an error: %v", r.Err)
	}
	if got := r.Value.Interface().(*types.Int).Value; got != 3 {
		t.Errorf("exit code = %d, want 3", got)
	}
}

// The extractor shares the result's backing array rather than copying it, which
// is what keeps a large stdout free to pass along.
func TestStdoutSharesRatherThanCopies(t *testing.T) {
	result := &types.CommandResult{Stdout: []byte("payload")}

	r := extract(t, StdoutDefinition(), result)

	got := r.Value.Interface().(*types.Bytes).Value
	if &got[0] != &result.Stdout[0] {
		t.Error("stdout should share the result's array, not copy it")
	}
}

func TestExtractorsDeclareTheirSignature(t *testing.T) {
	cases := []struct {
		def  node.Definition
		want node.TypeID
	}{
		{StdoutDefinition(), "Bytes"},
		{StderrDefinition(), "Bytes"},
		{ExitCodeDefinition(), "Int"},
	}

	for _, c := range cases {
		n, err := c.def.New(node.EmptyConfig)
		if err != nil {
			t.Fatalf("%s: New: %v", c.def.Name, err)
		}
		d := n.Descriptor()
		if d.Arity() != 1 || d.Inputs[0].Type != "CommandResult" {
			t.Errorf("%s inputs = %s, want (CommandResult)", c.def.Name, d.InputTypes())
		}
		if d.Output.Type != c.want {
			t.Errorf("%s output = %q, want %q", c.def.Name, d.Output.Type, c.want)
		}
	}
}

// An extractor has nothing to configure, so a with block is a mistake worth
// catching at check time rather than ignoring.
func TestExtractorsRejectAConfiguration(t *testing.T) {
	for _, def := range []node.Definition{StdoutDefinition(), StderrDefinition(), ExitCodeDefinition()} {
		_, err := def.New(yamlConfig("name: /bin/echo\n"))
		if err == nil {
			t.Errorf("%s: expected a with block to be rejected", def.Name)
			continue
		}
		if !strings.Contains(err.Error(), "name") {
			t.Errorf("%s: error %q should name the unknown field", def.Name, err)
		}
	}
}
