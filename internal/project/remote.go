package project

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// repo_url is tenant input passed to `git clone` on the daemon. An unrestricted local remote lets a
// clone read
// another tenant's project dir, so OCTOPUS_GIT_ALLOW_LOCAL_REMOTES is never for shared deployments.
const (
	envAllowSSH          = "OCTOPUS_GIT_ALLOW_SSH"
	envAllowLocalRemotes = "OCTOPUS_GIT_ALLOW_LOCAL_REMOTES"
	envLocalRemoteRoot   = "OCTOPUS_GIT_LOCAL_REMOTE_ROOT"
)

var ErrInvalidRepoURL error = apperr.Invalid("project.errors.invalid_repo_url")

var ErrInvalidBranch error = apperr.Invalid("project.errors.invalid_branch")

type remotePolicy struct {
	allowSSH   bool
	allowLocal bool
	localRoot  string
}

func currentRemotePolicy() remotePolicy {
	return remotePolicy{
		allowSSH:   envTrue(envAllowSSH),
		allowLocal: envTrue(envAllowLocalRemotes),
		localRoot:  strings.TrimSpace(os.Getenv(envLocalRemoteRoot)),
	}
}

func envTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Feeds GIT_ALLOW_PROTOCOL so git itself refuses other transports, including ones a redirect or
// submodule reaches.
func (p remotePolicy) allowedProtocols() []string {
	protos := []string{"https"}
	if p.allowSSH {
		protos = append(protos, "ssh")
	}
	if p.allowLocal || p.localRoot != "" {
		protos = append(protos, "file")
	}
	return protos
}

// git's <transport>::<address> remote-helper syntax (ext::sh -c ..., fd::3) can run commands.
var transportPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

// The part before the colon must have no slash, otherwise git reads it as a local path.
var scpLike = regexp.MustCompile(`^(?:([^@/:\s]+)@)?([^@/:\s]+):(.+)$`)

// Also run before every git call that takes the URL: rows written before this check existed are not
// trusted.
func ValidateRepoURL(raw string) error {
	_, err := classifyRepoURL(raw, currentRemotePolicy())
	return err
}

// DNS can change after create (rebinding); GIT_ALLOW_PROTOCOL and the per-call syntactic check hold
// after that.
func ValidateRepoURLResolved(ctx context.Context, raw string) error {
	pol := currentRemotePolicy()
	host, err := classifyRepoURL(raw, pol)
	if err != nil || host == "" {
		return err
	}
	if saasMode() && !gitAllowedPrivateHost(host) {
		addrs, rerr := resolveGitHost(ctx, host)
		if rerr == nil {
			rerr = checkGitEgressAddrs(host, addrs)
		}
		return apperr.WrapKind(apperr.KindInvalid, rerr, "project.errors.invalid_repo_url")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, lookupFailed(host, err), "project.errors.invalid_repo_url")
	}
	for _, a := range addrs {
		if err := checkRemoteIP(a.IP, pol); err != nil {
			return invalidURLWrap(err, "project.errors.url_resolves_to", "host", host, "ip", a.IP)
		}
	}
	return nil
}

// The resolver's error names sockets and nameserver addresses: log it, never show it to the user.
func lookupFailed(host string, err error) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return apperr.New("project.errors.url_host_unknown", "host", host)
	}
	logx.Warn(err, "project.log.resolve_host", "host", host)
	return apperr.New("project.errors.url_resolve_failed", "host", host)
}

var lookupIPAddr = net.DefaultResolver.LookupIPAddr

func invalidURL(key string, args ...any) error {
	return apperr.WrapKind(apperr.KindInvalid, apperr.New(key, args...), "project.errors.invalid_repo_url")
}

func invalidURLWrap(err error, key string, args ...any) error {
	return apperr.WrapKind(apperr.KindInvalid, apperr.Wrap(err, key, args...), "project.errors.invalid_repo_url")
}

func invalidBranch(key string, args ...any) error {
	return apperr.Wrap(ErrInvalidBranch, key, args...)
}

func errSSHDisabled() error {
	return invalidURL("project.errors.ssh_disabled", "env", envAllowSSH)
}

func classifyRepoURL(raw string, pol remotePolicy) (string, error) {
	if err := checkURLSyntax(raw); err != nil {
		return "", err
	}
	if strings.Contains(raw, "://") {
		return classifySchemeURL(raw, pol)
	}
	if m := scpLike.FindStringSubmatch(raw); m != nil && !strings.HasPrefix(raw, "/") {
		if !pol.allowSSH {
			return "", errSSHDisabled()
		}
		if err := checkRemoteHost(m[2], pol); err != nil {
			return "", apperr.WrapKind(apperr.KindInvalid, err, "project.errors.invalid_repo_url")
		}
		return m[2], nil
	}
	if !pol.allowLocal && pol.localRoot == "" {
		return "", invalidURL("project.errors.local_not_allowed_https")
	}
	if !strings.HasPrefix(raw, "/") {
		return "", invalidURL("project.errors.local_path_relative")
	}
	return "", pol.checkLocalPath(raw)
}

