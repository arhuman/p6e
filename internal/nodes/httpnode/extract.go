package httpnode

import (
	"context"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// The capabilities that read one field of an HTTPResponse onto an edge.
const (
	StatusName = "http.status"
	HeaderName = "http.header"
)

// StatusDefinition registers http.status: *types.Response in, *types.Int out.
//
// A non-2xx status is data, not a failure, and this is what makes that claim
// usable: without it the status never reaches an edge and no workflow can act
// on it. Whether a 404 is a problem stays the workflow's decision.
//
// It takes no configuration. A with block is rejected rather than ignored.
func StatusDefinition() node.Definition {
	return node.Static(StatusName, node.NewTypedNode(StatusName,
		func(_ context.Context, _ *node.ExecutionContext, resp *types.Response) node.Result[*types.Int] {
			return node.Ok(&types.Int{Value: int64(resp.Status)})
		}))
}

// headerConfig is an http.header step's with block.
type headerConfig struct {
	// Name is the header to read, matched case insensitively.
	Name string `yaml:"name"`
	// Default is what to produce when the response carries no such header.
	// Absent means an absent header is an error.
	Default *string `yaml:"default"`
}

// HeaderDefinition registers http.header: *types.Response in, *types.Text out.
//
// The header named in the with block is read case insensitively. A header sent
// more than once yields its first value; a pipeline needing all of them wants a
// different node rather than a surprising one.
//
// A missing header is an error unless the with block declares a default. There
// is no optional Text, so producing "" for an absent header would be
// indistinguishable from a header that is genuinely empty, and that value would
// then flow silently into a URL or a request body. Declaring the default makes
// the intent explicit and keeps the absent case visible:
//
//	with: {name: Retry-After, default: "0"}
//
// The error is permanent: the same response will not grow the header on a
// retry.
func HeaderDefinition() node.Definition {
	return node.Definition{
		Name: HeaderName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c headerConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", HeaderName)
			}
			if c.Name == "" {
				return nil, node.Errf(node.KindInvalidInput, "missing_name",
					"%s requires a name: the header to read", HeaderName)
			}

			name, fallback := c.Name, c.Default
			return node.NewTypedNode(HeaderName,
				func(_ context.Context, _ *node.ExecutionContext, resp *types.Response) node.Result[*types.Text] {
					// Values distinguishes an absent header from one present and
					// empty, which Get cannot: both come back as "".
					if values := resp.Headers.Values(name); len(values) > 0 {
						return node.Ok(&types.Text{Value: values[0]})
					}
					if fallback != nil {
						return node.Ok(&types.Text{Value: *fallback})
					}
					return node.Fail[*types.Text](node.Errf(node.KindPermanent, "header_absent",
						"response carries no %q header, and no default is configured", name))
				}), nil
		},
	}
}
