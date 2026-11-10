package initwizard

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Interviewer struct {
	P   *Prompter
	Cfg *Config
	Ctx context.Context //nolint:containedctx // the interview is one long-lived operation; probes borrow this

	Existing *EnvFile

	Detect func()

	NoSource bool

	BinaryVersion string

	HostArch func() string

	// Nil skips the check, and the bundled Postgres stays on 5433.
	PortFree func(port int) bool

	PreviousMCP []string

	PreviousEngines   []string
	PreviousRunner    RunnerPlan
	PreviousQuick     bool
	PreviousMode      Mode
	PreviousCompose   string
	PreviousWorkspace struct{ Name, Slug, ID string }
	PreviousCluster   struct{ Namespace, Release, RuntimeClass, AgentImage string }

	archChecked bool
	Stack       string

	step int
}

func (iv *Interviewer) section(title, blurb string) {
	iv.step++
	iv.P.Section(iv.step, title, blurb)
}

func (iv *Interviewer) def(key, fallback string) string {
	if iv.Existing == nil {
		return fallback
	}
	if v := iv.Existing.Get(key); v != "" {
		return v
	}
	return fallback
}

func (iv *Interviewer) Run() error {
	if err := iv.askMode(); err != nil {
		return err
	}
	setup := []func() error{iv.askIdentity, iv.askDatabase, iv.askNetwork, iv.askKubernetes, iv.askMCP, iv.askLicense, iv.askWorkspace}
	tail := []func() error{iv.askEngines, iv.askRunner, iv.askPostBoot}
	if iv.Cfg.Quick {
		if err := iv.withDefaults(setup); err != nil {
			return err
		}
		setup = nil
	}
	steps := make([]func() error, 0, len(setup)+len(tail))
	steps = append(steps, setup...)
	steps = append(steps, tail...)
	iv.P.Total = iv.countSections(steps)
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (iv *Interviewer) countSections(steps []func() error) int {
	n := len(steps)
	if iv.Cfg.Quick {
		return n
	}
	// askKubernetes (Kubernetes only) and askRunner (Docker only) are both in steps; exactly one of
	// them prints a section.
	return n - 1
}

func (iv *Interviewer) withDefaults(steps []func() error) error {
	saved, quiet, step := iv.P.NonInteractive, iv.P.muted, iv.step
	deferred := len(iv.Cfg.Deferred)
	iv.P.NonInteractive, iv.P.muted = true, true
	defer func() {
		iv.P.NonInteractive, iv.P.muted, iv.step = saved, quiet, step
		iv.Cfg.Deferred = iv.Cfg.Deferred[:deferred]
	}()
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

const (
	modeQuick  = "quick"
	modeCustom = "docker"
)

func (iv *Interviewer) askMode() error {
	if iv.Stack != "" {
		return iv.useStack()
	}
	iv.P.Section(0, i18n.Text("initwizard.sections.mode.title"), i18n.Text("initwizard.sections.mode.blurb"))

	if iv.Detect != nil {
		iv.Detect()
	} else {
		iv.preflight()
	}

	mode, err := iv.P.Choice(i18n.Text("initwizard.prompts.mode"), []Option{
		{Value: modeQuick, Label: i18n.Text("initwizard.options.mode_quick.label"),
			Detail: i18n.Text("initwizard.options.mode_quick.detail"), Diagram: modeDiagram(modeQuick)},
		{Value: modeCustom, Label: i18n.Text("initwizard.options.mode_docker.label"),
			Detail: i18n.Text("initwizard.options.mode_docker.detail"), Diagram: modeDiagram(modeCustom)},
		{Value: string(ModeKubernetes), Label: i18n.Text("initwizard.options.mode_kubernetes.label"),
			Detail: i18n.Text("initwizard.options.mode_kubernetes.detail"), Diagram: modeDiagram(string(ModeKubernetes))},
	}, iv.defaultMode())
	if err != nil {
		return err
	}
	switch mode {
	case modeQuick:
		iv.Cfg.Mode, iv.Cfg.Quick = ModeDocker, true
		iv.Cfg.ComposeFile = standaloneComposeFile
		return iv.withDefaults([]func() error{iv.askImage})
	case string(ModeKubernetes):
		iv.Cfg.Mode = ModeKubernetes
		return nil
	}
	iv.Cfg.Mode = ModeDocker
	return iv.askComposeStack()
}

func (iv *Interviewer) defaultMode() int {
	switch {
	case iv.PreviousMode == ModeKubernetes:
		return 2
	case iv.PreviousMode == ModeDocker && !iv.PreviousQuick:
		return 1
	}
	return 0
}

func (iv *Interviewer) askComposeStack() error {
	opts := []Option{
		{Value: caddyComposeFile, Label: i18n.Text("initwizard.options.stack_caddy.label"),
			Detail: i18n.Text("initwizard.options.stack_caddy.detail", "file", caddyComposeFile), Diagram: stackDiagram(caddyComposeFile)},
		{Value: standaloneComposeFile, Label: i18n.Text("initwizard.options.stack_standalone.label"),
			Detail:  i18n.Text("initwizard.options.stack_standalone.detail", "file", standaloneComposeFile),
			Diagram: stackDiagram(standaloneComposeFile)},
	}
	def := 0
	if iv.PreviousCompose == standaloneComposeFile {
		def = 1
	}
	if builds(iv.PreviousCompose) && !iv.NoSource {
		opts = append(opts, Option{Value: iv.PreviousCompose,
			Label:  i18n.Text("initwizard.options.stack_keep.label", "file", iv.PreviousCompose),
			Detail: i18n.Text("initwizard.options.stack_keep.detail")})
		def = len(opts) - 1
	}
	stack, err := iv.P.Choice(i18n.Text("initwizard.prompts.compose_stack"), opts, def)
	if err != nil {
		return err
	}
	iv.Cfg.ComposeFile = stack
	if builds(stack) {
		return nil
	}
	return iv.askImage()
}

func (iv *Interviewer) useStack() error {
	switch iv.Stack {
	case caddyComposeFile, standaloneComposeFile, prodComposeFile, devComposeFile:
	default:
		return apperr.Precondition("initwizard.errors.unknown_stack", "file", iv.Stack,
			"known", strings.Join([]string{caddyComposeFile, standaloneComposeFile, prodComposeFile, devComposeFile}, ", "))
	}
	if builds(iv.Stack) && iv.NoSource {
		return apperr.Precondition("initwizard.errors.stack_needs_checkout", "file", iv.Stack)
	}
	iv.Cfg.Mode, iv.Cfg.ComposeFile = ModeDocker, iv.Stack
	iv.P.Check(CheckOK, i18n.Text("initwizard.cli.stack_from_flag"), iv.Stack)
	if builds(iv.Stack) {
		return nil
	}
	return iv.askImage()
}

func builds(file string) bool { return file == prodComposeFile || file == devComposeFile }

// Defaults to this binary's version so the daemon's schema matches the CLI managing it.
func (iv *Interviewer) askImage() error {
	image, err := iv.P.Text(i18n.Text("initwizard.prompts.daemon_image"), iv.def("OCTOPUS_IMAGE", ReleaseImage(iv.BinaryVersion)))
	if err != nil {
		return err
	}
	iv.Cfg.Image = image
	if reg, tag, ok := splitDaemonImage(image); ok {
		iv.Cfg.ImageTag = tag
		if reg != ImageRegistry(DefaultRepo) || imageRegistryOverride() != "" {
			iv.Cfg.ImageRegistry = reg
		}
	} else {
		iv.Cfg.ImageTag = ImageTag(iv.BinaryVersion)
	}
	iv.warnImageArch()
	return nil
}

func (iv *Interviewer) warnImageArch() {
	if iv.HostArch == nil || iv.archChecked {
		return
	}
	if arch := iv.HostArch(); arch != "" && !imagePublishedFor(arch) {
		iv.P.narrate("initwizard.cli.image_arch_mismatch", "arch", arch, "image_arch", publishedImageArchList())
		iv.P.Printf("")
	}
}

func (iv *Interviewer) askIdentity() error {
	iv.section(i18n.Text("initwizard.sections.identity.title"), i18n.Text("initwizard.sections.identity.blurb"))

	opts := []Option{
		{Value: string(IdentityOIDC), Label: i18n.Text("initwizard.options.identity_oidc.label"),
			Detail: i18n.Text("initwizard.options.identity_oidc.detail")},
		{Value: string(IdentityGitHub), Label: i18n.Text("initwizard.options.identity_github.label"),
			Detail: i18n.Text("initwizard.options.identity_github.detail")},
	}
	// No-login is offered only where the port is guaranteed loopback (dev override, standalone
	// OCTOPUS_BIND);
	// the Caddy stack publishes 80/443 publicly, the case the startup guard refuses.
	if iv.loopbackAvailable() {
		opts = append(opts, Option{Value: string(IdentityNone), Label: i18n.Text("initwizard.options.identity_none.label"),
			Detail: i18n.Text("initwizard.options.identity_none.detail")})
	} else if iv.Cfg.Mode == ModeDocker {
		iv.P.narrate("initwizard.cli.no_login_unavailable")
		iv.P.Printf("")
	}
	choice, err := iv.P.Choice(i18n.Text("initwizard.prompts.identity"), opts, iv.defaultIdentity(opts))
	if err != nil {
		return err
	}
	iv.Cfg.Identity = Identity(choice)

	switch iv.Cfg.Identity {
	case IdentityOIDC:
		return iv.askOIDC()
	case IdentityGitHub:
		return iv.askGitHubOAuth()
	case IdentityNone:
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.auth_disabled")
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.sso.step"), i18n.Text("initwizard.deferred.sso.effect"),
			i18n.Text("initwizard.deferred.sso.finish"))
		return nil
	}
	return nil
}

func (iv *Interviewer) defaultIdentity(opts []Option) int {
	want := ""
	switch {
	case iv.def("OCTOPUS_OIDC_ISSUER", "") != "":
		want = string(IdentityOIDC)
	case iv.def("OCTOPUS_GITHUB_CLIENT_ID", "") != "":
		want = string(IdentityGitHub)
	case iv.loopbackAvailable():
		want = string(IdentityNone)
	}
	for i, o := range opts {
		if o.Value == want {
			return i
		}
	}
	return 0
}

func (iv *Interviewer) loopbackAvailable() bool {
	return iv.Cfg.ComposeFile == devComposeFile || iv.Cfg.ComposeFile == standaloneComposeFile
}

func (iv *Interviewer) askOIDC() error {
	iv.P.narrate("initwizard.cli.oidc_intro")
	iv.P.Printf("")
	return iv.askOIDCIssuer()
}

func (iv *Interviewer) askOIDCIssuer() error {
	issuer, err := iv.P.Text(i18n.Text("initwizard.prompts.issuer_url"), iv.def("OCTOPUS_OIDC_ISSUER", ""))
	if err != nil {
		return err
	}
	if issuer == "" {
		return apperr.Invalid("initwizard.errors.issuer_required")
	}
	if doc, probeErr := ProbeOIDC(iv.Ctx, issuer); probeErr != nil {
		iv.P.narrate("initwizard.cli.discovery_failed", "err", apperr.Text(probeErr))
		keep, askErr := iv.P.YesNo(i18n.Text("initwizard.prompts.use_issuer_anyway"), false)
		if askErr != nil {
			return askErr
		}
		if !keep {
			// Non-interactive: asking again returns the same URL and fails the same probe forever.
			if iv.P.cannotAskAgain() {
				return apperr.Wrap(probeErr, "initwizard.errors.issuer_unverified", "issuer", issuer)
			}
			return iv.askOIDCIssuer()
		}
	} else {
		iv.P.narrate("initwizard.cli.discovery_ok", "url", doc.AuthURL)
	}
	iv.Cfg.OIDC.Issuer = issuer

	if iv.Cfg.OIDC.ClientID, err = iv.P.Text(i18n.Text("initwizard.prompts.client_id"), iv.def("OCTOPUS_OIDC_CLIENT_ID", "")); err != nil {
		return err
	}
	secret, err := iv.P.Secret(i18n.Text("initwizard.prompts.client_secret"))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.OIDC.ClientSecret = iv.def("OCTOPUS_OIDC_CLIENT_SECRET", "")
	case err != nil:
		return err
	default:
		iv.Cfg.OIDC.ClientSecret = secret
	}
	if iv.Cfg.OIDC.ClientID == "" || iv.Cfg.OIDC.ClientSecret == "" {
		return apperr.Invalid("initwizard.errors.oidc_half_credentials")
	}

	if iv.Cfg.OIDC.DisplayName, err = iv.P.Text(i18n.Text("initwizard.prompts.oidc_button_label"), iv.def("OCTOPUS_OIDC_NAME", "SSO")); err != nil {
		return err
	}
	if err := iv.askSSODomains(); err != nil {
		return err
	}
	return iv.askOIDCRoles()
}

