package auth

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

type loginConfigOIDC struct {
	Name   string `json:"name"`
	Issuer string `json:"issuer"`
}

type loginConfig struct {
	OIDC               *loginConfigOIDC `json:"oidc"`
	GitHub             bool             `json:"github"`
	AllowedDomains     []string         `json:"allowed_domains"`
	AllowedGroups      []string         `json:"allowed_groups"`
	GroupsClaim        string           `json:"groups_claim"`
	SignupMode         string           `json:"signup_mode"`
	AllowedEmails      []string         `json:"allowed_emails"`
	PersonalWorkspaces bool             `json:"personal_workspaces"`
	MaxOwnedWorkspaces int              `json:"max_owned_workspaces"`
	DefaultWorkspace   string           `json:"default_workspace"`
	DefaultRole        Role             `json:"default_role"`
	GroupRoleMap       string           `json:"group_role_map"`
	AutoLink           bool             `json:"autolink"`
}

func currentLoginConfig() loginConfig {
	c := loginConfig{
		AllowedDomains:     nonNil(csvEnv("OCTOPUS_SSO_ALLOWED_DOMAINS")),
		AllowedGroups:      nonNil(csvEnv("OCTOPUS_SSO_ALLOWED_GROUPS")),
		GroupsClaim:        oidcGroupsClaim(),
		SignupMode:         SignupMode(),
		AllowedEmails:      nonNil(seedAllowedEmails()),
		PersonalWorkspaces: PersonalWorkspacesAllowed(),
		MaxOwnedWorkspaces: MaxOwnedWorkspaces(),
		DefaultWorkspace:   strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SSO_DEFAULT_WORKSPACE"))),
		GroupRoleMap:       strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_GROUP_ROLE_MAP")),
		AutoLink:           os.Getenv("OCTOPUS_ALLOW_SSO_AUTOLINK") == "1",
		GitHub:             strings.TrimSpace(os.Getenv("OCTOPUS_GITHUB_CLIENT_ID")) != "",
	}
	if role, err := SSODefaultRole(); err == nil {
		c.DefaultRole = role
	}
	if issuer := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_ISSUER")); issuer != "" {
		name := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_NAME"))
		if name == "" {
			name = "SSO"
		}
		c.OIDC = &loginConfigOIDC{Name: name, Issuer: issuer}
	}
	return c
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Operator only: the allowed domains and groups reveal who the install's tenants are.
func (h *Handler) handleLoginConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireOperator(w, r, PermMembersRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(currentLoginConfig())
}
