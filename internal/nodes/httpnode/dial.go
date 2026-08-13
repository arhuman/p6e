package httpnode

import (
	"fmt"
	"net"
	"syscall"
)

// The destination policy exists because a pipeline can build a request from
// data. http.from_url takes its URL off an edge, and that edge can carry a
// webhook body, so an outbound call is reachable by whoever sends the event.
// Without a policy the daemon is a confused deputy: it sits inside a network
// the caller cannot reach and will fetch whatever it is told to.
//
// The check runs in the dialer rather than on the URL string, and that is the
// whole point. A hostname is checked by resolving it, and between a check on
// the resolved address and the connect that follows, a second DNS answer can
// return a different address (DNS rebinding). net.Dialer.Control runs after
// resolution and immediately before connect, on the address actually being
// dialed, so there is no window between the two. It also covers every redirect
// hop for free, since each hop dials through the same transport.

// blockedDestination reports why an address is refused, or nil to allow it.
//
// It rejects the ranges that only ever mean "somewhere inside my own
// deployment": loopback, link-local (which is where 169.254.169.254 cloud
// metadata lives), private and unique-local, unspecified, and multicast. A
// public address that happens to be operated by the caller is not the threat
// this closes, and pretending otherwise would mean an allow-list of the entire
// internet.
func blockedDestination(ip net.IP) error {
	switch {
	case ip == nil:
		return fmt.Errorf("could not parse the destination address")
	case ip.IsLoopback():
		return fmt.Errorf("%s is a loopback address", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("%s is a link-local address, which is where cloud metadata lives", ip)
	case ip.IsPrivate():
		return fmt.Errorf("%s is a private address", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%s is the unspecified address", ip)
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("%s is a multicast address", ip)
	}
	return nil
}

// controlDestination is the net.Dialer.Control hook that enforces the policy.
// address is host:port with the host already resolved to a literal.
func controlDestination(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial %q: %w", address, err)
	}
	if err := blockedDestination(net.ParseIP(host)); err != nil {
		return fmt.Errorf("refusing to dial %s: %w, and this step did not set allow_private", address, err)
	}
	return nil
}
