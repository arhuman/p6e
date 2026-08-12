package pipeline

import (
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Version is the only pipeline schema version V0 understands.
const Version = 1

// File is a parsed pipeline document, before any resolution or type checking.
// It mirrors the YAML one-to-one and knows nothing about nodes.
type File struct {
	Version int             `yaml:"version"`
	Steps   map[string]Step `yaml:"steps"`
}

// Step is one entry under steps. The map key is the step's ID.
type Step struct {
	// Uses names a capability in the registry, for example "http.request".
	Uses string `yaml:"uses"`
	// With is the node's configuration, left undecoded here. Only the node
	// knows its own config shape, and it decodes it once, at compile time.
	With yaml.Node `yaml:"with"`
	// Needs names the steps this one consumes, either positionally or by port
	// (ADR 0002, ADR 0005).
	Needs Needs `yaml:"needs"`
	// Retry is the workflow's policy for this step. The engine applies it; the
	// node knows nothing about it.
	Retry *Retry `yaml:"retry"`
}

// Retry is a step's retry policy. It is workflow policy, not node behavior:
// the node reports whether a failure is retryable, this decides what to do.
type Retry struct {
	// MaxAttempts counts the first try, so 1 means no retry.
	MaxAttempts int `yaml:"max_attempts"`
	// Backoff is the delay before the second attempt. It doubles on each
	// subsequent attempt.
	Backoff Duration `yaml:"backoff"`
}

// DefaultRetry is the policy for a step that declares none: try once.
var DefaultRetry = Retry{MaxAttempts: 1}

// Needs is a step's dependency declaration. Two forms are accepted:
//
//	needs: [fetch]                     positional, bound in port order
//	needs: {left: fetch, right: probe} named, bound by input port name
//
// The positional form is the original and stays the idiom for single-input
// steps, where there is nothing to confuse. The named form exists because
// positional binding of two ports of the same type type-checks either way
// round, so a swap is silent (ADR 0005).
type Needs struct {
	order  []string
	byPort map[string]string
	named  bool
}

// UnmarshalYAML accepts a sequence or a mapping, and rejects anything else with
// a message naming both forms.
func (n *Needs) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return fmt.Errorf("needs list: %w", err)
		}
		n.order, n.byPort, n.named = list, nil, false
		return nil

	case yaml.MappingNode:
		byPort := map[string]string{}
		if err := value.Decode(&byPort); err != nil {
			return fmt.Errorf("needs mapping: %w", err)
		}
		for port, step := range byPort {
			if step == "" {
				return fmt.Errorf("needs: input %q is bound to nothing", port)
			}
		}
		n.order, n.byPort, n.named = nil, byPort, true
		return nil

	default:
		return fmt.Errorf("needs must be a list of steps or a mapping of input name to step")
	}
}

// Named reports whether the step used the mapping form.
func (n Needs) Named() bool { return n.named }

// Len is how many dependencies were declared.
func (n Needs) Len() int {
	if n.named {
		return len(n.byPort)
	}
	return len(n.order)
}

// Positional returns the dependencies in declaration order. It is empty for the
// named form.
func (n Needs) Positional() []string { return n.order }

// Port returns the step bound to an input port name.
func (n Needs) Port(name string) (string, bool) {
	step, ok := n.byPort[name]
	return step, ok
}

// PortNames lists the bound port names in sorted order, so error messages are
// deterministic.
func (n Needs) PortNames() []string {
	names := make([]string, 0, len(n.byPort))
	for name := range n.byPort {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Steps lists every step referenced, in a deterministic order, for the checks
// that care which steps are involved rather than which port they feed.
func (n Needs) Steps() []string {
	if !n.named {
		return n.order
	}
	steps := make([]string, 0, len(n.byPort))
	for _, name := range n.PortNames() {
		steps = append(steps, n.byPort[name])
	}
	return steps
}

// Duration is a time.Duration that reads YAML's "500ms" or "2s" form, which
// yaml.v3 does not handle on its own.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("expected a duration string such as \"250ms\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration %q is negative", s)
	}
	*d = Duration(parsed)
	return nil
}

// Duration converts back to the standard type.
func (d Duration) Duration() time.Duration { return time.Duration(d) }
