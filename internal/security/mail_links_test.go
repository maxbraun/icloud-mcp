package security

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

func TestMailLinkDialerValidatesAndPins(t *testing.T) {
	calls := 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		if network != "tcp" || address != "1.1.1.1:443" {
			t.Fatalf("unpinned dial: %s %s", network, address)
		}
		return nil, nil
	}
	wrapped := newMailLinkDialer([]string{"example.com"}, lookup, dial)
	if _, err := wrapped(t.Context(), "tcp", "other.example:443"); err == nil || calls != 0 {
		t.Fatal("host policy bypassed")
	}
	if _, err := wrapped(t.Context(), "tcp", "example.com:443"); err != nil || calls != 1 {
		t.Fatalf("public pinned destination failed: %v", err)
	}
	privateLookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := newMailLinkDialer([]string{"example.com"}, privateLookup, dial)(t.Context(), "tcp", "example.com:443"); err == nil || calls != 1 {
		t.Fatal("mixed DNS answer bypassed policy")
	}
}