func (iv *Interviewer) askSSODomains() error {
	domains, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.sso_domains"),
		iv.def("OCTOPUS_SSO_ALLOWED_DOMAINS", ""))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.sso_domains.step"),
			i18n.Text("initwizard.deferred.sso_domains.effect"),
			i18n.Text("initwizard.deferred.sso_domains.finish"))
	case err != nil:
		return err
	default:
		iv.Cfg.OIDC.AllowedDomains = domains
	}
	return nil
}

func (iv *Interviewer) askOIDCRoles() error {
	claim, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.groups_claim"),
		iv.def("OCTOPUS_OIDC_GROUPS_CLAIM", "groups"))
	if err != nil && !errors.Is(err, ErrSkipped) {
		return err
	}
	iv.Cfg.OIDC.GroupsClaim = claim

	mapping, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.group_role_map"), iv.def("OCTOPUS_OIDC_GROUP_ROLE_MAP", ""))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.group_roles.step"),
			i18n.Text("initwizard.deferred.group_roles.effect"),
			i18n.Text("initwizard.deferred.group_roles.finish"))
	case err != nil:
		return err
	default:
		iv.Cfg.OIDC.GroupRoleMap = mapping
	}
	return nil
}

func (iv *Interviewer) askGitHubOAuth() error {
	var err error
	if iv.Cfg.GitHub.ClientID, err = iv.P.Text(i18n.Text("initwizard.prompts.oauth_client_id"), iv.def("OCTOPUS_GITHUB_CLIENT_ID", "")); err != nil {
		return err
	}
	secret, err := iv.P.Secret(i18n.Text("initwizard.prompts.oauth_client_secret"))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.GitHub.ClientSecret = iv.def("OCTOPUS_GITHUB_CLIENT_SECRET", "")
	case err != nil:
		return err
	default:
		iv.Cfg.GitHub.ClientSecret = secret
	}
	if iv.Cfg.GitHub.ClientID == "" || iv.Cfg.GitHub.ClientSecret == "" {
		return apperr.Invalid("initwizard.errors.github_oauth_half_credentials")
	}
	return nil
}

