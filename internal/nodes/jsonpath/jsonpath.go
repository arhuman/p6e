// Package jsonpath resolves the dot-separated paths a step uses to address a
// value inside a decoded document.
//
// It lives apart from the nodes that use it so that what a path means has one
// definition. A pipeline that tests a path with condition and one that reads it
// with json.get must agree about the same document, and they only do if they
// share this code rather than each carrying a copy of it.
package jsonpath

import (
	"slices"
	"strings"

	"github.com/arhuman/p6e/internal/node"
)

// Parse splits a dot-separated path and rejects the forms that address nothing.
// It runs at compile time, so a malformed path never reaches a run.
//
// capability names the node in the error, since the same mistake reads
// differently depending on which step made it.
func Parse(capability, raw string) ([]string, error) {
	if raw == "" {
		return nil, node.Errf(node.KindInvalidInput, "missing_path",
			"%s requires a path such as \"user.name\"", capability)
	}
	segments := strings.Split(raw, ".")
	if slices.Contains(segments, "") {
		return nil, node.Errf(node.KindInvalidInput, "bad_path",
			"path %q has an empty segment", raw)
	}
	return segments, nil
}

// Lookup walks a document by map key. A key that is absent, or a value part way
// down that is not an object, means the path does not exist. Neither is an
// error here: not existing is one of the answers a caller may want, and it is
// the caller that decides what to do about it.
func Lookup(root any, path []string) (any, bool) {
	current := root
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// Describe names the JSON kind of a value, for error messages that have to say
// what was found where something else was expected.
func Describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case float64, int, int64, uint64:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return "an unsupported value"
	}
}