func (p remotePolicy) checkFileURL(u *url.URL) error {
	if !p.allowLocal && p.localRoot == "" {
		return invalidURL("project.errors.local_not_allowed")
	}
	if h := u.Hostname(); h != "" && !strings.EqualFold(h, "localhost") {
		return invalidURL("project.errors.file_url_host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return invalidURL("project.errors.url_query")
	}
	return p.checkLocalPath(u.Path)
}

func (p remotePolicy) checkLocalPath(path string) error {
	if p.allowLocal {
		return nil
	}
	if p.localRoot == "" {
		return invalidURL("project.errors.local_not_allowed_https")
	}
	if !filepath.IsAbs(p.localRoot) {
		return invalidURL("project.errors.local_root_relative", "env", envLocalRemoteRoot)
	}
	root, err := filepath.EvalSymlinks(p.localRoot)
	if err != nil {
		return invalidURLWrap(err, "project.errors.local_root_unresolvable", "env", envLocalRemoteRoot)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return invalidURL("project.errors.local_missing", "path", path)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return invalidURL("project.errors.local_outside_root", "root", p.localRoot)
	}
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if comp == ".git" {
			return invalidURL("project.errors.local_checkout_git")
		}
	}
	if !isBareRepo(resolved) {
		return invalidURL("project.errors.local_not_bare", "path", path)
	}
	return nil
}

func isBareRepo(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return false
	}
	head, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

func checkURLSyntax(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return invalidURL("project.errors.url_whitespace")
	}
	if strings.HasPrefix(raw, "-") {
		return invalidURL("project.errors.url_leading_dash")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return invalidURL("project.errors.url_control_chars")
		}
	}
	if transportPrefix.MatchString(raw) {
		return invalidURL("project.errors.url_transport_helper")
	}
	return nil
}

func classifySchemeURL(raw string, pol remotePolicy) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalidURL("project.errors.url_unparseable")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.User != nil {
			return "", invalidURL("project.errors.url_credentials_github")
		}
	case "ssh":
		if !pol.allowSSH {
			return "", errSSHDisabled()
		}
		if _, hasPass := u.User.Password(); hasPass {
			return "", invalidURL("project.errors.url_credentials")
		}
	case "file":
		return "", pol.checkFileURL(u)
	default:
		return "", invalidURL("project.errors.url_scheme", "scheme", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", invalidURL("project.errors.url_query")
	}
	host := u.Hostname()
	if host == "" {
		return "", invalidURL("project.errors.url_missing_host")
	}
	if strings.Trim(u.Path, "/") == "" {
		return "", invalidURL("project.errors.url_missing_path")
	}
	if err := checkRemoteHost(host, pol); err != nil {
		return "", apperr.WrapKind(apperr.KindInvalid, err, "project.errors.invalid_repo_url")
	}
	return host, nil
}

// Private ranges (10/8, 192.168/16, ...) stay allowed: GitHub Enterprise Server usually lives
// there.
func checkRemoteHost(host string, pol remotePolicy) error {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		if !pol.allowLocal {
			return apperr.Invalid("project.errors.host_loopback", "host", host)
		}
		return nil
	}
	if ip := net.ParseIP(h); ip != nil {
		return checkRemoteIP(ip, pol)
	}
	return nil
}

func checkRemoteIP(ip net.IP, pol remotePolicy) error {
	switch {
	case ip.IsLoopback():
		if !pol.allowLocal {
			return apperr.Invalid("project.errors.ip_loopback")
		}
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return apperr.Invalid("project.errors.ip_link_local")
	case ip.IsUnspecified():
		return apperr.Invalid("project.errors.ip_unspecified")
	case ip.IsMulticast():
		return apperr.Invalid("project.errors.ip_multicast")
	}
	return nil
}

// The image has no ssh client, and a GitHub credential or App token only authenticates HTTPS.
func NormalizeRepoURL(raw string) string {
	if currentRemotePolicy().allowSSH {
		return raw
	}
	return githubSSHToHTTPS(raw)
}

func githubSSHToHTTPS(raw string) string {
	host := githubcfg.Host()
	web := githubcfg.WebURL()
	for _, prefix := range []string{"git@" + host + ":", "ssh://git@" + host + "/"} {
		if len(raw) > len(prefix) && strings.EqualFold(raw[:len(prefix)], prefix) {
			return web + "/" + raw[len(prefix):]
		}
	}
	return raw
}

// Rows written before ValidateRepoURL rejected credentials may still carry them.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("***")
	return u.String()
}

func ValidateBranchName(name string) error {
	switch {
	case name == "":
		return invalidBranch("project.errors.branch_empty", "name", name)
	case len(name) > 255:
		return invalidBranch("project.errors.branch_too_long", "name", name)
	case strings.HasPrefix(name, "-"):
		return invalidBranch("project.errors.branch_leading_dash", "name", name)
	case name == "@" || name == "HEAD":
		return invalidBranch("project.errors.branch_reserved", "name", name)
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return invalidBranch("project.errors.branch_slash", "name", name)
	case strings.HasSuffix(name, "."):
		return invalidBranch("project.errors.branch_trailing_dot", "name", name)
	case strings.Contains(name, ".."):
		return invalidBranch("project.errors.branch_double_dot", "name", name)
	case strings.Contains(name, "@{"):
		return invalidBranch("project.errors.branch_reflog", "name", name)
	case strings.Contains(name, "//"):
		return invalidBranch("project.errors.branch_empty_component", "name", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return invalidBranch("project.errors.branch_forbidden_char", "name", name)
		}
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") {
			return invalidBranch("project.errors.branch_component_dot", "name", name)
		}
		if strings.HasSuffix(comp, ".lock") {
			return invalidBranch("project.errors.branch_component_lock", "name", name)
		}
	}
	return nil
}
