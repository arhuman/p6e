package pipeline

import (
	"fmt"
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
	// Needs lists the steps this one consumes, in port order (ADR 0002).
	Needs []string `yaml:"needs"`
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