func (iv *Interviewer) askDatabase() error {
	blurb := i18n.Text("initwizard.sections.database.blurb")
	if iv.Cfg.Mode == ModeKubernetes {
		blurb = i18n.Text("initwizard.sections.database.blurb_kubernetes")
	}
	iv.section(i18n.Text("initwizard.sections.database.title"), blurb)

	if iv.Cfg.Mode == ModeDocker {
		return iv.askDockerDatabase()
	}
	dsn, err := iv.P.Text(i18n.Text("initwizard.prompts.daemon_dsn"), iv.def("OCTOPUS_DATABASE_URL", ""))
	if err != nil {
		return err
	}
	if dsn == "" {
		return apperr.Invalid("initwizard.errors.dsn_required_chart")
	}
	iv.Cfg.DatabaseURL = dsn
	iv.Cfg.BootstrapDSN = dsn
	iv.checkDatabaseReachable()
	return nil
}

func (iv *Interviewer) askDockerDatabase() error {
	bundled, err := iv.P.YesNo(i18n.Text("initwizard.prompts.use_bundled_postgres"), true)
	if err != nil {
		return err
	}
	if bundled {
		var password string
		if password, err = iv.bundledPGPassword(); err != nil {
			return err
		}
		iv.Cfg.PGPassword = password
		iv.Cfg.PGHostPort = iv.choosePGHostPort()
		iv.Cfg.DatabaseURL = bundledDSN(password, "pg:5432")
		iv.Cfg.BootstrapDSN = bundledDSN(password, bundledHostAddrOn(iv.Cfg.PGHostPort))
		return nil
	}
	dsn, err := iv.P.Text(i18n.Text("initwizard.prompts.dsn"), iv.def("OCTOPUS_DATABASE_URL", ""))
	if err != nil {
		return err
	}
	if dsn == "" {
		return apperr.Invalid("initwizard.errors.dsn_required")
	}
	// Compose refuses to interpolate the stack while pg's required password is unset, even if the
	// daemon never uses pg.
	if iv.Cfg.PGPassword, err = iv.bundledPGPassword(); err != nil {
		return err
	}
	iv.Cfg.DatabaseURL = dsn
	iv.Cfg.BootstrapDSN = dsn
	iv.checkDatabaseReachable()
	return nil
}

