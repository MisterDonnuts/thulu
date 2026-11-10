package auth

import (
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var ErrSSONotAllowed error = apperr.Forbidden("auth.errors.sso_not_allowed")

var ErrSSOEmailUnverified error = apperr.Forbidden("auth.errors.sso_newcomer_email_unverified")

func ssoAdmit(id ssoIdentity) error {
	if domains := csvEnv("OCTOPUS_SSO_ALLOWED_DOMAINS"); len(domains) > 0 {
		if !id.EmailVerified {
			return apperr.Wrap(ErrSSONotAllowed, "auth.errors.sso_email_not_vouched")
		}
		if !emailInDomains(id.Email, domains) {
			return apperr.Wrap(ErrSSONotAllowed, "auth.errors.sso_domain_not_allowed", "domain", emailDomain(id.Email))
		}
	}
	if groups := csvEnv("OCTOPUS_SSO_ALLOWED_GROUPS"); len(groups) > 0 {
		if !anyGroup(id.Groups, groups) {
			return apperr.Wrap(ErrSSONotAllowed, "auth.errors.sso_group_not_allowed")
		}
	}
	return nil
}

func PersonalWorkspacesAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	// An enterprise IdP means one organization: strangers who can sign in must not found tenants.
	// SaaS mode is exempt; there the signup allowlist decides who gets in.
	return SaaSMode() || strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_ISSUER")) == ""
}

// Outside SaaS, GitHub sign-in and sign-up are open by default, so a stranger who reaches the
// install must not found a workspace on login: only the first login does, and later newcomers
// wait for an invitation unless OCTOPUS_ALLOW_PERSONAL_WORKSPACES says on. A member who is signed
// in still creates workspaces from the menu; there is no limit on how many.
func newcomersFoundWorkspaces() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return SaaSMode()
}

// Owner is refused: a workspace has exactly one owner.
func SSODefaultRole() (Role, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SSO_DEFAULT_ROLE")))
	if raw == "" {
		return RoleViewer, nil
	}
	switch r := Role(raw); r {
	case RoleViewer, RoleDeveloper, RoleAdmin:
		return r, nil
	case RoleOwner:
		return "", apperr.Invalid("auth.errors.sso_default_role_owner")
	}
	return "", apperr.Invalid("auth.errors.sso_default_role_invalid", "role", raw)
}

func csvEnv(name string) []string {
	var out []string
	for _, part := range strings.Split(os.Getenv(name), ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// Exact match only: subdomains are not implied, and suffix matching would admit
// "acme.com.attacker.io".
func emailInDomains(email string, domains []string) bool {
	d := emailDomain(email)
	if d == "" {
		return false
	}
	for _, allowed := range domains {
		if d == strings.ToLower(strings.TrimPrefix(allowed, "@")) {
			return true
		}
	}
	return false
}

func anyGroup(have, allowed []string) bool {
	set := make(map[string]bool, len(have))
	for _, g := range have {
		set[strings.TrimSpace(g)] = true
	}
	for _, g := range allowed {
		if set[g] {
			return true
		}
	}
	return false
}
