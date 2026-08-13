package trigger

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"gopkg.in/yaml.v3"
)

// config adapts a YAML fragment to node.Config, the way the pipeline package
// does for a step's with block.
type config string

func (c config) Decode(dst any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(c)))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// build resolves a trigger from the built-in definitions, failing the test if
// it does not build.
func build(t *testing.T, name string, cfg config) Trigger {
	t.Helper()

	def, err := Builtins().Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", name, err)
	}
	trg, err := def.New(cfg)
	if err != nil {
		t.Fatalf("New(%q): %v", name, err)
	}
	return trg
}

// buildErr expects a configuration to be rejected, and returns the message so
// the caller can check it says something useful.
func buildErr(t *testing.T, name string, cfg config) string {
	t.Helper()

	def, err := Builtins().Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", name, err)
	}
	trg, err := def.New(cfg)
	if err == nil {
		t.Fatalf("New(%q) accepted %q, want a configuration error", name, string(cfg))
	}
	if trg != nil {
		t.Errorf("New returned a trigger alongside an error")
	}
	return err.Error()
}

func TestDescriptorProvided(t *testing.T) {
	d := Descriptor{
		Name: "t",
		Provides: []node.PortDescriptor{
			{Name: "body", Type: "Bytes"},
			{Name: "method", Type: "Text"},
		},
	}

	if typ, ok := d.Provided("body"); !ok || typ != "Bytes" {
		t.Errorf(`Provided("body") = %q, %v; want "Bytes", true`, typ, ok)
	}
	if _, ok := d.Provided("absent"); ok {
		t.Error(`Provided("absent") should report false`)
	}
}

// The names are what a pipeline author reads when an input is not provided, so
// they carry the type as well as the name.
func TestDescriptorProvidedNames(t *testing.T) {
	d := Descriptor{Provides: []node.PortDescriptor{
		{Name: "method", Type: "Text"},
		{Name: "body", Type: "Bytes"},
	}}

	got := d.ProvidedNames()
	if !strings.Contains(got, `"body" Bytes`) || !strings.Contains(got, `"method" Text`) {
		t.Errorf("ProvidedNames() = %q, want it to name both ports with their types", got)
	}
	if strings.Index(got, "body") > strings.Index(got, "method") {
		t.Errorf("ProvidedNames() = %q, want a deterministic sorted order", got)
	}

	if empty := (Descriptor{}).ProvidedNames(); empty != "nothing" {
		t.Errorf("a trigger providing no values renders %q, want %q", empty, "nothing")
	}
}

func TestClaimZero(t *testing.T) {
	if !(Claim{}).IsZero() {
		t.Error("the zero Claim should claim nothing")
	}
	if (Claim{Kind: "http", Key: "POST /x"}).IsZero() {
		t.Error("a populated Claim should not report zero")
	}
	if got := (Claim{}).String(); got != "none" {
		t.Errorf("the zero Claim renders %q, want %q", got, "none")
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(WebhookDefinition()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Register(WebhookDefinition()); err == nil {
		t.Error("registering a name twice should fail rather than replace")
	}
}

func TestRegistryRejectsIncompleteDefinition(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(Definition{New: newWebhook}); err == nil {
		t.Error("a definition without a name should be rejected")
	}
	if err := r.Register(Definition{Name: "x"}); err == nil {
		t.Error("a definition without a constructor should be rejected")
	}
}

// An unknown trigger is a pipeline typo, so the error names what is available.
func TestResolveUnknownListsKnownNames(t *testing.T) {
	_, err := Builtins().Resolve("trigger.webook")
	if err == nil {
		t.Fatal("resolving an unknown trigger should fail")
	}
	if !strings.Contains(err.Error(), WebhookName) {
		t.Errorf("error %q should name the known triggers", err)
	}
}

func TestBuiltinsRegistersEveryDefinition(t *testing.T) {
	names := Builtins().Names()
	if len(names) != len(Definitions()) {
		t.Errorf("Builtins holds %d triggers, Definitions lists %d", len(names), len(Definitions()))
	}
}
