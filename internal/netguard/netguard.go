// Checks run at dial time on the dialed address, so DNS rebinding and redirect hops are caught.
// Through a proxy the target is resolved and checked before handoff; the proxy re-resolves, leaving
// a rebinding window.
package netguard

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var ErrBlocked error = apperr.Forbidden("netguard.errors.blocked")

var ErrUnresolved error = apperr.Unprocessable("netguard.errors.unresolved")

type Policy struct {
	AllowPrivate bool
	Allow        []netip.Prefix
	Local        []netip.Prefix
	Disabled     bool
}

// Checked before Allow in Check: no allow-list may admit the metadata endpoints.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("169.254.170.2"),
	netip.MustParseAddr("100.100.100.200"),
	netip.MustParseAddr("fd00:ec2::254"),
}

var privatePrefixes = mustPrefixes(
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10",
	"fc00::/7",
)

var reservedPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"169.254.0.0/16",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"fe80::/10",
	"fec0::/10",
	"ff00::/8",
	"100::/64",
)

var (
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func (p Policy) Check(ip netip.Addr) error {
	if p.Disabled {
		return nil
	}
	if !ip.IsValid() {
		return apperr.Wrap(ErrBlocked, "netguard.errors.invalid_address")
	}
	ip = ip.Unmap().WithZone("")
	if inner, ok := embeddedIPv4(ip); ok {
		if err := p.Check(inner); err != nil {
			return apperr.Wrap(err, "netguard.errors.embedded", "ip", ip)
		}
	}
	switch {
	case slices.Contains(metadataAddrs, ip):
		return apperr.Wrap(ErrBlocked, "netguard.errors.metadata_endpoint", "ip", ip)
	case inAny(p.Allow, ip):
		return nil
	case inAny(reservedPrefixes, ip):
		return apperr.Wrap(ErrBlocked, "netguard.errors.reserved_address", "ip", ip)
	case inAny(privatePrefixes, ip) && !p.AllowPrivate:
		return apperr.Wrap(ErrBlocked, "netguard.errors.private_address", "ip", ip)
	case inAny(p.Local, ip):
		return apperr.Wrap(ErrBlocked, "netguard.errors.local_address", "ip", ip)
	}
	return nil
}

func inAny(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64Prefix.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

func EgressPolicy() Policy {
	return Policy{Allow: allowFromEnv(), Local: LocalAddrs(), Disabled: guardDisabled()}
}

func PublicOnlyPolicy() Policy {
	return Policy{Local: LocalAddrs(), Disabled: guardDisabled()}
}

func InternalPolicy() Policy {
	return Policy{AllowPrivate: true, Allow: allowFromEnv(), Disabled: guardDisabled()}
}

var warned sync.Map

func guardDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_EGRESS_GUARD"))) {
	case "off", "0", "false", "disabled":
		if _, seen := warned.LoadOrStore("disabled", true); !seen {
			log.Printf("[netguard] %s", i18n.Log("netguard.log.guard_disabled"))
		}
		return true
	}
	return false
}

func allowFromEnv() []netip.Prefix {
	raw := os.Getenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS")
	out, bad := parsePrefixes(raw)
	// Log the position, not the value, to keep env text out of the log.
	if len(bad) > 0 {
		if _, seen := warned.LoadOrStore("allow="+raw, true); !seen {
			log.Printf("[netguard] %s", i18n.Log("netguard.log.bad_allow_entry", "index", bad[0]))
		}
	}
	return out
}

func parsePrefixes(raw string) (out []netip.Prefix, bad []int) {
	for i, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if pfx, err := netip.ParsePrefix(part); err == nil {
			out = append(out, pfx.Masked())
			continue
		}
		if ip, err := netip.ParseAddr(part); err == nil {
			out = append(out, hostPrefix(ip))
			continue
		}
		bad = append(bad, i+1)
	}
	return out, bad
}

func hostPrefix(ip netip.Addr) netip.Prefix {
	ip = ip.Unmap().WithZone("")
	return netip.PrefixFrom(ip, ip.BitLen())
}

type Guard struct {
	policy func() Policy
	proxy  func(*http.Request) (*url.URL, error)
	// Only dials to these exact proxy addresses skip the check; the target was already checked in
	// the Proxy hook.
	proxyAddrs sync.Map
}

func NewEgress() *Guard {
	return &Guard{policy: EgressPolicy, proxy: http.ProxyFromEnvironment}
}

func NewInternal() *Guard { return &Guard{policy: InternalPolicy} }

func NewWithPolicy(p Policy, proxy func(*http.Request) (*url.URL, error)) *Guard {
	return &Guard{policy: func() Policy { return p }, proxy: proxy}
}

func (g *Guard) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: g.Transport()}
}

func (g *Guard) Transport() *http.Transport {
	t := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	guarded := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return apperr.Wrap(ErrBlocked, "netguard.errors.dial_address_unparsable", "err", err)
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return apperr.Wrap(ErrBlocked, "netguard.errors.dial_address_unparsable", "err", err)
			}
			return g.policy().Check(ip)
		},
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, ok := g.proxyAddrs.Load(addr); ok {
			return dialer.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
	if g.proxy == nil {
		return t
	}
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		u, err := g.proxy(req)
		if err != nil || u == nil {
			return u, err
		}
		if hostErr := g.checkHost(req.Context(), req.URL.Hostname()); hostErr != nil {
			return nil, hostErr
		}
		g.proxyAddrs.Store(proxyDialAddr(u), true)
		return u, nil
	}
	return t
}

func proxyDialAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// Every resolved address must pass: the proxy may connect to any of them.
func (g *Guard) checkHost(ctx context.Context, host string) error {
	p := g.policy()
	if p.Disabled {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return p.Check(ip)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return apperr.Wrap(ErrUnresolved, "netguard.errors.resolve_failed", "host", host, "err", err)
	}
	if len(addrs) == 0 {
		return apperr.Wrap(ErrUnresolved, "netguard.errors.resolve_empty", "host", host)
	}
	for _, a := range addrs {
		if err := p.Check(a); err != nil {
			return apperr.Wrap(err, "netguard.errors.host_blocked", "host", host)
		}
	}
	return nil
}

func CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "netguard.errors.url_invalid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return apperr.Invalid("netguard.errors.url_scheme", "scheme", u.Scheme)
	}
	if u.Hostname() == "" {
		return apperr.Invalid("netguard.errors.url_no_host")
	}
	return NewEgress().checkHost(ctx, u.Hostname())
}
