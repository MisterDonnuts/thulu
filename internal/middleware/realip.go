package middleware

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// XFF is client-written unless the direct peer is in OCTOPUS_TRUSTED_PROXIES; trusting it lets
// callers bypass per-IP limits and forge audit entries.
// "private" also trusts agents, which are private peers and could forge the client address.
var (
	trustedOnce     sync.Once
	trustedNets     []*net.IPNet
	trustAllPrivate bool
)

func loadTrustedProxies() {
	trustedOnce.Do(func() {
		for i, part := range strings.Split(os.Getenv("OCTOPUS_TRUSTED_PROXIES"), ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.EqualFold(part, "private") {
				trustAllPrivate = true
				continue
			}
			if ipnet := parseTrustedEntry(part); ipnet != nil {
				trustedNets = append(trustedNets, ipnet)
				continue
			}
			// Log the position, not the value, to keep env text out of the log.
			log.Printf("[realip] %s", i18n.Log("middleware.log.realip_bad_entry", "index", i+1))
		}
		if trustAllPrivate && os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			// In a cluster "private" is every pod: any of them can forge X-Forwarded-For for the
			// audit log and rate limiter.
			log.Printf("[realip] %s", i18n.Log("middleware.log.realip_private_in_kubernetes"))
		}
	})
}

func parseTrustedEntry(part string) *net.IPNet {
	if _, ipnet, err := net.ParseCIDR(part); err == nil {
		return ipnet
	}
	ip := net.ParseIP(part)
	if ip == nil {
		return nil
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
}

func isTrustedProxy(ipStr string) bool {
	loadTrustedProxies()
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	if trustAllPrivate && (ip.IsLoopback() || ip.IsPrivate()) {
		return true
	}
	for _, n := range trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func ClientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" || !isTrustedProxy(peer) {
		return peer
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if candidate == "" {
			continue
		}
		if net.ParseIP(candidate) == nil {
			// A non-IP hop is client-written, and so is everything left of it: fall back to the
			// peer rather than log or key on a client-chosen string.
			return peer
		}
		if !isTrustedProxy(candidate) {
			return candidate
		}
	}
	if first := strings.TrimSpace(parts[0]); net.ParseIP(first) != nil {
		return first
	}
	return peer
}

// X-Forwarded-* headers are client-written unless this is true; gate X-Forwarded-Proto on it like
// ClientIP gates XFF.
func PeerIsTrustedProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	return isTrustedProxy(peer)
}

func ReloadTrustedProxies() {
	trustedOnce = sync.Once{}
	trustedNets = nil
	trustAllPrivate = false
}
