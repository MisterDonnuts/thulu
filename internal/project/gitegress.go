package project

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

// With OCTOPUS_SAAS_MODE=1, every daemon git call that reaches a remote refuses hosts resolving to
// private or own addresses (SSRF),
// pins curl to the checked addresses against rebinding, and disables redirects.
// Self-hosted installs skip this because their GHES often sits on a private range.
const (
	envSaaSMode               = "OCTOPUS_SAAS_MODE"
	envGitAllowedPrivateHosts = "OCTOPUS_GIT_ALLOWED_PRIVATE_HOSTS"
)

const gitLookupTimeout = 5 * time.Second

func saasMode() bool {
	v := strings.TrimSpace(os.Getenv(envSaaSMode))
	return v == "1" || strings.EqualFold(v, "true")
}

var networkSubcommands = []string{"clone", "fetch", "push", "pull", "ls-remote"}

func gitAllowedPrivateHost(host string) bool {
	h := normalizeHost(host)
	for _, entry := range strings.Split(os.Getenv(envGitAllowedPrivateHosts), ",") {
		if e := normalizeHost(entry); e != "" && e == h {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), "."))
}

func checkGitEgressAddrs(host string, addrs []netip.Addr) error {
	pol := netguard.PublicOnlyPolicy()
	for _, a := range addrs {
		if err := pol.Check(a); err != nil {
			return apperr.Wrap(err, "project.errors.git_remote_blocked", "host", host, "env", envGitAllowedPrivateHosts)
		}
	}
	return nil
}

func resolveGitHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitLookupTimeout)
	defer cancel()
	ips, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, lookupFailed(host, err)
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if a, ok := netip.AddrFromSlice(ip.IP); ok {
			out = append(out, a.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, apperr.New("project.errors.url_resolve_failed", "host", host)
	}
	return out, nil
}

type gitRemoteTarget struct {
	host string
	port string
}

func remoteTarget(raw string) (gitRemoteTarget, bool) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return gitRemoteTarget{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http":
			port := u.Port()
			if port == "" {
				port = "443"
				if strings.EqualFold(u.Scheme, "http") {
					port = "80"
				}
			}
			return gitRemoteTarget{host: u.Hostname(), port: port}, u.Hostname() != ""
		case "ssh":
			return gitRemoteTarget{host: u.Hostname()}, u.Hostname() != ""
		}
		return gitRemoteTarget{}, false
	}
	// git reads `[host]:path` (IPv6 literals included) as scp-like ssh; scpLike would split it
	// wrongly, so it is left unreadable and refused.
	if m := scpLike.FindStringSubmatch(raw); m != nil && !strings.HasPrefix(raw, "/") && !strings.Contains(m[2], "[") {
		return gitRemoteTarget{host: m[2]}, true
	}
	return gitRemoteTarget{}, false
}

func (g localGitExec) gitEgressArgs(dir string, args []string) ([]string, error) {
	if !saasMode() {
		return nil, nil
	}
	remote, network, err := networkRemote(args)
	if err != nil || !network {
		return nil, err
	}
	urls, err := g.remoteURLs(dir, remote)
	if err != nil {
		return nil, err
	}
	var pins []string
	for _, raw := range urls {
		pin, err := checkRemoteURL(raw)
		if err != nil {
			return nil, err
		}
		pins = append(pins, pin...)
	}
	return pins, nil
}

// Every daemon call must put the remote right after `--`; a network command without one is refused
// since its destination cannot be checked.
func networkRemote(args []string) (remote string, network bool, err error) {
	sub := 0
	for sub < len(args) && args[sub] == "-c" {
		sub += 2
	}
	if sub >= len(args) || !slices.Contains(networkSubcommands, args[sub]) {
		return "", false, nil
	}
	dash := slices.Index(args[sub:], "--")
	if dash < 0 || sub+dash+1 >= len(args) {
		return "", true, apperr.Forbidden("project.errors.git_remote_unknown", "cmd", args[sub])
	}
	return args[sub+dash+1], true, nil
}

func (g localGitExec) remoteURLs(dir, remote string) ([]string, error) {
	// A remote name never holds a colon; with one it is a URL or scp-like address, checked as
	// given.
	if _, ok := remoteTarget(remote); ok || strings.HasPrefix(remote, "/") || strings.Contains(remote, ":") {
		return []string{remote}, nil
	}
	var urls []string
	for _, flags := range [][]string{{"--all"}, {"--push", "--all"}} {
		out, err := g.Output(dir, append(append([]string{"remote", "get-url"}, flags...), remote)...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" && !slices.Contains(urls, line) {
				urls = append(urls, line)
			}
		}
	}
	return urls, nil
}

func localRemote(raw string) bool {
	return strings.HasPrefix(raw, "/") || strings.HasPrefix(strings.ToLower(raw), "file://")
}

// A remote that is neither a readable URL nor a local path is refused: git or curl could parse it
// differently (`https://a\@b/`, `[fd00::1]:repo`) and reach an unchecked host.
func checkRemoteURL(raw string) ([]string, error) {
	t, ok := remoteTarget(raw)
	if !ok {
		if localRemote(raw) {
			return nil, nil
		}
		return nil, apperr.Wrap(netguard.ErrBlocked, "project.errors.git_remote_unparsable")
	}
	if gitAllowedPrivateHost(t.host) {
		return nil, nil
	}
	addrs, err := resolveGitHost(context.Background(), t.host)
	if err != nil {
		return nil, err
	}
	if err := checkGitEgressAddrs(t.host, addrs); err != nil {
		return nil, err
	}
	if t.port == "" || net.ParseIP(strings.Trim(t.host, "[]")) != nil {
		return nil, nil
	}
	strs := make([]string, len(addrs))
	for i, a := range addrs {
		strs[i] = a.String()
		if a.Is6() {
			strs[i] = "[" + strs[i] + "]"
		}
	}
	return []string{"-c", "http.curloptResolve=" + t.host + ":" + t.port + ":" + strings.Join(strs, ",")}, nil
}
