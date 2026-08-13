// Package httpnode makes HTTP calls from a pipeline. It is named httpnode
// rather than http so that its own code can still refer to net/http.
//
// A non-2xx status is data, not a failure: whether a 404 is a problem is the
// workflow's decision, so it arrives as Response.Status on a successful edge.
// A node error means no response was obtained at all.
package httpnode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// RequestName and BodyName are the capabilities a pipeline references with
// "uses:".
const (
	RequestName = "http.request"
	BodyName    = "http.body"
)

const (
	defaultTimeout      = 30 * time.Second
	defaultMaxBodyBytes = 10 << 20
	// maxRedirects matches net/http's own default. It is named here because
	// setting CheckRedirect replaces that default rather than adding to it.
	maxRedirects = 10
)

// There are exactly two transports, one per destination policy, and they are
// package-level so that connection pooling, keep-alive and TLS session reuse
// are shared across every step that made the same choice. A transport per step
// would give each its own pool and reconnect constantly; a single transport
// could not carry two policies, because the policy lives in the dialer.
//
// MaxIdleConnsPerHost is raised from the standard library's 2 because a
// pipeline typically fans out against a single host, and two idle connections
// would make it reconnect on nearly every step.
var (
	// guardedTransport refuses to dial an internal address. It is the default.
	guardedTransport = newTransport(controlDestination)
	// openTransport is what a step gets when it sets allow_private, which is
	// the deliberate act of pointing a pipeline at something inside the
	// deployment.
	openTransport = newTransport(nil)
)

func newTransport(control func(network, address string, c syscall.RawConn) error) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   control,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// checkRedirect bounds redirect following and re-checks the scheme on every
// hop. The address policy needs no repeating here: each hop dials through the
// same transport, so its Control hook has already run.
//
// Without this a redirect is a hole in http.build's compile-time URL check: the
// configured URL is validated while the location a server sends back is not,
// so a trusted host could walk a pipeline to any scheme it liked.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if scheme := req.URL.Scheme; scheme != "http" && scheme != "https" {
		return fmt.Errorf("refusing to follow a redirect to scheme %q", scheme)
	}
	return nil
}

// requestConfig is an http.request step's with block.
type requestConfig struct {
	// Timeout bounds one call end to end, connection through body read, as a
	// duration string such as "10s". It defaults to 30s.
	Timeout string `yaml:"timeout"`
	// MaxBodyBytes caps the response body a step will hold, so one oversized
	// response cannot exhaust the process. It defaults to 10 MiB.
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
	// AllowPrivate lets this step reach loopback, link-local, private and
	// multicast addresses, which are refused by default.
	//
	// It is off by default because a request can be built from data: a URL that
	// arrived on an edge from a webhook body would otherwise let whoever sent
	// the event reach anything the daemon can route to, cloud metadata
	// included. Turn it on for a step that is meant to call a service inside
	// the deployment, which is a decision about one step rather than about the
	// process.
	AllowPrivate bool `yaml:"allow_private"`
}

// RequestDefinition registers the http.request capability: *types.Request in,
// *types.Response out.
//
// A request with no method is sent as GET. Configuration is decoded and
// validated here, at compile time, and the client is built here too: it is
// shared by every execution of the step, which is what makes connection reuse
// possible.
func RequestDefinition() node.Definition {
	return node.Definition{
		Name: RequestName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c requestConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", RequestName)
			}

			timeout, err := parseTimeout(c.Timeout)
			if err != nil {
				return nil, err
			}
			maxBody, err := maxBodyBytes(c.MaxBodyBytes)
			if err != nil {
				return nil, err
			}

			transport := guardedTransport
			if c.AllowPrivate {
				transport = openTransport
			}
			client := &http.Client{
				Transport:     transport,
				Timeout:       timeout,
				CheckRedirect: checkRedirect,
			}
			return node.NewTypedNode(RequestName,
				func(ctx context.Context, _ *node.ExecutionContext, req *types.Request) node.Result[*types.Response] {
					return call(ctx, client, maxBody, req)
				}), nil
		},
	}
}

