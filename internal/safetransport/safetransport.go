// Package safetransport provides SSRF-hardened HTTP plumbing for outbound
// calls to developer-supplied webhook URLs. A developer can register an
// arbitrary target URL, so both the registration verification probe and the
// production webhook sender must refuse to dial internal network addresses
// (RFC-1918, loopback, link-local incl. the cloud metadata endpoint, etc.).
//
// The guard runs as a net.Dialer.Control hook, which fires with the
// post-DNS-resolution IP on every dial attempt. That placement also defends
// against DNS rebinding: even if a hostname resolves to a public IP at
// registration time and a private IP at delivery time, the private dial is
// refused at connect time rather than at validation time.
package safetransport

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// cgnat is the RFC-6598 carrier-grade NAT range (100.64.0.0/10). net.IP's
// IsPrivate does not cover it, but it is routable only inside provider
// networks, so we treat it as blocked.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// blocked reports whether ip is in a range we refuse to dial.
func blocked(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (incl. 169.254.169.254), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsPrivate() || // 10/8, 172.16/12, 192.168/16, fc00::/7
		cgnat.Contains(ip)
}

// control is the net.Dialer.Control hook enforcing the blocklist.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("safetransport: bad dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("safetransport: cannot parse dial IP %q", host)
	}
	if blocked(ip) {
		return fmt.Errorf("safetransport: refusing to dial private/loopback address %s", ip)
	}
	return nil
}

func dialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
}

// Guard installs the SSRF dial guard onto t and returns it. t must not be
// nil. The guard replaces t.DialContext, so callers that need a custom dialer
// should compose around Guard rather than overwrite DialContext afterwards.
func Guard(t *http.Transport) *http.Transport {
	t.DialContext = dialer().DialContext
	return t
}

// Client returns an http.Client with an SSRF-guarded transport and the given
// timeout. Used by the registration verification probe.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Guard(http.DefaultTransport.(*http.Transport).Clone()),
	}
}
