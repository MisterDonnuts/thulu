package credentials

import (
	"context"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// tools/list and CallTool both go through Authorize; a second code path would let a tool be hidden
// from the catalog yet callable by name. Default is deny.

// Reason values are a stable wire identifier: the dashboard groups on them and the MCP servers
// branch on them.
type Reason string

const (
	ReasonOK                 Reason = "ok"
	ReasonNoCredentialNeeded Reason = "no_credential_needed" //nolint:gosec // G101: an outcome code, not a credential
	// Covers both "no such credential" and "not granted to your task" on purpose: splitting them
	// lets an agent enumerate other tenants' environments.
	// Same reasoning as the 404-on-scope-miss in internal/api/scope.go.
	ReasonNoCredential Reason = "no_credential" //nolint:gosec // G101: an outcome code, not a credential
	// Never guess between candidates: they are usually staging and production.
	ReasonAmbiguous           Reason = "ambiguous_credential"
	ReasonProviderUnsupported Reason = "provider_unsupported"
	ReasonDisabled            Reason = "credential_disabled"
	ReasonExpired             Reason = "credential_expired"
	ReasonInvalid             Reason = "credential_invalid"
	ReasonPolicyDisabled      Reason = "tool_disabled_by_policy"
	ReasonMissingCapability   Reason = "missing_capability"
	ReasonStageBlocked        Reason = "tool_not_in_stage_allowlist"
	ReasonHeaderMismatch      Reason = "header_mismatch"
)

type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  Reason `json:"reason"`
	// Returned to the agent. Config refusals (policy, capability, stage) may be specific; existence
	// refusals must stay generic (see ReasonNoCredential).
	Detail string `json:"detail,omitempty"`

	CredentialID string   `json:"credential_id,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Missing      []string `json:"missing,omitempty"`
}

type Request struct {
	// Built server-side from the task the API key is scoped to, never from anything the agent sent.
	Reach Reach

	TaskID string
	Agent  string
	Stage  string

	Server string
	Tool   string

	CredentialID string

	// When set, only a credential granted to Subject may serve; no match is a refusal, never a
	// fallback to another credential.
	Subject *Grant

	// Same entry format as thulu.mcp.default.<stage> and the dispatch gate. nil = unrestricted;
	// empty non-nil = nothing allowed.
	StageTools []string
}

type Authorizer struct {
	store   *Store
	catalog *Catalog
}

func NewAuthorizer(store *Store, catalog *Catalog) *Authorizer {
	return &Authorizer{store: store, catalog: catalog}
}

func (a *Authorizer) Catalog() *Catalog { return a.catalog }

func (a *Authorizer) Authorize(ctx context.Context, req Request) (Decision, error) {
	// Stage allowlist runs first so a tool the agent was never offered never triggers a credential
	// lookup.
	if !a.stageFilter(req.StageTools)(req.Server, req.Tool) {
		return Decision{
			Reason: ReasonStageBlocked,
			Detail: i18n.TCtx(ctx, "credentials.decisions.stage_blocked", "tool", req.Tool, "stage", req.Stage),
		}, nil
	}

	spec, ok := a.catalog.Lookup(req.Server, req.Tool)
	if !ok {
		return Decision{
			Allowed: true,
			Reason:  ReasonNoCredentialNeeded,
			Detail:  i18n.TCtx(ctx, "credentials.decisions.no_credential_needed"),
		}, nil
	}

	cred, dec, err := a.resolve(ctx, req, spec)
	if err != nil {
		return Decision{}, err
	}
	if cred == nil {
		return dec, nil
	}

	required, supported := spec.Requires[cred.Provider]
	if !supported {
		return Decision{
			Reason:       ReasonProviderUnsupported,
			CredentialID: cred.ID,
			Provider:     cred.Provider,
			Detail: i18n.TCtx(ctx, "credentials.decisions.provider_unsupported",
				"tool", req.Tool, "provider", cred.Provider, "supported", strings.Join(spec.Providers(), ", ")),
		}, nil
	}

	if d, bad := stateDecision(ctx, cred); bad {
		return d, nil
	}

	policy, err := a.store.ToolPolicy(ctx, cred.ID)
	if err != nil {
		return Decision{}, err
	}
	if enabled, hasOverride := policy[PolicyKey(req.Server, req.Tool)]; hasOverride && !enabled {
		return Decision{
			Reason:       ReasonPolicyDisabled,
			CredentialID: cred.ID,
			Provider:     cred.Provider,
			Detail: i18n.TCtx(ctx, "credentials.decisions.tool_switched_off",
				"tool", req.Tool, "credential", cred.Name),
		}, nil
	}

	if missing := cred.MissingCapabilities(required); len(missing) > 0 {
		return Decision{
			Reason:       ReasonMissingCapability,
			CredentialID: cred.ID,
			Provider:     cred.Provider,
			Missing:      missing,
			Detail: i18n.TCtx(ctx, "credentials.decisions.missing_capability",
				"credential", cred.Name, "missing", strings.Join(missing, ", ")),
		}, nil
	}

	return Decision{
		Allowed:      true,
		Reason:       ReasonOK,
		CredentialID: cred.ID,
		Provider:     cred.Provider,
	}, nil
}

func (a *Authorizer) resolve(ctx context.Context, req Request, spec ToolSpec) (*Credential, Decision, error) {
	reachable, err := a.store.Reachable(ctx, req.Reach)
	if err != nil {
		return nil, Decision{}, err
	}

	if req.CredentialID != "" {
		for _, c := range reachable {
			if c.ID == req.CredentialID {
				return c, Decision{}, nil
			}
		}
		// Nonexistent, other workspace, or not granted must all answer the same (see
		// ReasonNoCredential).
		return nil, Decision{Reason: ReasonNoCredential, Detail: noCredentialDetail(ctx)}, nil
	}

	if req.Subject != nil {
		if c := findGranted(reachable, *req.Subject); c != nil {
			return c, Decision{}, nil
		}
		return nil, Decision{Reason: ReasonNoCredential, Detail: noCredentialDetail(ctx)}, nil
	}

	var candidates []*Credential
	for _, c := range reachable {
		if _, ok := spec.Requires[c.Provider]; ok {
			candidates = append(candidates, c)
		}
	}
	switch len(candidates) {
	case 0:
		return nil, Decision{Reason: ReasonNoCredential, Detail: noCredentialDetail(ctx)}, nil
	case 1:
		return candidates[0], Decision{}, nil
	default:
		names := make([]string, 0, len(candidates))
		for _, c := range candidates {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		return nil, Decision{
			Reason: ReasonAmbiguous,
			Detail: i18n.TCtx(ctx, "credentials.decisions.ambiguous", "names", strings.Join(names, ", ")),
		}, nil
	}
}

func findGranted(reachable []*Credential, want Grant) *Credential {
	for _, c := range reachable {
		for _, g := range c.Grants {
			if g.SubjectType == want.SubjectType && g.SubjectID == want.SubjectID {
				return c
			}
		}
	}
	return nil
}

// Deliberately uninformative. See ReasonNoCredential.
func noCredentialDetail(ctx context.Context) string {
	return i18n.TCtx(ctx, "credentials.decisions.no_credential")
}

func stateDecision(ctx context.Context, c *Credential) (Decision, bool) {
	base := Decision{CredentialID: c.ID, Provider: c.Provider}
	switch {
	case c.Disabled:
		base.Reason = ReasonDisabled
		base.Detail = i18n.TCtx(ctx, "credentials.decisions.disabled", "credential", c.Name)
		return base, true
	case c.State == StateExpired:
		base.Reason = ReasonExpired
		base.Detail = i18n.TCtx(ctx, "credentials.decisions.expired",
			"credential", c.Name, "date", c.ExpiresAt.Format("2006-01-02"))
		return base, true
	case c.State == StateInvalid:
		base.Reason = ReasonInvalid
		base.Detail = i18n.TCtx(ctx, "credentials.decisions.invalid",
			"credential", c.Name, "provider", c.Provider)
		return base, true
	}
	return Decision{}, false
}

type ToolAvailability struct {
	Server   string   `json:"server"`
	Tool     string   `json:"tool"`
	Summary  string   `json:"summary,omitempty"`
	Requires []string `json:"requires,omitempty"`
	Decision Decision `json:"decision"`
}

type ListRequest struct {
	Reach        Reach
	TaskID       string
	Agent        string
	Stage        string
	Server       string
	CredentialID string
	StageTools   []string
}

func (a *Authorizer) ListAvailable(ctx context.Context, req ListRequest) ([]ToolAvailability, error) {
	out := []ToolAvailability{}
	for _, spec := range a.catalog.All() {
		if req.Server != "" && spec.Server != req.Server {
			continue
		}
		dec, err := a.Authorize(ctx, Request{
			Reach:        req.Reach,
			TaskID:       req.TaskID,
			Agent:        req.Agent,
			Stage:        req.Stage,
			Server:       spec.Server,
			Tool:         spec.Tool,
			CredentialID: req.CredentialID,
			StageTools:   req.StageTools,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, ToolAvailability{
			Server:   spec.Server,
			Tool:     spec.Tool,
			Summary:  spec.SummaryIn(ctx),
			Decision: dec,
		})
	}
	return out, nil
}

func (a *Authorizer) ToolMatrix(ctx context.Context, cred *Credential) ([]ToolAvailability, error) {
	policy, err := a.store.ToolPolicy(ctx, cred.ID)
	if err != nil {
		return nil, err
	}
	out := []ToolAvailability{}
	for _, spec := range a.catalog.ForProvider(cred.Provider) {
		out = append(out, ToolAvailability{
			Server:   spec.Server,
			Tool:     spec.Tool,
			Summary:  spec.SummaryIn(ctx),
			Requires: spec.Requires[cred.Provider],
			Decision: matrixDecision(ctx, cred, spec, policy),
		})
	}
	return out, nil
}

func matrixDecision(ctx context.Context, cred *Credential, spec ToolSpec, policy map[string]bool) Decision {
	base := Decision{CredentialID: cred.ID, Provider: cred.Provider}
	if enabled, ok := policy[PolicyKey(spec.Server, spec.Tool)]; ok && !enabled {
		base.Reason = ReasonPolicyDisabled
		base.Detail = i18n.TCtx(ctx, "credentials.decisions.matrix_policy_disabled")
		return base
	}
	if missing := cred.MissingCapabilities(spec.Requires[cred.Provider]); len(missing) > 0 {
		base.Reason = ReasonMissingCapability
		base.Missing = missing
		base.Detail = i18n.TCtx(ctx, "credentials.decisions.matrix_missing", "missing", strings.Join(missing, ", "))
		return base
	}
	base.Allowed = true
	base.Reason = ReasonOK
	return base
}

type ServerRequest struct {
	Reach  Reach
	TaskID string
	Agent  string
	Stage  string
	Server string
	// A name resolves only within the reach's single workspace, so one install-wide registration
	// uses each tenant's own credential.
	CredentialRef string
}

func (a *Authorizer) AuthorizeServer(ctx context.Context, req ServerRequest) (Decision, error) {
	ref := strings.TrimSpace(req.CredentialRef)
	if ref == "" {
		return Decision{Reason: ReasonNoCredential, Detail: noCredentialDetail(ctx)}, nil
	}
	reachable, err := a.store.Reachable(ctx, req.Reach)
	if err != nil {
		return Decision{}, err
	}
	var cred *Credential
	for _, c := range reachable {
		if c.ID == ref || c.Name == ref {
			cred = c
			break
		}
	}
	if cred == nil {
		return Decision{Reason: ReasonNoCredential, Detail: noCredentialDetail(ctx)}, nil
	}
	if d, bad := stateDecision(ctx, cred); bad {
		return d, nil
	}
	return Decision{
		Allowed:      true,
		Reason:       ReasonOK,
		CredentialID: cred.ID,
		Provider:     cred.Provider,
	}, nil
}
