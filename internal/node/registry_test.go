package node

import (
	"context"
	"strings"
	"testing"
)

func testDefinition(name string) Definition {
	return Static(name, NewTypedNode(name, bump))
}

func TestRegistryResolve(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(testDefinition("bump")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := r.Resolve("bump")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	n, err := d.New(EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n.Descriptor().Name != "bump" {
		t.Errorf("resolved %q, want %q", n.Descriptor().Name, "bump")
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(testDefinition("bump")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Register(testDefinition("bump")); err == nil {
		t.Error("registering a name twice should fail rather than replace")
	}
}

func TestRegistryRejectsIncompleteDefinition(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(Definition{New: testDefinition("x").New}); err == nil {
		t.Error("a definition without a name should be rejected")
	}
	if err := r.Register(Definition{Name: "x"}); err == nil {
		t.Error("a definition without a constructor should be rejected")
	}
}

// An unknown node is the most common pipeline typo, so the error names what is
// available.
func TestResolveUnknownListsKnownNames(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(testDefinition("bump"))
	r.MustRegister(testDefinition("other"))

	_, err := r.Resolve("bmup")
	if err == nil {
		t.Fatal("expected an error for an unknown node")
	}
	if !strings.Contains(err.Error(), "bump") || !strings.Contains(err.Error(), "other") {
		t.Errorf("error %q should list the known node names", err)
	}
}

func TestNamesAreSorted(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(testDefinition("zebra"))
	r.MustRegister(testDefinition("alpha"))

	got := r.Names()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zebra" {
		t.Errorf("Names = %v, want [alpha zebra]", got)
	}
}

// A definition's constructor runs at compile time, which is what lets a node's
// descriptor depend on its configuration.
func TestDefinitionConstructorMayFail(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(Definition{
		Name: "picky",
		New: func(Config) (RuntimeNode, error) {
			return nil, errBadConfig
		},
	})

	d, err := r.Resolve("picky")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := d.New(EmptyConfig); err == nil {
		t.Error("expected the constructor to reject the configuration")
	}
}

var errBadConfig = Errf(KindInvalidInput, "bad_config", "nope")

func TestStaticSharesOneInstance(t *testing.T) {
	shared := NewTypedNode("bump", bump)
	d := Static("bump", shared)

	first, err := d.New(EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := d.New(EmptyConfig)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if first != second {
		t.Error("Static should hand out one shared instance, not a copy per step")
	}
}

// One node implementation serves many concurrent executions. This is the
// property the whole runtime rests on, so it is asserted, not assumed.
func TestNodeIsSafeForConcurrentUse(t *testing.T) {
	n := NewTypedNode("bump", bump)
	ctx := context.Background()

	done := make(chan int, 64)
	for i := range 64 {
		go func() {
			in := []Value{NewValue(&payload{N: i})}
			r := n.Execute(ctx, &ExecutionContext{ExecutionID: "e", Attempt: 1}, in)
			if r.Failed() {
				done <- -1
				return
			}
			done <- r.Value.Interface().(*payload).N
		}()
	}
	for range 64 {
		if got := <-done; got < 1 {
			t.Fatalf("concurrent execution produced %d", got)
		}
	}
}
