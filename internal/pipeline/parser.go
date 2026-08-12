package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"

	"github.com/arhuman/p6e/internal/node"
	"gopkg.in/yaml.v3"
)

// Parse reads a pipeline document. Decoding is strict: an unknown field is an
// error, because a typo that is silently ignored produces a pipeline that
// compiles and then does the wrong thing.
//
// Parse checks only what the document says about itself. Whether the nodes
// exist, whether the graph is acyclic, and whether the types line up are the
// compiler's business.
func Parse(r io.Reader) (*File, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("pipeline is empty")
		}
		return nil, fmt.Errorf("invalid pipeline: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// ParseFile reads a pipeline document from disk.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// validate covers what can be judged from the document alone.
func (f *File) validate() error {
	if f.Version == 0 {
		return fmt.Errorf("missing version (expected %d)", Version)
	}
	if f.Version != Version {
		return fmt.Errorf("unsupported version %d (this build understands %d)", f.Version, Version)
	}
	if len(f.Steps) == 0 {
		return fmt.Errorf("pipeline has no steps")
	}

	for _, id := range f.StepIDs() {
		step := f.Steps[id]
		if step.Uses == "" {
			return fmt.Errorf("step %q: missing uses", id)
		}
		if step.Retry != nil && step.Retry.MaxAttempts < 1 {
			return fmt.Errorf("step %q: retry.max_attempts must be at least 1, got %d", id, step.Retry.MaxAttempts)
		}
		if slices.Contains(step.Needs.Steps(), id) {
			return fmt.Errorf("step %q needs itself", id)
		}
	}
	return nil
}

// StepIDs returns step IDs in sorted order. YAML mappings have no order once
// decoded, and compilation must be deterministic: the same file has to produce
// the same plan and the same first error every time.
func (f *File) StepIDs() []string {
	ids := make([]string, 0, len(f.Steps))
	for id := range f.Steps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RetryPolicy returns the step's policy, or the default when it declares none.
func (s Step) RetryPolicy() Retry {
	if s.Retry == nil {
		return DefaultRetry
	}
	return *s.Retry
}

// Config adapts a step's with block to the node package's Config interface, so
// node implementations never import a YAML library.
func (s Step) Config() node.Config {
	return yamlConfig{node: &s.With}
}

type yamlConfig struct{ node *yaml.Node }

// Decode strictly decodes the with block into dst. Round-tripping through bytes
// is what buys strict field checking, which yaml.Node.Decode does not offer.
// This runs once per step at compile time, never during execution.
func (c yamlConfig) Decode(dst any) error {
	if c.node == nil || c.node.IsZero() {
		return nil
	}
	raw, err := yaml.Marshal(c.node)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
