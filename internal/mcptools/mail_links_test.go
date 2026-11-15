package mcptools

import (
	"context"
	maildomain "github.com/ThomasCrouzet/icloud-mcp/internal/mail"
	"strings"
	"testing"
	"time"
)

func TestProtectedMailLinks(t *testing.T) {
	d := mailToolsDeps(&mailToolsFakeService{})
	d.Links = newMailLinks(nil)
	m := maildomain.Message{Body: "Visit https://example.com/action?email=secret@example.com", Headers: map[string][]string{"List-Unsubscribe": {"<https://example.com/leave?email=secret@example.com>"}, "List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"}}}
	protectMessageLinks(&m, d.Links)
	if strings.Contains(m.Body, "secret@example.com") || len(m.Links) != 2 {
		t.Fatalf("unprotected links: %+v", m)
	}
	var post string
	for _, l := range m.Links {
		if l.Method == "POST" {
			post = l.Reference
		}
	}
	if post == "" {
		t.Fatal("missing one-click action")
	}
	item, ok := d.Links.take(post, time.Now())
	if !ok || !strings.Contains(item.URL, "secret@example.com") {
		t.Fatal("lost original URL")
	}
	if _, ok = d.Links.take(post, time.Now()); ok {
		t.Fatal("replayed action")
	}
	r, _ := executeMailLinkHandler(d)(context.Background(), mailToolsRequest(map[string]any{"reference": "missing", "confirmation": false}))
	if !r.IsError {
		t.Fatal("unconfirmed action accepted")
	}
}

func TestMailLinkDestinationPolicy(t *testing.T) {
	for _, raw := range []string{"http://example.com/x", "https://user:pass@example.com/x", "https://127.0.0.1/x", "https://example.com:444/x", "https://other.example/x"} {
		if _, err := mailLinkURL(raw, []string{"example.com"}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "::ffff:127.0.0.1", "100.64.0.1"} {
		if publicMailIP(raw) {
			t.Fatalf("accepted private IP %s", raw)
		}
	}
}

func TestMailLinkExpiryAndSpecialAddressSpace(t *testing.T) {
	s := newMailLinks(nil)
	ref := s.put("https://example.com/x?x=exact", "GET")
	if _, ok := s.take(ref, time.Now().Add(mailLinkTTL)); ok {
		t.Fatal("expired reference survived")
	}
	for _, raw := range []string{"0.1.2.3", "2001::1234", "64:ff9b:1::1234"} {
		if publicMailIP(raw) {
			t.Fatalf("special-purpose IP accepted: %s", raw)
		}
	}
}
