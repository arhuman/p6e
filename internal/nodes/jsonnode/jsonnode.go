// Package jsonnode provides the JSON nodes. It is named jsonnode rather than
// json so that the implementation can still refer to encoding/json.
//
// JSON is what this node does, never what the engine does: between native nodes
// an edge carries a typed Go reference, and nothing is ever serialized.
package jsonnode

import (
	"context"
	"encoding/json"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// DecodeName is the capability a pipeline references with "uses: json.decode".
const DecodeName = "json.decode"

// DecodeDefinition is the "json.decode" capability: Bytes to JSONDocument.
//
// It takes no configuration. A with block is still rejected, because a typo
// that is silently ignored produces a pipeline that checks clean and then does
// something other than what it says.
func DecodeDefinition() node.Definition {
	return node.Static(DecodeName, node.NewTypedNode(DecodeName, decode))
}

// decode unmarshals the payload into a Document whose Root is whatever the
// document was: an object, an array, or a scalar.
//
// Malformed input is KindInvalidInput and not retryable: the bytes are the
// problem, and the same bytes will fail the same way.
func decode(_ context.Context, _ *node.ExecutionContext, in *types.Bytes) node.Result[*types.Document] {
	var root any
	if err := json.Unmarshal(in.Value, &root); err != nil {
		return node.Fail[*types.Document](node.Wrap(err, node.KindInvalidInput, "malformed_json",
			"input is not valid JSON"))
	}
	return node.Ok(&types.Document{Root: root})
}
