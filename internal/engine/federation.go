package engine

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// JSON names follow Anthropic's token exchange request
// (platform.claude.com/docs/en/manage-claude/wif-reference).
type FederationConfig struct {
	RuleID           string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id"`
	// Anthropic's workspace (wrkspc_…), not a Thulu workspace.
	WorkspaceID string `json:"workspace_id,omitempty"`
}

func (f FederationConfig) Set() bool { return f != FederationConfig{} }

func (f FederationConfig) Trimmed() FederationConfig {
	return FederationConfig{
		RuleID:           strings.TrimSpace(f.RuleID),
		OrganizationID:   strings.TrimSpace(f.OrganizationID),
		ServiceAccountID: strings.TrimSpace(f.ServiceAccountID),
		WorkspaceID:      strings.TrimSpace(f.WorkspaceID),
	}
}

var orgUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// The CLI takes the federation path only when rule, organization, service account and token are all
// present.
func (f FederationConfig) Validate() error {
	switch {
	case !strings.HasPrefix(f.RuleID, "fdrl_") || len(f.RuleID) <= len("fdrl_"):
		return apperr.Invalid("engine.errors.federation_rule_id", "prefix", "fdrl_")
	case !orgUUID.MatchString(f.OrganizationID):
		return apperr.Invalid("engine.errors.federation_organization_id")
	case !strings.HasPrefix(f.ServiceAccountID, "svac_") || len(f.ServiceAccountID) <= len("svac_"):
		return apperr.Invalid("engine.errors.federation_service_account_id", "prefix", "svac_")
	case f.WorkspaceID != "" && (!strings.HasPrefix(f.WorkspaceID, "wrkspc_") || len(f.WorkspaceID) <= len("wrkspc_")):
		return apperr.Invalid("engine.errors.federation_workspace_id", "prefix", "wrkspc_")
	}
	return nil
}

func (f FederationConfig) Encode() string {
	if !f.Set() {
		return ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return string(b)
}

func DecodeFederation(raw string) (FederationConfig, error) {
	var f FederationConfig
	if strings.TrimSpace(raw) == "" {
		return f, nil
	}
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return FederationConfig{}, apperr.Wrap(err, "engine.errors.federation_invalid_json")
	}
	return f, nil
}

func (c Capabilities) FederatedKeyKind() (KeyKind, bool) {
	if c.FederationEnv == nil {
		return KeyKind{}, false
	}
	for _, k := range c.KeyKinds {
		if k.Federated {
			return k, true
		}
	}
	return KeyKind{}, false
}
