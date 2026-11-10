package netguard

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPolicyCheck_Egress(t *testing.T) {
	p := Policy{}
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.5", "172.16.3.4", "192.168.1.1",
		"100.64.0.1",
		"169.254.169.254",
		"169.254.1.1",
		"fd00:ec2::254",
		"100.100.100.200",
		"0.0.0.0", "::",
		"224.0.0.1", "255.255.255.255", "ff02::1",
		"fe80::1", "fc00::1", "fd12:3456::1",
		"::ffff:127.0.0.1",
		"::ffff:169.254.169.254",
		"64:ff9b::a9fe:a9fe",
		"64:ff9b::7f00:1",
		"2002:a9fe:a9fe::1",
	}
	for _, s := range blocked {
		if err := p.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: allowed, want blocked", s)
		}
	}
	allowed := []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111", "64:ff9b::101:101"}
	for _, s := range allowed {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: %v, want allowed", s, err)
		}
	}
}

func TestPolicyCheck_AllowlistAndInternal(t *testing.T) {
	p := Policy{Allow: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("169.254.0.0/16")}}
	if err := p.Check(netip.MustParseAddr("10.20.5.5")); err != nil {
		t.Errorf("allowlisted private address blocked: %v", err)
	}
	if err := p.Check(netip.MustParseAddr("10.21.5.5")); err == nil {
		t.Error("address outside the allowlist admitted")
	}
	if err := p.Check(netip.MustParseAddr("169.254.169.254")); err == nil {
		t.Error("metadata endpoint admitted through an allowlist")
	}

	internal := Policy{AllowPrivate: true}
	for _, s := range []string{"10.96.0.10", "127.0.0.1", "fd00::5", "100.64.1.1"} {
		if err := internal.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("internal policy blocked %s: %v", s, err)
		}
	}
	for _, s := range []string{"169.254.169.254", "169.254.10.10", "fe80::1", "fd00:ec2::254"} {
		if err := internal.Check(netip.MustParseAddr(s)); err == nil {
			t.Errorf("internal policy admitted %s", s)
		}
	}
}

func TestEgressPolicy_Env(t *testing.T) {
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "10.9.0.0/16, 192.168.7.7, not-a-cidr")
	p := EgressPolicy()
	if p.Disabled || len(p.Allow) != 2 {
		t.Fatalf("policy = %+v, want two allowed prefixes", p)
	}
	if p.Check(netip.MustParseAddr("192.168.7.7")) != nil || p.Check(netip.MustParseAddr("192.168.7.8")) == nil {
		t.Error("single-IP allowlist entry is not exact")
	}
	t.Setenv("OCTOPUS_EGRESS_GUARD", "off")
	if !EgressPolicy().Disabled {
		t.Error("OCTOPUS_EGRESS_GUARD=off ignored")
	}
}

func TestTransport_BlocksLoopbackAtConnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")

	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	c := NewEgress().Client(5 * time.Second)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	for _, u := range []string{srv.URL, "http://localhost:" + port} {
		if _, err := c.Get(u); !errors.Is(err, ErrBlocked) {
			t.Errorf("GET %s: err=%v, want ErrBlocked", u, err)
		}
	}

	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "127.0.0.0/8,::1")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowlisted loopback: %v", err)
	}
	_ = resp.Body.Close()
}

func TestTransport_BlocksRedirectToInternal(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "secret") }))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "127.0.0.2", 1), http.StatusFound)
	}))
	defer front.Close()

	g := NewWithPolicy(Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}, nil)
	_, err := g.Client(5 * time.Second).Get(front.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("redirect into 127.0.0.2: err=%v, want ErrBlocked", err)
	}
}

func TestTransport_ProxyChecksTarget(t *testing.T) {
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = append(proxied, r.URL.String())
		_, _ = io.WriteString(w, "via proxy")
	}))
	defer proxy.Close()

	pu, _ := url.Parse(proxy.URL)
	// http.ProxyFromEnvironment caches the environment once per process, so t.Setenv cannot drive
	// it.
	c := NewWithPolicy(Policy{}, http.ProxyURL(pu)).Client(5 * time.Second)

	if _, err := c.Get("http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrBlocked) {
		t.Errorf("metadata via proxy: err=%v, want ErrBlocked", err)
	}
	if _, err := c.Get("http://10.0.0.1/"); !errors.Is(err, ErrBlocked) {
		t.Errorf("private target via proxy: err=%v, want ErrBlocked", err)
	}
	resp, err := c.Get("http://93.184.216.34/")
	if err != nil {
		t.Fatalf("public target via a loopback proxy: %v", err)
	}
	_ = resp.Body.Close()
	if len(proxied) != 1 {
		t.Errorf("proxy saw %v, want only the public request", proxied)
	}
}

func TestCheckURL(t *testing.T) {
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	for _, u := range []string{"ftp://example.com", "http://169.254.169.254/", "http://127.0.0.1:8585/api", "http:///nohost"} {
		if err := CheckURL(t.Context(), u); err == nil {
			t.Errorf("CheckURL(%q) passed", u)
		}
	}
	if err := CheckURL(t.Context(), "https://93.184.216.34/hook"); err != nil {
		t.Errorf("public literal refused: %v", err)
	}
}