// A rerun keeps its port: a busy 5433 there is most likely this install's own Postgres. A new install
// moves off a busy 5433, so `thulu init` never talks to somebody else's server on it.
func (iv *Interviewer) choosePGHostPort() string {
	if port := iv.def(pgHostPortEnv, ""); port != "" {
		return port
	}
	if iv.PortFree == nil || iv.hasBundledPG() || iv.PortFree(defaultPGHostPort) {
		return ""
	}
	for port := defaultPGHostPort + 1; port < defaultPGHostPort+100; port++ {
		if iv.PortFree(port) {
			iv.P.narrate("initwizard.cli.pg_port_moved", "busy", defaultPGHostPort, "port", port)
			return strconv.Itoa(port)
		}
	}
	return ""
}

func (iv *Interviewer) hasBundledPG() bool {
	return iv.def("OCTOPUS_PG_PASSWORD", "") != "" || bundledPasswordFromDSN(iv.def("OCTOPUS_DATABASE_URL", "")) != ""
}

// Older installs carry the password only inside OCTOPUS_DATABASE_URL; keep it, since Postgres fixed
// it at volume init.
func (iv *Interviewer) bundledPGPassword() (string, error) {
	if existing := iv.def("OCTOPUS_PG_PASSWORD", ""); existing != "" {
		return existing, nil
	}
	if legacy := bundledPasswordFromDSN(iv.def("OCTOPUS_DATABASE_URL", "")); legacy != "" {
		if legacy == legacyPGPassword {
			iv.P.narrate("initwizard.cli.legacy_postgres_password", "compose_file", iv.Cfg.ComposeFile)
		}
		return legacy, nil
	}
	return GenerateSecret()
}

const legacyPGPassword = "octopus"

