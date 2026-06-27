package webfetch

import (
	"fmt"
	"net"
	"net/url"
)

// checkSSRF is the start of the §6 security floor: refuse a URL whose host is a
// literal IP in a loopback / link-local / metadata range. It deliberately does
// NOT block private ranges — for the sandbox venue those are the legitimate
// internal network (the mirror, internal docs). DNS-resolving checks for the
// controller venue land in 6b, where Popo's broad network access makes them
// load-bearing.
func checkSSRF(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q (want http/https)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url has no host")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return nil // a hostname — the literal-IP floor doesn't apply (6b resolves)
	}
	if blockedIP(ip) {
		return fmt.Errorf("refusing to fetch blocked address %s", u.Hostname())
	}
	return nil
}

// blockedIP covers loopback (127/8, ::1), link-local (169.254/16 incl. the
// 169.254.169.254 metadata address, fe80::/10), and the unspecified address.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}
