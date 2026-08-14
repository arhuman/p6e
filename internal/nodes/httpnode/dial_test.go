package httpnode

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/arhuman/p6e/internal/nodes/types"
)

func TestBlockedDestinationClassifiesAddresses(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"127.1.2.3",       // the rest of 127/8, which is still loopback
		"::1",             // loopback, v6
		"169.254.169.254", // cloud metadata, the reason this exists
		"169.254.0.1",     // the rest of link-local
		"fe80::1",         // link-local, v6
		"10.1.2.3",        // RFC 1918
		"172.16.0.1",      // RFC 1918
		"192.168.1.1",     // RFC 1918
		"fc00::1",         // unique-local, v6
		"0.0.0.0",         // unspecified
		"::",              // unspecified, v6
		"224.0.0.1",       // multicast
		"ff02::1",         // multicast, v6
	}
	for _, addr := range blocked {
		if err := blockedDestination(net.ParseIP(addr)); err == nil {
			t.Errorf("blockedDestination(%s) = nil, want an error", addr)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"93.184.216.34",
		"2606:2800:220:1:248:1893:25c8:1946",
	}
	for _, addr := range allowed {
		if err := blockedDestination(net.ParseIP(addr)); err != nil {
			t.Errorf("blockedDestination(%s) = %v, want nil", addr, err)
		}
	}

	// An address that will not parse must fail closed rather than be treated as
	// public, which is what a nil net.IP would silently become.
	if err := blockedDestination(nil); err == nil {
		t.Error("blockedDestination(nil) = nil, want an error: an unparseable address must fail closed")
	}
}

func TestControlDestinationRefusesInternalAddress(t *testing.T) {
	if err := controlDestination("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("expected the cloud metadata address to be refused")
	}
	if err := controlDestination("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("expected a public address to be allowed, got %v", err)
	}
	if err := controlDestination("tcp", "not-an-address", nil); err == nil {
		t.Error("expected an unsplittable address to be refused")
	}
}

// The policy is enforced at dial time rather than on the URL string, so a
// hostname that resolves to an internal address is refused just as a literal
// one is. This is what makes DNS rebinding a non-issue: the check runs on the
// address actually being connected to.
func TestRequestRefusesInternalDestinationByDefault(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	err := fails(t, request(t, t.Context(), "", &types.Request{URL: checked(t, srv.URL)}))

	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error = %q, want it to name the refused address class", err.Error())
	}
}

// The same call succeeds once the step says it means to reach inside the
// deployment, which is the whole point of the opt-in.
func TestRequestReachesInternalDestinationWhenAllowed(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	succeeds(t, request(t, t.Context(), allowPrivate, &types.Request{URL: checked(t, srv.URL)}))
}

func TestCheckRedirectBoundsAndChecksScheme(t *testing.T) {
	httpsHop := &http.Request{URL: mustParseURL(t, "https://example.com/next")}

	if err := checkRedirect(httpsHop, nil); err != nil {
		t.Errorf("a first https hop should be followed, got %v", err)
	}

	// A redirect is otherwise a hole in http.build's compile-time URL check: the
	// configured URL is validated and the location a server returns is not.
	fileHop := &http.Request{URL: mustParseURL(t, "file:///etc/passwd")}
	if err := checkRedirect(fileHop, nil); err == nil {
		t.Error("expected a redirect to a non-http scheme to be refused")
	}

	tooMany := make([]*http.Request, maxRedirects)
	if err := checkRedirect(httpsHop, tooMany); err == nil {
		t.Errorf("expected the chain to stop after %d redirects", maxRedirects)
	}
}

// Coverage note: that a redirect hop is also address-checked follows from the
// hop being dialled through the same transport, whose Control hook
// TestRequestRefusesInternalDestinationByDefault exercises. Asserting it
// end to end would need a public origin server redirecting inward, which no
// offline test can stand up: an origin on loopback is only reachable with
// allow_private, and that selects the transport with no policy at all.

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}
