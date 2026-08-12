package httpnode

import (
	"context"
	"strconv"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// AssertStatusName is the capability a pipeline references with
// "uses: http.assert_status".
const AssertStatusName = "http.assert_status"

// The range of status codes worth accepting in configuration. A code outside it
// is a typo, and catching "equals: 20" at compile time is worth more than
// letting a pipeline wait for a response that can never match.
const (
	minStatus = 100
	maxStatus = 599
)

// assertStatusConfig is an http.assert_status step's with block. Equals and the
// Min/Max pair are alternatives, and pointers so that "not set" stays distinct
// from a zero that was written deliberately.
type assertStatusConfig struct {
	Equals *int `yaml:"equals"`
	Min    *int `yaml:"min"`
	Max    *int `yaml:"max"`
}

// AssertStatusDefinition registers http.assert_status: *types.Response in, the
// same response out.
//
// A non-2xx status is data, and http.request is right not to fail on one: only
// the workflow knows whether a 404 is a problem. This is how a workflow says
// that it is, without giving up that principle anywhere else. The step is
// opt-in, and a pipeline that wants to inspect a 404 simply does not add one.
//
// The response passes through, so the steps that read it can depend on this one
// and will not run at all if the status is wrong.
//
// The with block takes either an exact code or a range:
//
//	with: {equals: 200}
//	with: {min: 200, max: 299}
//
// A range open at one end is allowed: min alone accepts anything at or above
// it, max alone anything at or below.
func AssertStatusDefinition() node.Definition {
	return node.Definition{
		Name: AssertStatusName,
		New: func(cfg node.Config) (node.RuntimeNode, error) {
			var c assertStatusConfig
			if err := cfg.Decode(&c); err != nil {
				return nil, node.Wrap(err, node.KindInvalidInput, "bad_config",
					"invalid %s configuration", AssertStatusName)
			}
			low, high, err := c.bounds()
			if err != nil {
				return nil, err
			}

			return node.NewTypedNode(AssertStatusName,
				func(_ context.Context, _ *node.ExecutionContext, resp *types.Response) node.Result[*types.Response] {
					if resp.Status < low || resp.Status > high {
						// Permanent: retrying this step would re-test the same
						// response. Retrying the call is http.request's policy.
						return node.Fail[*types.Response](node.Errf(node.KindPermanent,
							"unexpected_status", "response status is %d, want %s",
							resp.Status, describe(low, high)))
					}
					return node.Ok(resp)
				}), nil
		},
	}
}

// bounds resolves the configuration into the closed interval to accept.
func (c assertStatusConfig) bounds() (int, int, error) {
	hasRange := c.Min != nil || c.Max != nil

	switch {
	case c.Equals != nil && hasRange:
		return 0, 0, node.Errf(node.KindInvalidInput, "ambiguous_test",
			"%s takes equals or a min/max range, not both", AssertStatusName)
	case c.Equals == nil && !hasRange:
		return 0, 0, node.Errf(node.KindInvalidInput, "missing_test",
			"%s requires equals, or min and max, naming the status to accept", AssertStatusName)
	case c.Equals != nil:
		if err := inRange(*c.Equals); err != nil {
			return 0, 0, err
		}
		return *c.Equals, *c.Equals, nil
	}

	low, high := minStatus, maxStatus
	if c.Min != nil {
		if err := inRange(*c.Min); err != nil {
			return 0, 0, err
		}
		low = *c.Min
	}
	if c.Max != nil {
		if err := inRange(*c.Max); err != nil {
			return 0, 0, err
		}
		high = *c.Max
	}
	if low > high {
		return 0, 0, node.Errf(node.KindInvalidInput, "bad_range",
			"%s has min %d above max %d, which accepts nothing", AssertStatusName, low, high)
	}
	return low, high, nil
}

func inRange(status int) error {
	if status < minStatus || status > maxStatus {
		return node.Errf(node.KindInvalidInput, "bad_status",
			"%d is not an HTTP status code, which run from %d to %d",
			status, minStatus, maxStatus)
	}
	return nil
}

// describe renders the accepted interval the way the configuration wrote it.
func describe(low, high int) string {
	switch {
	case low == high:
		return strconv.Itoa(low)
	case low == minStatus:
		return "at most " + strconv.Itoa(high)
	case high == maxStatus:
		return "at least " + strconv.Itoa(low)
	default:
		return strconv.Itoa(low) + " to " + strconv.Itoa(high)
	}
}
