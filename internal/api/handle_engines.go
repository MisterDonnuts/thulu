package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

type EngineInfo struct {
	Name      string        `json:"name"`
	Label     string        `json:"label"`
	Provider  string        `json:"provider"`
	KeyKinds  []KeyKindView `json:"key_kinds"`
	Models    []string      `json:"models,omitempty"`
	IsDefault bool          `json:"is_default"`

	Effort     bool     `json:"effort"`
	BaseURL    bool     `json:"base_url"`
	OrgID      bool     `json:"org_id"`
	AuthHeader bool     `json:"auth_header"`
	Providers  []string `json:"providers"`
	TurnBudget bool     `json:"turn_budget"`

	// HasKey/KeyHint stay for the init wizard and older clients.
	HasKey             bool   `json:"has_key"`
	KeyHint            string `json:"key_hint,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	DefaultProfileID   string `json:"default_profile_id,omitempty"`
	DefaultProfileName string `json:"default_profile_name,omitempty"`
	DefaultModel       string `json:"default_model,omitempty"`
	FallbackModel      string `json:"fallback_model,omitempty"`
	HostFallback       bool   `json:"host_fallback"`
}

// EnvVars and Clears are deliberately omitted: exposing them would leak how credentials are wired
// into runs.
type KeyKindView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Prefix    string `json:"prefix"`
	Federated bool   `json:"federated,omitempty"`
	OwnerOnly bool   `json:"owner_only,omitempty"`
}

type engineDescriptor struct {
	Name               string
	Label              string
	Provider           string
	KeyKinds           []engine.KeyKind
	Models             []string
	DefaultModel       string
	TurnBudget         bool
	IsDefault          bool
	Effort             bool
	BaseURL            bool
	OrgID              bool
	AuthHeader         bool
	Providers          []string
	FederationAudience string
}

func availableEngines(ctx context.Context) []engineDescriptor {
	out := make([]engineDescriptor, 0, len(engine.Known()))
	for _, name := range engine.Known() {
		eng, err := engine.Get(name)
		if err != nil {
			continue
		}
		caps := engine.CapabilitiesIn(ctx, eng)
		if caps.Provider == "" {
			continue
		}
		label := caps.Label
		if label == "" {
			label = name
		}
		out = append(out, engineDescriptor{
			Name:               name,
			Label:              label,
			Provider:           caps.Provider,
			KeyKinds:           caps.KeyKinds,
			Models:             caps.Models,
			DefaultModel:       caps.DefaultModel,
			TurnBudget:         caps.TurnBudget,
			IsDefault:          name == engine.DefaultName,
			Effort:             caps.EffortFlag,
			BaseURL:            caps.BaseURLEnv != "",
			OrgID:              caps.OrgIDEnv != "",
			AuthHeader:         caps.AuthorizationEnv != "",
			Providers:          caps.Providers(),
			FederationAudience: federationAudience(caps),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func engineByName(ctx context.Context, name string) (engineDescriptor, bool) {
	descs := availableEngines(ctx)
	for i := range descs {
		if descs[i].Name == name {
			return descs[i], true
		}
	}
	return engineDescriptor{}, false
}

func (s *Server) handleListEngines(w http.ResponseWriter, r *http.Request) {
	descs := availableEngines(r.Context())
	out := make([]EngineInfo, 0, len(descs))

	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	statuses, defaults := s.engineKeyState(r.Context(), wsID)
	hostFallback := s.hostLLMCredentialAllowed()
	owner := s.callerIsInstallationOwner(r)

	for i := range descs {
		d := &descs[i]
		st, hasKey := statuses[d.Name]
		info := engineInfo(d, defaults[d.Name], st, hasKey, owner)
		info.HostFallback = hostFallback
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"engines": out})
}

func (s *Server) hostLLMCredentialAllowed() bool {
	return s.taskDispatcher != nil && s.taskDispatcher.HostLLMCredentialAllowed()
}

func engineInfo(d *engineDescriptor, def *secrets.LLMProfile, st llmKeyStatusView, hasKey, owner bool) EngineInfo {
	info := EngineInfo{
		Name: d.Name, Label: d.Label, Provider: d.Provider,
		KeyKinds: keyKindViews(d.KeyKinds, owner), Models: d.Models, IsDefault: d.IsDefault,
		Effort: d.Effort, BaseURL: d.BaseURL, OrgID: d.OrgID,
		AuthHeader: d.AuthHeader, Providers: d.Providers,
		TurnBudget: d.TurnBudget, DefaultModel: d.DefaultModel,
		FallbackModel: d.DefaultModel,
	}
	if def != nil {
		info.DefaultProfileID = def.ID
		info.DefaultProfileName = def.Name
		if def.Model != "" {
			info.DefaultModel = def.Model
		}
	}
	if hasKey {
		info.HasKey = true
		info.KeyHint = st.hint
		info.UpdatedAt = st.updatedAt
	}
	return info
}

func (s *Server) engineKeyState(ctx context.Context, wsID string) (map[string]llmKeyStatusView, map[string]*secrets.LLMProfile) {
	statuses := map[string]llmKeyStatusView{}
	defaults := map[string]*secrets.LLMProfile{}
	if s.secretsStore == nil {
		return statuses, defaults
	}
	if m, err := s.secretsStore.ListLLMKeys(ctx, wsID); err == nil {
		for eng, st := range m {
			statuses[eng] = llmKeyStatusView{hint: st.Hint, updatedAt: st.UpdatedAt.Format(http.TimeFormat)}
		}
	}
	if ps, err := s.secretsStore.ListLLMProfiles(ctx, wsID); err == nil {
		for _, p := range ps {
			if p.IsDefault {
				defaults[p.Engine] = p
			}
		}
	}
	return statuses, defaults
}

type llmKeyStatusView struct {
	hint      string
	updatedAt string
}

func keyKindViews(kinds []engine.KeyKind, owner bool) []KeyKindView {
	out := make([]KeyKindView, 0, len(kinds))
	for _, k := range kinds {
		if (k.OwnerOnly && !owner) || !k.Usable() {
			continue
		}
		out = append(out, KeyKindView{ID: k.ID, Label: k.Label, Prefix: k.Prefix, Federated: k.Federated, OwnerOnly: k.OwnerOnly})
	}
	return out
}

func federationAudience(caps engine.Capabilities) string {
	if _, ok := caps.FederatedKeyKind(); !ok {
		return ""
	}
	return caps.FederationAudience
}
