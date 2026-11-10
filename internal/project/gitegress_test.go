package project

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/netguard"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func stubLookup(t *testing.T, answers map[string][]string) {
	t.Helper()
	orig := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = orig })
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		ips, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		out := make([]net.IPAddr, len(ips))
		for i, ip := range ips {
			out[i] = net.IPAddr{IP: net.ParseIP(ip)}
		}
		return out, nil
	}
}

var egressAnswers = map[string][]string{
	"github.com":            {"140.82.112.3"},
	"dual.example":          {"140.82.112.3", "2606:50c0:8000::153"},
	"cluster.evil.example":  {"10.96.0.1"},
	"metadata.evil.example": {"169.254.169.254"},
	"cgnat.evil.example":    {"100.100.1.1"},
	"ula.evil.example":      {"fd00::1"},
	"loop.evil.example":     {"127.0.0.1"},
	"mixed.evil.example":    {"140.82.112.3", "192.168.1.10"},
	"self.evil.example":     {"203.0.113.77"},
	"ghe.corp.example":      {"10.1.2.3"},
}

func saasEgress(t *testing.T) {
	t.Helper()
	t.Setenv(envSaaSMode, "1")
	t.Setenv(envGitAllowedPrivateHosts, "")
	stubLookup(t, egressAnswers)
	netguard.SetDiscovered("test-node", []netip.Addr{netip.MustParseAddr("203.0.113.77")})
	t.Cleanup(func() { netguard.SetDiscovered("test-node", nil) })
}

func TestGitEgressSaaSRefusesInternalRemotes(t *testing.T) {
	saasEgress(t)
	t.Setenv(envAllowLocalRemotes, "")
	for _, u := range []string{
		"https://cluster.evil.example/o/r",
		"https://metadata.evil.example/o/r",
		"https://cgnat.evil.example/o/r",
		"https://ula.evil.example/o/r",
		"https://loop.evil.example/o/r",
		"https://mixed.evil.example/o/r",
		"https://self.evil.example/o/r",
		"https://10.96.0.1/o/r",
		"https://[fd00::1]/o/r",
		"https://203.0.113.77:6443/o/r",
	} {
		dest := filepath.Join(t.TempDir(), "c")
		err := gitCloneAuth(t.Context(), "", u, dest, "")
		if err == nil || !errors.Is(err, netguard.ErrBlocked) && !strings.Contains(err.Error(), "not reachable") {
			t.Errorf("clone %s: err = %v, want refused", u, err)
		}
		if err := ValidateRepoURLResolved(t.Context(), u); !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("ValidateRepoURLResolved(%s) = %v, want ErrInvalidRepoURL", u, err)
		}
	}
	if err := ValidateRepoURLResolved(t.Context(), "https://nx.example/o/r"); !errors.Is(err, ErrInvalidRepoURL) {
		t.Errorf("an unresolvable host must fail at create: %v", err)
	}
	if err := ValidateRepoURLResolved(t.Context(), "https://github.com/o/r"); err != nil {
		t.Errorf("a public host was refused: %v", err)
	}
}

func TestGitEgressSaaSPinsCheckedAddresses(t *testing.T) {
	saasEgress(t)
	g := localGitExec{}
	pins, err := g.gitEgressArgs("", []string{"-c", "url.https://x@github.com/.insteadOf=https://github.com/", "clone", "--no-recurse-submodules", "--", "https://dual.example/o/r", "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "http.curloptResolve=dual.example:443:140.82.112.3,[2606:50c0:8000::153]"}
	if !slices.Equal(pins, want) {
		t.Errorf("pins = %q, want %q", pins, want)
	}
	pins, err = g.gitEgressArgs("", []string{"ls-remote", "--", "https://github.com:8443/o/r"})
	if err != nil || !slices.Equal(pins, []string{"-c", "http.curloptResolve=github.com:8443:140.82.112.3"}) {
		t.Errorf("explicit port: pins = %q, err = %v", pins, err)
	}
	for _, args := range [][]string{{"status"}, {"-c", "x=y", "log", "--", "a"}, {"remote", "get-url", "origin"}} {
		if pins, err := g.gitEgressArgs("", args); err != nil || pins != nil {
			t.Errorf("%q: pins = %q, err = %v", args, pins, err)
		}
	}
	if _, err := g.gitEgressArgs("", []string{"fetch", "https://cluster.evil.example/o/r"}); err == nil {
		t.Error("fetch without -- was not refused")
	}
	if _, err := g.gitEgressArgs("", []string{"push", "--"}); err == nil {
		t.Error("push with nothing after -- was not refused")
	}
	if !slices.Contains(hardenedConfig(), "http.followRedirects=false") {
		t.Error("SaaS git calls must not follow redirects")
	}
}

func TestGitEgressSaaSChecksNamedRemotes(t *testing.T) {
	src := testgit.BareRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	testgit.Clone(t, src, clone)
	saasEgress(t)
	g := repoGit(clone)

	if err := g.Run(clone, "fetch", "--no-recurse-submodules", "--", "origin", "main"); err != nil {
		t.Fatalf("fetch from a local origin: %v", err)
	}

	if err := g.Run(clone, "remote", "set-url", "origin", "https://cluster.evil.example/o/r"); err != nil {
		t.Fatal(err)
	}
	if err := g.Run(clone, "fetch", "--", "origin", "main"); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("fetch from an internal origin: err = %v, want ErrBlocked", err)
	}

	if err := g.Run(clone, "remote", "set-url", "origin", "https://github.com/o/r"); err != nil {
		t.Fatal(err)
	}
	if err := g.Run(clone, "remote", "set-url", "--push", "origin", "https://metadata.evil.example/o/r"); err != nil {
		t.Fatal(err)
	}
	if err := g.Run(clone, "push", "--", "origin", "main"); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("push to an internal push URL: err = %v, want ErrBlocked", err)
	}
}

