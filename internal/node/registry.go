package node

import (
	"fmt"
	"sort"
	"sync"
)

// Config is a step's undecoded with block. The node package deliberately does
// not know what format it came from; the pipeline package supplies an
// implementation over YAML.
type Config interface {
	// Decode strictly decodes the block into dst, which must be a pointer to a
	// config struct. Unknown fields are an error: a typo in a pipeline file
	// should fail at check time, not be silently ignored.
	Decode(dst any) error
}

// EmptyConfig stands in for a step with no with block.
var EmptyConfig Config = emptyConfig{}

type emptyConfig struct{}

func (emptyConfig) Decode(any) error { return nil }

// Definition is a registered capability: the name a pipeline references, and
// how to build a configured node from a with block.
//
// New is called once per step at compile time, never during execution. That is
// what lets configuration decoding, validation, and any expensive setup happen
// ahead of the hot path, and it lets a node's descriptor depend on its
// configuration (the value node's output type is its configured type).
//
// The node New returns is shared by every execution of the compiled plan, so it
// must be safe for concurrent use.
type Definition struct {
	Name string
	New  func(cfg Config) (RuntimeNode, error)
}

// Static builds a Definition for a node that takes no configuration.
func Static(name string, n RuntimeNode) Definition {
	return Definition{
		Name: name,
		New:  func(Config) (RuntimeNode, error) { return n, nil },
	}
}

// Registry resolves the capability names a pipeline references. Workflows name
// capabilities, never Go types, so pipeline files stay decoupled from the
// implementation.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]Definition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

// Register adds a definition. Registering a name twice is an error: silently
// replacing a node implementation is never what the caller meant.
func (r *Registry) Register(d Definition) error {
	if d.Name == "" {
		return fmt.Errorf("node: definition has no name")
	}
	if d.New == nil {
		return fmt.Errorf("node: definition %q has no constructor", d.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[d.Name]; exists {
		return fmt.Errorf("node: %q is already registered", d.Name)
	}
	r.defs[d.Name] = d
	return nil
}

// MustRegister is Register for package init, where a failure is a build error
// in disguise.
func (r *Registry) MustRegister(d Definition) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// Resolve looks up a capability by name.
func (r *Registry) Resolve(name string) (Definition, error) {
	r.mu.RLock()
	d, ok := r.defs[name]
	r.mu.RUnlock()
	if !ok {
		return Definition{}, fmt.Errorf("unknown node %q (known: %v)", name, r.Names())
	}
	return d, nil
}

// Names lists registered capabilities in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.defs))
	for name := range r.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
