package netpolicy

import "net"

// sharedOverlayIPv4 is RFC 6598 shared address space. It is not globally
// routable and is commonly used by explicitly selected overlay/VPN networks,
// but Go intentionally does not classify it as IP.IsPrivate.
var sharedOverlayIPv4 = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// TrustedPrivateEndpointIP reports whether a literal IP is eligible for the
// explicitly trust-pinned private-network bootstrap. This is a reachability
// policy only: relay trust still comes exclusively from the transferred TLS
// identity and never from an address classification.
func TrustedPrivateEndpointIP(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback() || sharedOverlayIPv4.Contains(ip))
}
