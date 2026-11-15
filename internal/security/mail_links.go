package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// NewMailLinkTransport is isolated from all credential-bearing domain transports.
func NewMailLinkTransport(hosts []string) *http.Transport {
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16384, DialContext: newMailLinkDialer(hosts, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)}
}
func newMailLinkDialer(hosts []string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial DialContextFunc) DialContextFunc {
	allowed := append([]string(nil), hosts...)
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || network != "tcp" {
			return nil, fmt.Errorf("invalid link destination")
		}
		permitted := false
		for _, candidate := range allowed {
			if strings.EqualFold(candidate, host) {
				permitted = true
				break
			}
		}
		if !permitted || lookup == nil || dial == nil {
			return nil, fmt.Errorf("link destination denied")
		}
		ips, err := lookup(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("link destination lookup failed")
		}
		for _, ip := range ips {
			if !IsPublicMailLinkIP(ip.String()) {
				return nil, fmt.Errorf("non-public link destination denied")
			}
		}
		// The policy boundary passes only a validated numeric address to the injected dialer.
		// TLS verification in http.Transport still uses the original request hostname.
		return dial(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
	}
}

// IsPublicMailLinkIP excludes local and special-purpose address space.
func IsPublicMailLinkIP(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "2001::/23", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
