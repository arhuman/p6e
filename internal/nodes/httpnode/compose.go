package httpnode

import (
	"context"
	"net/textproto"
	"strings"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// The capabilities that build a request from upstream data rather than from a
// static with block.
const (
	FromURLName    = "http.from_url"
	WithHeaderName = "http.with_header"
	WithBodyName   = "http.with_body"
)

// fromURLConfig is an http.from_url step's with block.
type fromURLConfig struct {
	// Method defaults to GET.
	Method string `yaml:"method"`
}

// FromURLDefinition registers http.from_url: *types.Text in,
// *types.Request out.
//
// http.build fixes its URL in a with block, which is right when the URL is
// known while writing the pipeline and wrong the moment it comes from data. A
// request whose URL is computed starts here instead, and http.with_header and
// http.with_body add to it.
//
// The trade this makes is explicit and worth stating: http.build validates its
// URL at compile time, and a URL arriving on an edge cannot be validated until
// it arrives. Static type checking is unaffected; what is given up is static
// value checking of the URL, and only for the steps that opt into a computed
// one. The check still happens, as an invalid_input failure at the step that
// produced the bad URL rather than somewhere downstream.
func FromURLDefinition() node.Definition {
	return node.Definition{
		Name: FromURLName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c fromURLConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", FromURLName)
			}
			method := strings.ToUpper(c.Method)
			if method == "" {
				method = "GET"
			}

			return node.NewTypedNode(FromURLName,
				func(_ context.Context, _ *node.ExecutionContext, url *types.Text) node.Result[*types.Request] {
					checked, err := types.NewCheckedURL(FromURLName, url.Value)
					if err != nil {
						return node.Fail[*types.Request](err)
					}
					return node.Ok(&types.Request{Method: method, URL: checked})
				}), nil
		},
	}
}

// headerNameConfig is an http.with_header step's with block.
type headerNameConfig struct {
	// Name is the header to set. The value comes from the second input.
	Name string `yaml:"name"`
}

// WithHeaderDefinition registers http.with_header: (*types.Request,
// *types.Text) in, *types.Request out.
//
// The header name is configuration because it is known while writing the
// pipeline; the value is an input because it is not. An existing header of the
// same name is replaced.
//
// It produces a new request rather than modifying the one it received. Values
// on edges are immutable, and this node has two reasons to respect that: the
// request it consumes may fan out to a sibling step, and a retried attempt
// receives the same input as the first.
//
// Header names are canonicalised, so a pipeline that sets "content-type" over a
// request carrying "Content-Type" replaces it rather than producing both.
func WithHeaderDefinition() node.Definition {
	return node.Definition{
		Name: WithHeaderName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c headerNameConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", WithHeaderName)
			}
			if c.Name == "" {
				return nil, node.Errf(node.KindInvalidInput, "missing_name",
					"%s requires a name: the header to set", WithHeaderName)
			}
			name := textproto.CanonicalMIMEHeaderKey(c.Name)

			return node.NewTypedNode2(WithHeaderName,
				func(_ context.Context, _ *node.ExecutionContext, req *types.Request, value *types.Text) node.Result[*types.Request] {
					next := *req
					next.Headers = make(map[string]string, len(req.Headers)+1)
					for k, v := range req.Headers {
						next.Headers[textproto.CanonicalMIMEHeaderKey(k)] = v
					}
					next.Headers[name] = value.Value
					return node.Ok(&next)
				}), nil
		},
	}
}

// WithBodyDefinition registers http.with_body: (*types.Request, *types.Bytes)
// in, *types.Request out.
//
// It takes no configuration. A with block is rejected rather than ignored.
//
// The body is not wrapped, encoded, or given a content type: pairing it with
// http.with_header is how a pipeline says what the bytes are. Like
// http.with_header this produces a new request, and it shares the original's
// headers rather than copying them, which is safe because nothing mutates them.
func WithBodyDefinition() node.Definition {
	return node.Static(WithBodyName, node.NewTypedNode2(WithBodyName,
		func(_ context.Context, _ *node.ExecutionContext, req *types.Request, body *types.Bytes) node.Result[*types.Request] {
			next := *req
			next.Body = body.Value
			return node.Ok(&next)
		}))
}
