package middleware

import (
	"net/http/httptest"
	"testing"
)

func withTrustedProxies(t *testing.T, value string) {
	t.Helper()
	t.Setenv("OCTOPUS_TRUSTED_PROXIES", value)
	ReloadTrustedProxies()
	t.Cleanup(ReloadTrustedProxies)
}

func clientIPOf(remote, xff string) string {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return ClientIP(r)
}

func TestClientIP_TrustsNobodyByDefault(t *testing.T) {
	withTrustedProxies(t, "")
	if got := clientIPOf("10.0.0.2:5555", "203.0.113.9"); got != "10.0.0.2" {
		t.Errorf("unconfigured: got %s, want the peer", got)
	}
	if got := clientIPOf("127.0.0.1:5555", "203.0.113.9"); got != "127.0.0.1" {
		t.Errorf("unconfigured loopback: got %s, want the peer", got)
	}
}

func TestClientIP_ExplicitProxy(t *testing.T) {
	withTrustedProxies(t, "10.0.0.2")
	if got := clientIPOf("10.0.0.2:5555", "203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("trusted proxy: got %s", got)
	}
	if got := clientIPOf("10.0.0.3:5555", "203.0.113.9"); got != "10.0.0.3" {
		t.Errorf("untrusted peer: got %s", got)
	}
	if got := clientIPOf("10.0.0.2:5555", "1.1.1.1, 203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("spoofed left hop: got %s", got)
	}
}

func TestClientIP_GarbageInChainFallsBackToPeer(t *testing.T) {
	withTrustedProxies(t, "10.0.0.0/8")
	if got := clientIPOf("10.0.0.2:5555", "<script>, 10.0.0.9"); got != "10.0.0.2" {
		t.Errorf("garbage hop: got %q, want the peer", got)
	}
	if got := clientIPOf("10.0.0.2:5555", "admin"); got != "10.0.0.2" {
		t.Errorf("non-IP only hop: got %q, want the peer", got)
	}
}

func TestClientIP_PrivateKeyword(t *testing.T) {
	withTrustedProxies(t, "private")
	if got := clientIPOf("172.18.0.5:5555", "203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("private keyword: got %s", got)
	}
	if got := clientIPOf("198.51.100.7:5555", "203.0.113.9"); got != "198.51.100.7" {
		t.Errorf("public peer trusted under private: got %s", got)
	}
}
