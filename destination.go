package main

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"
)

// errForbiddenDestination is the dial error for an address that
// forbidLocalDestinations or forbidPrivateDestinations rules out. Callers
// test for it with errors.Is to answer 403 rather than 502.
var errForbiddenDestination = errors.New("destination address not allowed")

var (
	// 0.0.0.0/8 is "this network" (RFC 791); Linux and macOS take a
	// connect to 0.0.0.0 as one to the local host.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	// 100.64.0.0/10 is the shared address space of carrier-grade NAT
	// (RFC 6598), which some VPNs also use for their private networks.
	sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")
)

// isLocalAddress reports whether a is this machine or its link: loopback,
// unspecified, link-local unicast (including 169.254.169.254, the cloud
// metadata address) or multicast. IPv4-mapped IPv6 addresses count as the
// IPv4 address they carry.
func isLocalAddress(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsUnspecified() || thisNetwork.Contains(a) ||
		a.IsLinkLocalUnicast() || a.IsMulticast()
}

// isPrivateAddress reports whether a is a private address (RFC 1918, or an
// IPv6 unique local address, RFC 4193) or in the shared address space.
func isPrivateAddress(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || sharedAddressSpace.Contains(a)
}

// checkDestination is a net.Dialer Control function. It runs after the name
// has been resolved, just before each connect, so it sees the address that is
// actually dialled: a hostname that resolves to 127.0.0.1 (DNS rebinding) is
// caught the same as a URL naming 127.0.0.1, and a name with both a refused
// and an allowed address still connects to the allowed one.
func (ph *ProxyHandler) checkDestination(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", errForbiddenDestination, address)
	}
	a := ap.Addr()
	if ph.config().ForbidLocalDestinations && isLocalAddress(a) ||
		ph.config().ForbidPrivateDestinations && isPrivateAddress(a) {
		return fmt.Errorf("%w: %s", errForbiddenDestination, a.Unmap())
	}
	return nil
}
