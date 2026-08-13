package jsonnode

import (
	"context"
	"encoding/json"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// EncodeName is the capability a pipeline references with "uses: json.encode".
const EncodeName = "json.encode"

// EncodeDefinition is the "json.encode" capability: JSONDocument to Bytes.
//
// It is json.decode's inverse, and it is what lets a pipeline produce a request
// body rather than only consume a response: nothing else in the catalogue turns
// a document back into bytes.
//
// It takes no configuration. A with block is still rejected, because a typo
// that is silently ignored produces a pipeline that checks clean and then does
// something other than what it says.
func EncodeDefinition() node.Definition {
	return node.Static(EncodeName, node.NewTypedNode(EncodeName, encode))
}

// encode marshals the document's root.
//
// A document that came from json.decode always encodes. One assembled by
// another node need not, so the failure is reported rather than assumed away:
// it is KindInvalidInput and not retryable, because the value is the problem
// and the same value fails the same way.
func encode(_ context.Context, _ *node.ExecutionContext, doc *types.Document) node.Result[*types.Bytes] {
	raw, err := json.Marshal(doc.Root)
	if err != nil {
		return node.Fail[*types.Bytes](node.Wrap(err, node.KindInvalidInput, "unencodable",
			"document cannot be encoded as JSON"))
	}
	return node.Ok(&types.Bytes{Value: raw})
}