func TestGitEgressAllowedPrivateHosts(t *testing.T) {
	saasEgress(t)
	t.Setenv(envGitAllowedPrivateHosts, " GHE.corp.example. ,10.9.9.9")
	g := localGitExec{}
	for _, u := range []string{"https://ghe.corp.example/o/r", "https://10.9.9.9/o/r", "git@ghe.corp.example:o/r.git"} {
		if pins, err := g.gitEgressArgs("", []string{"clone", "--", u, "/tmp/x"}); err != nil || pins != nil {
			t.Errorf("%s: pins = %q, err = %v", u, pins, err)
		}
	}
	if err := ValidateRepoURLResolved(t.Context(), "https://ghe.corp.example/o/r"); err != nil {
		t.Errorf("allowed private host refused at create: %v", err)
	}
	if _, err := g.gitEgressArgs("", []string{"clone", "--", "https://cluster.evil.example/o/r", "/tmp/x"}); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("unlisted private host: err = %v", err)
	}
}

func TestGitEgressSelfHostedUnchanged(t *testing.T) {
	t.Setenv(envSaaSMode, "")
	stubLookup(t, egressAnswers)
	if pins, err := (localGitExec{}).gitEgressArgs("", []string{"clone", "--", "https://ghe.corp.example/o/r", "/tmp/x"}); err != nil || pins != nil {
		t.Errorf("pins = %q, err = %v", pins, err)
	}
	if err := ValidateRepoURLResolved(t.Context(), "https://ghe.corp.example/o/r"); err != nil {
		t.Errorf("private GHES host rejected outside SaaS: %v", err)
	}
	if slices.Contains(hardenedConfig(), "http.followRedirects=false") {
		t.Error("followRedirects changed outside SaaS mode")
	}
}

func TestRemoteTarget(t *testing.T) {
	for raw, want := range map[string]gitRemoteTarget{
		"https://github.com/o/r":   {host: "github.com", port: "443"},
		"https://[::1]:8443/o/r":   {host: "::1", port: "8443"},
		"ssh://git@github.com/o/r": {host: "github.com"},
		"git@github.com:o/r.git":   {host: "github.com"},
		"github.com:o/r.git":       {host: "github.com"},
	} {
		if got, ok := remoteTarget(raw); !ok || got != want {
			t.Errorf("remoteTarget(%q) = %+v, %v; want %+v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"/srv/repo.git", "file:///srv/repo.git", "origin", "./rel"} {
		if got, ok := remoteTarget(raw); ok {
			t.Errorf("remoteTarget(%q) = %+v, want local", raw, got)
		}
	}
}

func TestGitEgressSaaSRefusesUnreadableRemotes(t *testing.T) {
	saasEgress(t)
	g := localGitExec{}
	for _, u := range []string{
		`https://github.com\@10.96.0.1/o/r`,
		"https://10.96.0.1%0a/o/r",
		"https:///o/r",
		"[fd00::1]:o/r",
		"git://10.96.0.1/o/r",
	} {
		if _, err := g.gitEgressArgs("", []string{"clone", "--", u, "/tmp/x"}); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s: err = %v, want ErrBlocked", u, err)
		}
	}
	for _, u := range []string{"/srv/repo.git", "file:///srv/repo.git", "FILE:///srv/repo.git"} {
		if pins, err := g.gitEgressArgs("", []string{"fetch", "--", u, "main"}); err != nil || pins != nil {
			t.Errorf("%s: pins = %q, err = %v", u, pins, err)
		}
	}
}
