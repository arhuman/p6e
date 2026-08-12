package nodes

import (
	"testing"

	"github.com/arhuman/p6e/internal/node"
)

// Registry panics on a duplicate name, and it is built lazily on first use. A
// clash between two built-ins would therefore surface as a crash the first time
// anyone ran a pipeline. This test moves that to build time.
func TestRegistryBuilds(t *testing.T) {
	r := Registry()

	if len(r.Names()) != len(Definitions()) {
		t.Errorf("registry holds %d nodes, want %d: a name is registered twice",
			len(r.Names()), len(Definitions()))
	}
}

func TestRegistryIsShared(t *testing.T) {
	first, second := Registry(), Registry()

	if first != second {
		t.Error("Registry should return one shared instance, not build a new one per call")
	}
}

// Every capability must resolve and build, otherwise it is listed but unusable.
// Nodes that require configuration are expected to reject an empty with block,
// which is itself the contract: configuration is validated at compile time.
func TestEveryDefinitionResolves(t *testing.T) {
	r := Registry()

	for _, want := range Definitions() {
		got, err := r.Resolve(want.Name)
		if err != nil {
			t.Errorf("%s: %v", want.Name, err)
			continue
		}
		if got.New == nil {
			t.Errorf("%s: registered without a constructor", want.Name)
		}
	}
}

// The names are the pipeline-facing API: renaming one silently breaks every
// pipeline that uses it, so the set is pinned here.
func TestCapabilityNames(t *testing.T) {
	want := map[string]bool{
		"value":            true,
		"env.get":          true,
		"text.format":      true,
		"json.decode":      true,
		"json.encode":      true,
		"json.get":         true,
		"condition":        true,
		"exec.command":     true,
		"exec":             true,
		"exec.stdout":      true,
		"exec.stderr":      true,
		"exec.exit_code":   true,
		"http.build":       true,
		"http.request":     true,
		"http.body":        true,
		"http.status":      true,
		"http.header":      true,
		"http.from_url":    true,
		"http.with_header": true,
		"http.with_body":   true,
	}

	got := Registry().Names()
	if len(got) != len(want) {
		t.Errorf("registry lists %v, want %d capabilities", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected capability %q: add it to this list deliberately", name)
		}
	}
}

// Nodes that need no configuration must still build from an empty with block,
// or a pipeline could never use them.
func TestZeroConfigNodesBuild(t *testing.T) {
	for _, name := range []string{"json.decode", "exec", "http.request", "http.body"} {
		def, err := Registry().Resolve(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := def.New(node.EmptyConfig); err != nil {
			t.Errorf("%s: should build without configuration, got %v", name, err)
		}
	}
}
