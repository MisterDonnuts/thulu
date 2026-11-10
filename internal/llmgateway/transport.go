package llmgateway

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var errPrivateUpstream error = apperr.Forbidden("llmgateway.errors.private_upstream")

// A workspace admin sets base_url; without this guard the gateway would read internal services from
// the daemon's network position.
// Non-vouched targets are refused at connect time for internal addresses; configured egress proxies
// may still be dialed.
type upstreamTransport struct {
	open    http.RoundTripper
	guarded http.RoundTripper
}

func newUpstreamTransport() *upstreamTransport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	open := base.Clone()
	guarded := base.Clone()
	plain := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	checked := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseInternal}
	proxies := proxyHosts(os.Getenv)
	guarded.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err == nil && proxies[strings.ToLower(host)] {
			return plain.DialContext(ctx, network, addr)
		}
		return checked.DialContext(ctx, network, addr)
	}
	return &upstreamTransport{open: open, guarded: guarded}
}

func (t *upstreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	target := targetFrom(r.Context())
	if target.Sign != nil {
		if err := signOutgoing(r, target.Sign); err != nil {
			return nil, err
		}
	}
	if target.AllowPrivate {
		return t.open.RoundTrip(r)
	}
	return t.guarded.RoundTrip(r)
}

func signOutgoing(r *http.Request, sign func(context.Context, *http.Request, []byte) error) error {
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(r.Body, MaxSignedBody+1))
		_ = r.Body.Close()
		if err != nil {
			return apperr.Wrap(err, "llmgateway.errors.read_body")
		}
		if len(b) > MaxSignedBody {
			return apperr.TooLarge("llmgateway.errors.body_too_large_to_sign")
		}
		body = b
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	if err := sign(r.Context(), r, body); err != nil {
		return apperr.Wrap(err, "llmgateway.errors.sign_request")
	}
	return nil
}

// Runs after DNS resolution, on the address actually dialed, so a public name resolving inward is
// caught.
func refuseInternal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return apperr.Wrap(err, "llmgateway.errors.dial", "address", address)
	}
	ip := net.ParseIP(host)
	if ip == nil || internalIP(ip) {
		return errPrivateUpstream
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func internalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

func proxyHosts(getenv func(string) string) map[string]bool {
	out := map[string]bool{}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(getenv(k))
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			out[strings.ToLower(u.Hostname())] = true
		}
	}
	return out
}

var probeTransport = newUpstreamTransport()

const ProbeTimeout = 10 * time.Second

func Probe(ctx context.Context, t Target, path string) (int, error) {
	if t.Upstream == nil || t.Upstream.Host == "" || t.Credential == "" || t.Sign != nil {
		return 0, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, targetKey{}, t), ProbeTimeout)
	defer cancel()
	u := *t.Upstream
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return 0, err
	}
	if t.AuthStyle == AuthBearer {
		req.Header.Set("Authorization", "Bearer "+t.Credential) //i18n:ignore HTTP auth scheme
	} else {
		req.Header.Set("x-api-key", t.Credential)
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	resp, err := probeTransport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// RoundTrip does not follow redirects, so a provider cannot bounce the call somewhere the guard
// would refuse.
func Send(ctx context.Context, req *http.Request, allowPrivate bool) (*http.Response, error) {
	return probeTransport.RoundTrip(req.WithContext(context.WithValue(ctx, targetKey{}, Target{AllowPrivate: allowPrivate})))
}