func bundledDSN(password, hostPort string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("octopus", password),
		Host:     hostPort,
		Path:     "/octopus",
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func bundledPasswordFromDSN(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.User == nil || u.Hostname() != "pg" {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

// Warn only: an external Postgres firewalled from this host but open to the cluster is a normal
// setup.
func (iv *Interviewer) checkDatabaseReachable() {
	if err := ProbePostgres(iv.Ctx, iv.Cfg.BootstrapDSN); err != nil {
		iv.P.narrate("initwizard.cli.database_unreachable", "err", apperr.Text(err))
		return
	}
	iv.P.narrate("initwizard.cli.database_reachable")
}

func (iv *Interviewer) askNetwork() error {
	iv.section(i18n.Text("initwizard.sections.network.title"), i18n.Text("initwizard.sections.network.blurb"))

	fallback := "localhost"
	sayKey, errKey := "initwizard.cli.invalid_domain", "initwizard.errors.invalid_domain"
	switch {
	case iv.Cfg.Mode == ModeKubernetes:
		fallback = "thulu.example.com"
	case behindCaddy(iv.Cfg.ComposeFile):
		fallback = ""
		sayKey, errKey = "initwizard.cli.public_domain_required", "initwizard.errors.public_domain_required"
	}
	domain, err := iv.askChecked(i18n.Text("initwizard.prompts.domain"), iv.def("OCTOPUS_DOMAIN", fallback),
		invalidDomain, sayKey, errKey)
	if err != nil {
		return err
	}
	iv.Cfg.Domain = domain

	scheme := "https"
	if isLocalHost(domain) {
		scheme = "http"
	}
	origin := scheme + "://" + domain
	if iv.Cfg.Mode == ModeDocker && isLocalHost(domain) {
		origin = "http://localhost:8585"
		if iv.Cfg.ComposeFile == devComposeFile {
			origin = "http://localhost:4321,http://localhost:8585"
		}
	}
	origins, err := iv.askOrigins(origin)
	if err != nil {
		return err
	}
	iv.Cfg.PublicOrigin = normalizeOrigins(origins)
	if iv.Cfg.ComposeFile == devComposeFile && iv.Cfg.PublicOrigin != anyOrigin && !strings.Contains(iv.Cfg.PublicOrigin, ":4321") {
		iv.Cfg.PublicOrigin = strings.TrimSuffix(iv.Cfg.PublicOrigin, ",") + ",http://localhost:4321"
		iv.P.narrate("initwizard.cli.added_vite_origin")
	}
	if proxyErr := iv.askTrustedProxies(); proxyErr != nil {
		return proxyErr
	}
	if bindErr := iv.askBind(); bindErr != nil {
		return bindErr
	}
	base := callbackBase(iv.Cfg.PublicOrigin, scheme+"://"+domain)
	if iv.Cfg.Identity == IdentityOIDC {
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.register_redirect_uri", "url", base+"/auth/oidc/callback")
	}
	if iv.Cfg.Identity == IdentityGitHub {
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.register_oauth_callback", "url", base+"/github/oauth/callback")
	}
	return nil
}

func (iv *Interviewer) askOrigins(def string) (string, error) {
	if iv.Cfg.Mode == ModeDocker && isLocalHost(iv.Cfg.Domain) {
		previous := iv.def("OCTOPUS_ALLOWED_ORIGINS", "")
		if _, bad := invalidOrigin(previous); previous == "" || bad {
			return def, nil
		}
		return previous, nil
	}
	return iv.askChecked(i18n.Text("initwizard.prompts.allowed_origins"), iv.def("OCTOPUS_ALLOWED_ORIGINS", def),
		invalidOrigin, "initwizard.cli.invalid_origin", "initwizard.errors.invalid_origin")
}

// Prefer the :8585 origin: the dev stack's Vite origin on 4321 does not serve the callback, and a
// portless callback hits port 80.
func callbackBase(origins, fallback string) string {
	if origins == anyOrigin {
		return fallback
	}
	first := ""
	for _, o := range strings.Split(origins, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if strings.HasSuffix(o, ":8585") {
			return o
		}
		if first == "" {
			first = o
		}
	}
	if first == "" {
		return fallback
	}
	return first
}

func (iv *Interviewer) askChecked(label, def string, check func(string) (string, bool), sayKey, errKey string) (string, error) {
	for {
		answer, err := iv.P.Text(label, def)
		if err != nil {
			return "", err
		}
		entry, bad := check(answer)
		if !bad {
			return answer, nil
		}
		if iv.P.cannotAskAgain() {
			return "", apperr.Invalid(errKey, "entry", entry)
		}
		iv.P.narrate(sayKey, "entry", entry)
	}
}

func invalidDomain(domain string) (string, bool) {
	d := strings.TrimSpace(domain)
	return domain, d == "" || strings.ContainsAny(d, "/:@?# \t")
}

// Must match middleware/cors.go: "*" means every origin only on its own, never inside a list.
const anyOrigin = "*"

func invalidOrigin(list string) (string, bool) {
	if strings.TrimSpace(list) == anyOrigin {
		return "", false
	}
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		u, err := url.Parse(part)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			strings.TrimSuffix(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return part, true
		}
	}
	return "", false
}

func normalizeOrigins(list string) string {
	if strings.TrimSpace(list) == anyOrigin {
		return anyOrigin
	}
	var out []string
	for _, part := range strings.Split(list, ",") {
		if part = strings.TrimRight(strings.TrimSpace(part), "/"); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ",")
}

// "private" is dangerous: agents sit on a private network too and could spoof the client IP the
// audit log and rate limiter see.
func (iv *Interviewer) askTrustedProxies() error {
	previous := strings.TrimSpace(iv.def("OCTOPUS_TRUSTED_PROXIES", ""))
	if strings.EqualFold(previous, "private") {
		iv.P.narrate("initwizard.cli.dropped_private_proxies")
		previous = ""
	}

	var label string
	switch {
	case iv.Cfg.Mode == ModeKubernetes:
		label = i18n.Text("initwizard.prompts.ingress_proxies")
	case iv.Cfg.ComposeFile == standaloneComposeFile && !isLocalHost(iv.Cfg.Domain):
		label = i18n.Text("initwizard.prompts.reverse_proxies")
	case iv.Cfg.ComposeFile == standaloneComposeFile, behindCaddy(iv.Cfg.ComposeFile):
		// Localhost has nothing in front; behind Caddy the compose stack already trusts Caddy's
		// network subnet. Keep any previous value.
		iv.Cfg.TrustedProxies = previous
		return nil
	default:
		iv.Cfg.TrustedProxies = ""
		return nil
	}

	for {
		answer, err := iv.P.TextOrSkip(label, previous)
		switch {
		case errors.Is(err, ErrSkipped):
			iv.Cfg.TrustedProxies = ""
			return nil
		case err != nil:
			return err
		}
		if bad := invalidProxyEntry(answer); bad != "" {
			if iv.P.cannotAskAgain() {
				return apperr.Invalid("initwizard.errors.invalid_proxy", "entry", bad)
			}
			iv.P.narrate("initwizard.cli.invalid_proxy", "entry", bad)
			continue
		}
		if strings.Contains(strings.ToLower(answer), "private") {
			iv.P.narrate("initwizard.cli.private_proxies_warning")
		}
		iv.Cfg.TrustedProxies = answer
		return nil
	}
}

// Same grammar as internal/middleware/realip.go.
func invalidProxyEntry(list string) string {
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "private") {
			continue
		}
		if _, _, err := net.ParseCIDR(part); err == nil {
			continue
		}
		if net.ParseIP(part) != nil {
			continue
		}
		return part
	}
	return ""
}

