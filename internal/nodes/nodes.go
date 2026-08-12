// Package nodes assembles the built-in node registry.
//
// V0 ships only enough nodes to prove the execution model composes: a constant,
// a JSON decoder, a predicate, a local process, and an HTTP call. Building
// hundreds of integrations is explicitly not the goal. Anything n8n offers
// should be expressible as an ordinary node registered here, with no special
// case anywhere in the engine.
package nodes

import (
	"sync"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/condition"
	"github.com/arhuman/p6e/internal/nodes/env"
	"github.com/arhuman/p6e/internal/nodes/exec"
	"github.com/arhuman/p6e/internal/nodes/httpnode"
	"github.com/arhuman/p6e/internal/nodes/jsonnode"
	"github.com/arhuman/p6e/internal/nodes/text"
	"github.com/arhuman/p6e/internal/nodes/value"
)

var (
	once     sync.Once
	registry *node.Registry
)

// Registry returns the shared registry of built-in nodes.
//
// It is built once and reused: the node instances inside are stateless with
// respect to workflows and safe for concurrent use, which is what lets one
// registry serve every pipeline in a process.
func Registry() *node.Registry {
	once.Do(func() {
		registry = node.NewRegistry()
		for _, d := range Definitions() {
			registry.MustRegister(d)
		}
	})
	return registry
}

// Definitions lists every built-in node. Tests use it to build an isolated
// registry rather than sharing the process-wide one.
func Definitions() []node.Definition {
	return []node.Definition{
		value.Definition(),
		env.Definition(),
		text.FormatDefinition(),
		jsonnode.DecodeDefinition(),
		jsonnode.EncodeDefinition(),
		jsonnode.GetDefinition(),
		condition.Definition(),
		exec.CommandDefinition(),
		exec.Definition(),
		exec.StdoutDefinition(),
		exec.StderrDefinition(),
		exec.ExitCodeDefinition(),
		httpnode.BuildDefinition(),
		httpnode.RequestDefinition(),
		httpnode.BodyDefinition(),
		httpnode.StatusDefinition(),
		httpnode.HeaderDefinition(),
		httpnode.FromURLDefinition(),
		httpnode.WithHeaderDefinition(),
		httpnode.WithBodyDefinition(),
	}
}
