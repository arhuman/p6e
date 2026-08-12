// Package text builds strings from pipeline data.
//
// It is what a pipeline uses instead of interpolation in a with block. An
// expression DSL would move type checking to run time, which is the one thing
// this engine exists to avoid; a node with one typed input port per placeholder
// keeps the same convenience under the compiler.
package text

import (
	"context"
	"strings"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// FormatName is the capability a pipeline references with "uses: text.format".
const FormatName = "text.format"

// openTag and closeTag delimit a placeholder in a template.
const (
	openTag  = "{{"
	closeTag = "}}"
)

// config is a text.format step's with block.
type config struct {
	// Template is literal text with {{name}} placeholders. Each distinct name
	// becomes an input port.
	Template string `yaml:"template"`
}

// FormatDefinition is the "text.format" capability: one Text input port per
// placeholder, Text out.
//
// The template is parsed at compile time and its placeholders become the node's
// input ports, in order of first appearance:
//
//	message:
//	  uses: text.format
//	  with:
//	    template: "{{repo}} released {{tag}}"
//	  needs:
//	    repo: repo_name
//	    tag: release_tag
//
// This is interpolation that the compiler checks. A placeholder with no step
// bound to it is a compile error, as is a needs entry naming a placeholder the
// template does not contain, because those are the ordinary named-binding
// checks applied to ports that happen to come from a string.
//
// Every port is a Text, so a template with more than one placeholder cannot be
// bound positionally: ADR 0009 requires the mapping form exactly when ports
// share a type, and here they always do. That is the intended reading of a
// template, where argument order is not something a reader should have to
// reconstruct.
//
// A name repeated in the template is one port, used at each occurrence.
func FormatDefinition() node.Definition {
	return node.Definition{Name: FormatName, New: newFormat}
}

func newFormat(cfg node.Config) (node.RuntimeNode, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
			"invalid %s configuration", FormatName)
	}
	if c.Template == "" {
		return nil, node.Errf(node.KindInvalidInput, "missing_template",
			"%s requires a template", FormatName)
	}

	segments, ports, err := parse(c.Template)
	if err != nil {
		return nil, err
	}

	return node.NewTypedNodeN(FormatName, ports,
		func(_ context.Context, _ *node.ExecutionContext, in []*types.Text) node.Result[*types.Text] {
			var sb strings.Builder
			for _, s := range segments {
				if s.port < 0 {
					sb.WriteString(s.literal)
					continue
				}
				sb.WriteString(in[s.port].Value)
			}
			return node.Ok(&types.Text{Value: sb.String()})
		}), nil
}

// segment is one piece of a parsed template: either literal text, or the port
// whose value belongs at this position.
type segment struct {
	literal string
	port    int // -1 for a literal
}

// parse splits a template into segments and collects its port names in order of
// first appearance. It rejects the forms that cannot mean anything, so a
// malformed template fails p6e check rather than producing a surprising string.
func parse(template string) ([]segment, []string, error) {
	var (
		segments []segment
		ports    []string
		index    = map[string]int{}
		rest     = template
	)

	for {
		start := strings.Index(rest, openTag)
		if start < 0 {
			if rest != "" {
				segments = append(segments, segment{literal: rest, port: -1})
			}
			return segments, ports, nil
		}
		if start > 0 {
			segments = append(segments, segment{literal: rest[:start], port: -1})
		}

		rest = rest[start+len(openTag):]
		end := strings.Index(rest, closeTag)
		if end < 0 {
			return nil, nil, node.Errf(node.KindInvalidInput, "bad_template",
				"%s has an unclosed %s in template %q", FormatName, openTag, template)
		}

		name := strings.TrimSpace(rest[:end])
		if err := validName(name, template); err != nil {
			return nil, nil, err
		}
		port, seen := index[name]
		if !seen {
			port = len(ports)
			index[name] = port
			ports = append(ports, name)
		}
		segments = append(segments, segment{port: port})
		rest = rest[end+len(closeTag):]
	}
}

// validName rejects placeholder names that could not be written as a needs key,
// or that would read as a mistake.
func validName(name, template string) error {
	if name == "" {
		return node.Errf(node.KindInvalidInput, "bad_template",
			"%s has an empty placeholder in template %q", FormatName, template)
	}
	if strings.ContainsAny(name, " \t\n") {
		return node.Errf(node.KindInvalidInput, "bad_template",
			"%s placeholder %q contains whitespace, which cannot be a port name", FormatName, name)
	}
	if strings.Contains(name, openTag) {
		return node.Errf(node.KindInvalidInput, "bad_template",
			"%s placeholder %q contains %s, so a placeholder was left unclosed",
			FormatName, name, openTag)
	}
	return nil
}
