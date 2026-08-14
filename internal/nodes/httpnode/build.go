package httpnode

import (
	"context"
	"strings"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// BuildName is the capability that builds a Request from a with block.
const BuildName = "http.build"

// buildConfig is an http.build step's with block.
type buildConfig struct {
	// Method defaults to GET.
	Method string `yaml:"method"`
	// URL is required and must be absolute, with an http or https scheme.
	URL string `yaml:"url"`
	// Headers are sent as given, with no implicit additions.
	Headers map[string]string `yaml:"headers"`
	// Body is the request body.
	Body string `yaml:"body"`
}

// BuildDefinition registers the http.build capability: a source producing the
// *types.Request its with block declares.
//
// It exists because the engine performs no implicit conversion. http.request
// consumes a Request, so a pipeline that calls a fixed URL says so in two
// steps: one that describes the call, one that makes it. A request built by a
// future node and a configured one then reach http.request the same way.
//
// The URL is validated here, at compile time, so a typo fails p6e check rather
// than a production run.
func BuildDefinition() node.Definition {
	return node.Definition{
		Name: BuildName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c buildConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", BuildName)
			}
			checked, err := types.NewCheckedURL(BuildName, c.URL)
			if err != nil {
				return nil, err
			}

			method := strings.ToUpper(c.Method)
			if method == "" {
				method = "GET"
			}

			request := &types.Request{
				Method:  method,
				URL:     checked,
				Headers: c.Headers,
				Body:    []byte(c.Body),
			}
			return node.NewSource(BuildName,
				func(context.Context, *node.ExecutionContext) node.Result[*types.Request] {
					return node.Ok(request)
				}), nil
		},
	}
}