// Nothing fronts the standalone stack, so with auth disabled the bind is the whole access control
// and is forced to loopback.
func (iv *Interviewer) askBind() error {
	if iv.Cfg.ComposeFile != standaloneComposeFile {
		return nil
	}
	if iv.Cfg.DevMode() {
		iv.Cfg.Bind = "127.0.0.1"
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.bind_forced")
		return nil
	}
	bind, err := iv.P.Text(i18n.Text("initwizard.prompts.bind"), iv.def("OCTOPUS_BIND", "127.0.0.1"))
	if err != nil {
		return err
	}
	iv.Cfg.Bind = bind
	if !isLocalHost(bind) {
		iv.P.narrate("initwizard.cli.plain_http_warning", "bind", bind)
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.tls.step"),
			i18n.Text("initwizard.deferred.tls.effect", "bind", bind),
			i18n.Text("initwizard.deferred.tls.finish", "file", caddyComposeFile))
	}
	return nil
}

func (iv *Interviewer) askKubernetes() error {
	if iv.Cfg.Mode != ModeKubernetes {
		return nil
	}
	iv.section(i18n.Text("initwizard.sections.cluster.title"), i18n.Text("initwizard.sections.cluster.blurb"))

	// Default to the previous release: Enter on "octopus" would install a second release beside it,
	// with its own credential key for the same database.
	prev := iv.PreviousCluster
	namespace, release := "octopus", "octopus"
	if prev.Release != "" {
		release = prev.Release
		if prev.Namespace != "" {
			namespace = prev.Namespace
		}
	}
	var err error
	if iv.Cfg.Namespace, err = iv.P.Text(i18n.Text("initwizard.prompts.namespace"), namespace); err != nil {
		return err
	}
	if iv.Cfg.Release, err = iv.P.Text(i18n.Text("initwizard.prompts.helm_release"), release); err != nil {
		return err
	}
	opts := []Option{
		{Value: "kata-clh", Label: i18n.Text("initwizard.options.isolation_kata.label"),
			Detail: i18n.Text("initwizard.options.isolation_kata.detail")},
		{Value: "gvisor", Label: i18n.Text("initwizard.options.isolation_gvisor.label"),
			Detail: i18n.Text("initwizard.options.isolation_gvisor.detail")},
		{Value: "", Label: i18n.Text("initwizard.options.isolation_default.label"),
			Detail: i18n.Text("initwizard.options.isolation_default.detail")},
	}
	isolation := 0
	for i, o := range opts {
		if prev.Release != "" && o.Value == prev.RuntimeClass {
			isolation = i
		}
	}
	runtimeClass, err := iv.P.Choice(i18n.Text("initwizard.prompts.agent_isolation"), opts, isolation)
	if err != nil {
		return err
	}
	iv.Cfg.RuntimeClass = runtimeClass
	iv.warnMissingRuntimeClass(runtimeClass)

	image, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.agent_image"), prev.AgentImage)
	if err != nil && !errors.Is(err, ErrSkipped) {
		return err
	}
	iv.Cfg.AgentImage = image
	return nil
}

