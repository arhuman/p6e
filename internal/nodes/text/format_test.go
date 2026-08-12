package text

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
	"gopkg.in/yaml.v3"
)

type withBlock string

func (w withBlock) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(w)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func format(t *testing.T, template string) node.RuntimeNode {
	t.Helper()
	n, err := FormatDefinition().New(withBlock("template: " + template + "\n"))
	if err != nil {
		t.Fatalf("New(%s): %v", template, err)
	}
	return n
}

func run(t *testing.T, n node.RuntimeNode, values ...string) string {
	t.Helper()
	in := make([]node.Value, len(values))
	for i, v := range values {
		in[i] = node.NewValue(&types.Text{Value: v})
	}
	r := n.Execute(context.Background(), &node.ExecutionContext{StepID: "s", Attempt: 1}, in)
	if r.Failed() {
		t.Fatalf("format: %v", r.Err)
	}
	return r.Value.Interface().(*types.Text).Value
}

// The template's placeholders become the node's ports, which is what lets a
// pipeline interpolate without an expression language.
func TestPlaceholdersBecomePorts(t *testing.T) {
	n := format(t, `"{{repo}} released {{tag}}"`)
	d := n.Descriptor()

	if d.Arity() != 2 {
		t.Fatalf("arity = %d, want 2", d.Arity())
	}
	if d.Inputs[0].Name != "repo" || d.Inputs[1].Name != "tag" {
		t.Errorf("ports = %s, want \"repo\", \"tag\" in order of appearance", d.InputNames())
	}
	for _, p := range d.Inputs {
		if p.Type != "Text" {
			t.Errorf("port %q is %s, want Text", p.Name, p.Type)
		}
	}
	if d.Output.Type != "Text" {
		t.Errorf("output = %q, want Text", d.Output.Type)
	}
}

func TestFormatInterpolates(t *testing.T) {
	got := run(t, format(t, `"{{repo}} released {{tag}}"`), "golang/go", "v1.26")

	if want := "golang/go released v1.26"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
}

// A template with no placeholder is a constant, and a template that is nothing
// but one placeholder is a rename. Both are legal.
func TestFormatEdgeShapes(t *testing.T) {
	constant := format(t, `"nothing to fill"`)
	if constant.Descriptor().Arity() != 0 {
		t.Errorf("arity = %d, want 0 for a template with no placeholder", constant.Descriptor().Arity())
	}
	if got := run(t, constant); got != "nothing to fill" {
		t.Errorf("format = %q, want the literal", got)
	}

	if got := run(t, format(t, `"{{only}}"`), "value"); got != "value" {
		t.Errorf("format = %q, want %q", got, "value")
	}
}

// One name used twice is one port, so a pipeline binds it once and it appears
// at every occurrence.
func TestRepeatedNameIsOnePort(t *testing.T) {
	n := format(t, `"{{name}} and {{name}} again"`)

	if n.Descriptor().Arity() != 1 {
		t.Fatalf("arity = %d, want 1: a repeated name is one port", n.Descriptor().Arity())
	}
	if got, want := run(t, n, "ada"), "ada and ada again"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
}

// Surrounding whitespace is a writing convenience, not part of the name.
func TestPlaceholderWhitespaceIsTrimmed(t *testing.T) {
	n := format(t, `"{{ repo }}"`)

	if n.Descriptor().Inputs[0].Name != "repo" {
		t.Errorf("port = %q, want %q", n.Descriptor().Inputs[0].Name, "repo")
	}
}

// A malformed template is a configuration error, caught at check time rather
// than producing a surprising string on a run.
func TestFormatRejectsMalformedTemplates(t *testing.T) {
	cases := []struct {
		name     string
		template string
		wants    string
	}{
		{"unclosed", `"{{repo"`, "unclosed"},
		{"empty placeholder", `"{{}}"`, "empty placeholder"},
		{"inner whitespace", `"{{two words}}"`, "whitespace"},
		{"nested open", `"{{a{{b}}"`, "unclosed"},
		{"no template", `""`, "requires a template"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FormatDefinition().New(withBlock("template: " + c.template + "\n"))
			if err == nil {
				t.Fatalf("expected template %s to be rejected", c.template)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

func TestFormatRejectsAnUnknownField(t *testing.T) {
	_, err := FormatDefinition().New(withBlock("template: x\nseparator: \",\"\n"))

	if err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
	if !strings.Contains(err.Error(), "separator") {
		t.Errorf("error %q should name the unknown field", err)
	}
}
