package types_test

import (
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/node"
	"github.com/arhuman/p6e/internal/nodes/types"
)

// These URLs used to reach http.request and fail there. They cannot now: a
// Request carries a CheckedURL, so the failure moved from run time to
// construction. That move is the whole value of the type, so it is asserted
// here rather than assumed.
func TestCheckedURLRejectsUnusableURLs(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":              "",
		"malformed":          "http://%zz",
		"unsupported scheme": "ftp://example.invalid/f",
		"no scheme at all":   "example.invalid/f",
		"scheme but no host": "http://",
	} {
		t.Run(name, func(t *testing.T) {
			u, err := types.NewCheckedURL("test", raw)
			if err == nil {
				t.Fatalf("NewCheckedURL(%q) = %q, want a rejection", raw, u)
			}
			if err.Kind != node.KindInvalidInput {
				t.Errorf("Kind = %q, want %q: a bad URL is the pipeline's fault, not the world's",
					err.Kind, node.KindInvalidInput)
			}
			if err.Retryable {
				t.Error("no retry fixes a URL that cannot be parsed")
			}
			if u != (types.CheckedURL{}) {
				t.Errorf("a rejected URL returned %q, want the zero value", u)
			}
		})
	}
}

func TestCheckedURLAcceptsAndPreservesUsableURLs(t *testing.T) {
	for _, raw := range []string{
		"http://example.invalid",
		"https://example.invalid/path?q=1#frag",
		"https://user:pass@example.invalid:8443/deep/path",
		// Refused at dial time by the destination policy, not here: this
		// constructor checks the shape of a URL, never where it points.
		"http://169.254.169.254/latest/meta-data/",
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := types.NewCheckedURL("test", raw)
			if err != nil {
				t.Fatalf("NewCheckedURL(%q): %v", raw, err)
			}
			if u.String() != raw {
				t.Errorf("String() = %q, want the URL as given, unmodified", u.String())
			}
		})
	}
}

// The type's guarantee rests on this: outside the package there is no way to
// put a value in a CheckedURL except through the constructor. The zero value is
// the one exception, and it is empty rather than plausible, so every consumer
// rejects it.
func TestZeroCheckedURLIsEmpty(t *testing.T) {
	var zero types.CheckedURL
	if zero.String() != "" {
		t.Errorf("the zero CheckedURL is %q, want the empty string", zero.String())
	}
}

// The capability name reaches the message, because the same check runs at
// compile time for http.build and at execution for http.from_url, and the two
// report the same problem at different moments.
func TestCheckedURLErrorNamesTheCapability(t *testing.T) {
	_, err := types.NewCheckedURL("http.from_url", "")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if got := err.Error(); !strings.Contains(got, "http.from_url") {
		t.Errorf("error = %q, want it to name the capability that rejected the URL", got)
	}
}