// BodyDefinition registers the http.body capability: *types.Response in,
// *types.Bytes out.
//
// It is a one-line extractor and exists because the engine performs no implicit
// conversion between types. A pipeline that feeds an HTTP response into
// json.decode, which takes Bytes, must say so with a step, so the graph
// describes the whole computation rather than hiding part of it in the engine.
//
// The body is shared, not copied: the Bytes it produces points at the same
// backing array as the response, which is safe because values are immutable.
func BodyDefinition() node.Definition {
	return node.Static(BodyName, bodyNode)
}

var bodyNode = node.NewTypedNode(BodyName,
	func(_ context.Context, _ *node.ExecutionContext, resp *types.Response) node.Result[*types.Bytes] {
		return node.Ok(&types.Bytes{Value: resp.Body})
	})

func parseTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, node.Wrap(err, node.KindInvalidInput, "bad_config",
			"timeout %q is not a duration such as \"10s\"", s)
	}
	if d <= 0 {
		return 0, node.Errf(node.KindInvalidInput, "bad_config",
			"timeout must be positive, got %q", s)
	}
	return d, nil
}

func maxBodyBytes(n int64) (int64, error) {
	if n == 0 {
		return defaultMaxBodyBytes, nil
	}
	if n < 0 {
		return 0, node.Errf(node.KindInvalidInput, "bad_config",
			"max_body_bytes must be positive, got %d", n)
	}
	return n, nil
}

func call(ctx context.Context, client *http.Client, maxBody int64, req *types.Request) node.Result[*types.Response] {
	httpReq, nerr := build(ctx, req)
	if nerr != nil {
		return node.Fail[*types.Response](nerr)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return node.Fail[*types.Response](transportError(ctx, err))
	}
	defer resp.Body.Close()

	// N is the limit plus one so that a body exactly at the limit still reads
	// and one byte over is detectable: io.LimitReader would truncate in silence.
	limited := &io.LimitedReader{R: resp.Body, N: maxBody + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		if ctx.Err() != nil {
			return node.Fail[*types.Response](node.Normalize(ctx.Err(), "cancelled"))
		}
		return node.Fail[*types.Response](node.Wrap(err, node.KindTransient, "body_read",
			"reading the response body from %s failed", req.URL))
	}
	if limited.N == 0 {
		return node.Fail[*types.Response](node.Errf(node.KindPermanent, "body_too_large",
			"response body from %s exceeds the configured limit of %d bytes", req.URL, maxBody))
	}

	return node.Ok(&types.Response{Status: resp.StatusCode, Headers: resp.Header, Body: body})
}

// build turns the request payload into an *http.Request, rejecting everything
// that no retry could fix. The scheme is checked here rather than left to the
// transport, which reports an unsupported one only once the call is under way.
func build(ctx context.Context, req *types.Request) (*http.Request, *node.NodeError) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, node.Wrap(err, node.KindPermanent, "invalid_request",
			"cannot build a %s request for %q", method, req.URL)
	}
	if scheme := httpReq.URL.Scheme; scheme != "http" && scheme != "https" {
		return nil, node.Errf(node.KindPermanent, "unsupported_scheme",
			"scheme %q in %q is not supported, want http or https", scheme, req.URL)
	}
	for name, value := range req.Headers {
		httpReq.Header.Set(name, value)
	}
	return httpReq, nil
}

// transportError classifies a call that produced no response. The context is
// consulted first because a client timeout also reports DeadlineExceeded, and
// an abandoned execution and an overrun call mean different things to policy.
func transportError(ctx context.Context, err error) *node.NodeError {
	if ctx.Err() != nil {
		return node.Normalize(ctx.Err(), "cancelled")
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return node.Wrap(err, node.KindTransient, "timeout", "request timed out")
	}
	return node.Wrap(err, node.KindTransient, "transport", "request failed")
}