func (iv *Interviewer) warnMissingRuntimeClass(name string) {
	if name == "" {
		return
	}
	present, err := RuntimeClassPresent(iv.Ctx, name)
	switch {
	case err != nil:
		iv.P.narrate("initwizard.cli.runtimeclass_check_failed", "name", name, "err", apperr.Text(err))
	case !present:
		iv.P.narrate("initwizard.cli.runtimeclass_missing", "name", name)
	default:
		iv.P.narrate("initwizard.cli.runtimeclass_present", "name", name)
	}
}

func (iv *Interviewer) askMCP() error {
	iv.section(i18n.Text("initwizard.sections.mcp.title"), i18n.Text("initwizard.sections.mcp.blurb"))
	iv.P.narrate("initwizard.cli.sandbox_builtin")
	iv.P.Printf("")

	previous := iv.previousMCP()
	opts := make([]Option, 0, len(NativeMCPServers))
	checked := []int{}
	for i, s := range NativeMCPServers {
		opts = append(opts, Option{Value: s.Name, Label: s.Name, Detail: s.Summary()})
		if containsName(previous, s.Name) {
			checked = append(checked, i)
		}
	}
	selected, err := iv.P.MultiChoice(i18n.Text("initwizard.prompts.mcp_servers"), opts, checked)
	if err != nil {
		return err
	}
	iv.Cfg.MCPServers = selected
	if iv.Cfg.ImageRegistry == "" {
		iv.Cfg.ImageRegistry = imageRegistryOverride()
	}

	if len(selected) == 0 {
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.mcp.step"), i18n.Text("initwizard.deferred.mcp.effect"),
			i18n.Text("initwizard.deferred.mcp.finish"))
		return nil
	}
	if containsName(selected, "thulu-cloud") {
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.thulu_cloud_note")
	}
	if containsName(selected, "thulu-figma") {
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.thulu_figma_note")
	}
	return nil
}

func (iv *Interviewer) previousMCP() []string {
	if iv.Existing != nil {
		if raw := iv.Existing.Get(mcpServersKey); raw != "" {
			return parseMCPSelection(raw)
		}
	}
	if iv.PreviousMCP != nil {
		return iv.PreviousMCP
	}
	return defaultMCPSelection()
}

func (iv *Interviewer) askLicense() error {
	iv.section(i18n.Text("initwizard.sections.license.title"), i18n.Text("initwizard.sections.license.blurb"))

	key, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.license_key"), iv.def("OCTOPUS_LICENSE_KEY", ""))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.license.step"), i18n.Text("initwizard.deferred.license.effect"),
			i18n.Text("initwizard.deferred.license.finish"))
	case err != nil:
		return err
	default:
		iv.Cfg.LicenseKey = key
	}
	return nil
}

