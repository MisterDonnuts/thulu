package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func fakeLocal(t *testing.T, addrs func() ([]net.Addr, error)) *time.Time {
	t.Helper()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	prevAddrs, prevNow := interfaceAddrs, now
	interfaceAddrs = addrs
	now = func() time.Time { return clock }
	reset := func() {
		local.Lock()
		local.ifaces, local.listedAt, local.discovered = nil, time.Time{}, nil
		local.Unlock()
	}
	reset()
	t.Cleanup(func() {
		interfaceAddrs, now = prevAddrs, prevNow
		reset()
	})
	return &clock
}

func ipNet(s string) net.Addr {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestEgressPolicy_BlocksHostPublicAddress(t *testing.T) {
	fakeLocal(t, func() ([]net.Addr, error) {
		return []net.Addr{ipNet("127.0.0.1/8"), ipNet("203.0.113.20/26"), ipNet("2001:db8:1:2::2/64")}, nil
	})
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	t.Setenv("OCTOPUS_PUBLIC_IPS", "")

	p := EgressPolicy()
	for _, s := range []string{"203.0.113.20", "2001:db8:1:2::2", "::ffff:203.0.113.20"} {
		if err := p.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s (an interface address): %v, want blocked", s, err)
		}
	}
	if err := p.Check(netip.MustParseAddr("203.0.113.21")); err != nil {
		t.Errorf("neighbour address blocked: %v", err)
	}
	if err := InternalPolicy().Check(netip.MustParseAddr("203.0.113.20")); err != nil {
		t.Errorf("internal policy blocked the host address: %v", err)
	}
}

func TestEgressPolicy_PublicIPsEnvAndDiscovered(t *testing.T) {
	fakeLocal(t, func() ([]net.Addr, error) { return []net.Addr{ipNet("172.18.0.5/16")}, nil })
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	t.Setenv("OCTOPUS_PUBLIC_IPS", "203.0.113.9, 198.51.100.0/30, nonsense")

	SetDiscovered("kubernetes-nodes", []netip.Addr{netip.MustParseAddr("192.0.2.44")})
	p := EgressPolicy()
	for _, s := range []string{"203.0.113.9", "198.51.100.2", "192.0.2.44"} {
		if err := p.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v, want blocked", s, err)
		}
	}
	SetDiscovered("kubernetes-nodes", nil)
	if err := EgressPolicy().Check(netip.MustParseAddr("192.0.2.44")); err != nil {
		t.Errorf("cleared node address still blocked: %v", err)
	}
}

func TestEgressPolicy_AllowAdmitsLocal(t *testing.T) {
	fakeLocal(t, func() ([]net.Addr, error) { return []net.Addr{ipNet("203.0.113.20/26")}, nil })
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_PUBLIC_IPS", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "203.0.113.20")
	if err := EgressPolicy().Check(netip.MustParseAddr("203.0.113.20")); err != nil {
		t.Errorf("explicitly allowed host address blocked: %v", err)
	}
	if err := PublicOnlyPolicy().Check(netip.MustParseAddr("203.0.113.20")); !errors.Is(err, ErrBlocked) {
		t.Errorf("public-only policy admitted the host address: %v", err)
	}
	if err := PublicOnlyPolicy().Check(netip.MustParseAddr("10.0.0.1")); !errors.Is(err, ErrBlocked) {
		t.Errorf("public-only policy admitted a private address: %v", err)
	}
}

func TestLocalAddrs_RefreshAndKeepOnFailure(t *testing.T) {
	addrs := []net.Addr{ipNet("203.0.113.20/26")}
	var fail error
	clock := fakeLocal(t, func() ([]net.Addr, error) { return addrs, fail })
	t.Setenv("OCTOPUS_PUBLIC_IPS", "")
	host := netip.MustParsePrefix("203.0.113.20/32")
	moved := netip.MustParsePrefix("198.51.100.1/32")

	if got := LocalAddrs(); !slices.Contains(got, host) {
		t.Fatalf("LocalAddrs = %v, want %v", got, host)
	}
	addrs = append(addrs, ipNet("198.51.100.1/32"))
	if slices.Contains(LocalAddrs(), moved) {
		t.Fatal("listing re-read before the refresh interval")
	}
	*clock = clock.Add(localRefresh)
	if !slices.Contains(LocalAddrs(), moved) {
		t.Fatal("new interface address not picked up after the refresh interval")
	}
	fail = errors.New("netlink: permission denied")
	*clock = clock.Add(localRefresh)
	if got := LocalAddrs(); !slices.Contains(got, host) || !slices.Contains(got, moved) {
		t.Fatalf("after a failed listing LocalAddrs = %v, want the previous addresses", got)
	}
}

func TestTransport_RefusesLocalAddressAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	g := NewWithPolicy(Policy{AllowPrivate: true, Local: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}, nil)
	resp, err := g.Client(5 * time.Second).Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to a local address went through")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}