func (iv *Interviewer) askWorkspace() error {
	iv.section(i18n.Text("initwizard.sections.workspace.title"), i18n.Text("initwizard.sections.workspace.blurb"))

	var err error
	// The default name must not be translated: the slug derives from it and a rerun in another
	// locale must find the same workspace.
	defaultName := i18n.Text("initwizard.labels.default_workspace")
	if iv.PreviousWorkspace.Name != "" {
		defaultName = iv.PreviousWorkspace.Name
	}
	if iv.Cfg.WorkspaceName, err = iv.P.Text(i18n.Text("initwizard.prompts.workspace_name"), defaultName); err != nil {
		return err
	}
	iv.Cfg.WorkspaceSlug = iv.workspaceSlug()
	// The config is written before the bootstrap; keep the current operator workspace so a
	// bootstrap that cannot reach the database does not leave the daemon without one.
	iv.Cfg.WorkspaceID = iv.def("OCTOPUS_OPERATOR_WORKSPACE", iv.PreviousWorkspace.ID)
	iv.Cfg.OperatorEmail, err = iv.P.Text(i18n.Text("initwizard.prompts.owner_email"), iv.defaultOperatorEmail())
	if err != nil {
		return err
	}
	if iv.Cfg.Identity == IdentityOIDC {
		iv.P.Printf("")
		iv.P.narrate("initwizard.cli.bootstrap_owner_note")
	}
	if iv.Cfg.Identity == IdentityNone {
		return nil
	}
	// Read back explicitly: ApplyToEnv would otherwise unset a landing workspace set by hand.
	iv.Cfg.OIDC.DefaultWorkspace = iv.def("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	return iv.askAdminEmail()
}

func (iv *Interviewer) workspaceSlug() string {
	if p := iv.PreviousWorkspace; p.Slug != "" && p.Name == iv.Cfg.WorkspaceName {
		return p.Slug
	}
	if slug := slugify(iv.Cfg.WorkspaceName); slug != "" {
		return slug
	}
	return slugify(i18n.Text("initwizard.labels.default_workspace"))
}

func (iv *Interviewer) askAdminEmail() error {
	for {
		admin, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.admin_email"), "")
		switch {
		case errors.Is(err, ErrSkipped):
			iv.Cfg.Defer(i18n.Text("initwizard.deferred.admin_email.step"),
				i18n.Text("initwizard.deferred.admin_email.effect"),
				i18n.Text("initwizard.deferred.admin_email.finish"))
			return nil
		case err != nil:
			return err
		}
		admin = strings.ToLower(strings.TrimSpace(admin))
		if !strings.Contains(admin, "@") {
			iv.P.narrate("initwizard.cli.admin_email_invalid", "email", admin)
			continue
		}
		if strings.EqualFold(admin, iv.Cfg.OperatorEmail) {
			iv.P.narrate("initwizard.cli.admin_email_is_owner", "email", admin)
			continue
		}
		iv.Cfg.AdminEmail = admin
		return nil
	}
}

// With a login configured the bootstrap owner must be an address no IdP will assert, so no sign-in
// collides with it.
func (iv *Interviewer) defaultOperatorEmail() string {
	if iv.Cfg.Identity != IdentityNone {
		return "bootstrap@octopus.local"
	}
	out, err := exec.CommandContext(iv.Ctx, "git", "config", "--get", "user.email").Output()
	if email := strings.TrimSpace(string(out)); err == nil && email != "" {
		return email
	}
	return "bootstrap@octopus.local"
}

func (iv *Interviewer) askPostBoot() error {
	iv.section(i18n.Text("initwizard.sections.post_boot.title"), i18n.Text("initwizard.sections.post_boot.blurb"))

	if err := iv.askGitCredential(); err != nil {
		return err
	}
	if iv.Cfg.Quick {
		iv.Cfg.GitHubURL = iv.def("OCTOPUS_GITHUB_URL", "")
		iv.Cfg.GitHubAPIURL = iv.def("OCTOPUS_GITHUB_API_URL", "")
		return nil
	}
	return iv.askGitHubServer()
}

func (iv *Interviewer) askGitHubServer() error {
	for {
		answer, err := iv.P.TextOrSkip(i18n.Text("initwizard.prompts.github_server"), iv.def("OCTOPUS_GITHUB_URL", "https://github.com"))
		switch {
		case errors.Is(err, ErrSkipped):
			iv.Cfg.GitHubURL, iv.Cfg.GitHubAPIURL = "", ""
			return nil
		case err != nil:
			return err
		}
		web, ok := githubWebURL(answer)
		if !ok {
			if iv.P.cannotAskAgain() {
				return apperr.Invalid("initwizard.errors.invalid_github_url", "url", answer)
			}
			iv.P.narrate("initwizard.cli.invalid_github_url", "url", answer)
			continue
		}
		if strings.EqualFold(web, "https://github.com") {
			iv.Cfg.GitHubURL, iv.Cfg.GitHubAPIURL = "", ""
			return nil
		}
		iv.Cfg.GitHubURL = web
		iv.Cfg.GitHubAPIURL = web + "/api/v3"
		previousWeb := strings.TrimRight(iv.def("OCTOPUS_GITHUB_URL", ""), "/")
		if api := strings.TrimRight(iv.def("OCTOPUS_GITHUB_API_URL", ""), "/"); api != "" && web == previousWeb {
			iv.Cfg.GitHubAPIURL = api
		}
		iv.P.narrate("initwizard.cli.github_api", "url", iv.Cfg.GitHubAPIURL)
		return nil
	}
}

func githubWebURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", false
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), true
}

func (iv *Interviewer) askGitCredential() error {
	token, err := iv.P.Secret(i18n.Text("initwizard.prompts.git_token"))
	switch {
	case errors.Is(err, ErrSkipped):
		iv.Cfg.Defer(i18n.Text("initwizard.deferred.git_credential.step"),
			i18n.Text("initwizard.deferred.git_credential.skipped_effect"),
			i18n.Text("initwizard.deferred.git_credential.skipped_finish"))
		return nil
	case err != nil:
		return err
	}
	iv.Cfg.GitToken = token
	iv.Cfg.GitProvider = "github"
	return nil
}

func isLocalHost(domain string) bool {
	host := strings.ToLower(strings.TrimSpace(domain))
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".local")
}

func slugify(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() > 0:
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
