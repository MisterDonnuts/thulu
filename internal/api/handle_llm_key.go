package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

func validateKeyAgainstEngineKinds(desc engineDescriptor, wantKind, key string) error {
	if len(desc.KeyKinds) == 0 {
		return nil
	}
	if wantKind != "" {
		k, ok := engine.KeyKindByID(desc.KeyKinds, wantKind)
		if !ok {
			return apperr.Invalid("api.llm.errors.unknown_key_kind", "kind", wantKind, "engine", desc.Label)
		}
		if k.Prefix != "" && !strings.HasPrefix(key, k.Prefix) {
			return apperr.Invalid("api.llm.errors.key_prefix_mismatch", "engine", desc.Label, "kind", k.Label, "prefix", k.Prefix)
		}
		return nil
	}
	if _, ok := engine.MatchKeyKind(desc.KeyKinds, key); ok {
		return nil
	}
	prefixes := make([]string, 0, len(desc.KeyKinds))
	for _, k := range desc.KeyKinds {
		if k.Prefix != "" {
			prefixes = append(prefixes, k.Prefix)
		}
	}
	if len(prefixes) == 0 {
		return nil
	}
	return apperr.Invalid("api.llm.errors.key_prefix_unknown", "engine", desc.Label, "prefixes", strings.Join(prefixes, ", "))
}

func engineFromRequest(r *http.Request, bodyEngine string) string {
	if bodyEngine != "" {
		return bodyEngine
	}
	if q := strings.TrimSpace(r.URL.Query().Get("engine")); q != "" {
		return q
	}
	return engine.DefaultName
}

func (s *Server) handleGetLLMKey(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	st, err := s.secretsStore.StatusLLMKey(r.Context(), wsID, engineFromRequest(r, ""))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type setLLMKeyReq struct {
	Engine string `json:"engine"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
}

func (s *Server) handleSetLLMKey(w http.ResponseWriter, r *http.Request) {
	var req setLLMKeyReq
	if !decodeBody(w, r, &req) {
		return
	}
	engineName := engineFromRequest(r, req.Engine)
	desc, ok := engineByName(r.Context(), engineName)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.unknown_engine", "engine", engineName)
		return
	}
	key := strings.TrimSpace(req.Key)
	if key != "" {
		if err := validateKeyAgainstEngineKinds(desc, strings.TrimSpace(req.Kind), key); err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		if err := s.ownerOnlyCredential(r, desc, strings.TrimSpace(req.Kind), key); err != nil {
			writeError(w, r, http.StatusForbidden, err)
			return
		}
	}
	wsID, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.secretsStore.SetLLMKey(r.Context(), wsID, engineName, desc.Provider, key); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	st, err := s.secretsStore.StatusLLMKey(r.Context(), wsID, engineName)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDeleteLLMKey(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.secretsStore.DeleteLLMKey(r.Context(), wsID, engineFromRequest(r, "")); errors.Is(err, secrets.ErrProfileInUse) {
		writeErrKey(w, r, http.StatusConflict, "api.llm.errors.default_profile_in_use")
		return
	} else if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) llmKeyForWorkspace(ctx context.Context, workspaceID, engineName string) (key string, ok bool) {
	if s.secretsStore == nil || workspaceID == "" {
		return "", false
	}
	_, key, err := s.secretsStore.GetLLMKey(ctx, workspaceID, engineName)
	if errors.Is(err, secrets.ErrNotFound) || key == "" {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return key, true
}

func (s *Server) callerIsInstallationOwner(r *http.Request) bool {
	var members auth.MemberRoles
	if s.authStore != nil {
		members = s.authStore
	}
	return auth.IsInstallationOwner(r.Context(), members)
}

// Check both the named kind and the prefix-matched kind: profiles with a base_url skip prefix
// validation, but this gate must not.
func (s *Server) ownerOnlyCredential(r *http.Request, desc engineDescriptor, kind, key string) error {
	if key == "" && kind == "" {
		return nil
	}
	for _, k := range []func() (engine.KeyKind, bool){
		func() (engine.KeyKind, bool) { return engine.KeyKindByID(desc.KeyKinds, kind) },
		func() (engine.KeyKind, bool) { return engine.MatchKeyKind(desc.KeyKinds, key) },
	} {
		if off, ok := k(); ok && !off.Usable() {
			return apperr.Forbidden("api.llm.errors.kind_needs_kube_opt_in", "kind", off.Label, "env", off.KubeOptIn)
		}
	}
	gated, ok := engine.KeyKindByID(desc.KeyKinds, kind)
	if !ok || !gated.OwnerOnly {
		gated, ok = engine.MatchKeyKind(desc.KeyKinds, key)
	}
	if !ok || !gated.OwnerOnly || s.callerIsInstallationOwner(r) {
		return nil
	}
	return apperr.Forbidden("api.llm.errors.owner_only_kind", "kind", gated.Label, "env", OperatorWorkspaceEnv)
}

// Without this an admin could redirect the owner's stored token to another base_url, header or
// provider without ever sending it.
func (s *Server) ownerOnlyStored(r *http.Request, profile *secrets.LLMProfile) (int, error) {
	if s.callerIsInstallationOwner(r) {
		return http.StatusOK, nil
	}
	desc, ok := engineByName(r.Context(), profile.Engine)
	if !ok || !slices.ContainsFunc(desc.KeyKinds, func(k engine.KeyKind) bool { return k.OwnerOnly }) {
		return http.StatusOK, nil
	}
	key, err := s.secretsStore.GetLLMProfileCredential(r.Context(), profile.WorkspaceID, profile.ID)
	if errors.Is(err, secrets.ErrNotFound) {
		return http.StatusOK, nil
	}
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if kind, ok := engine.MatchKeyKind(desc.KeyKinds, key); ok && kind.OwnerOnly {
		return http.StatusForbidden, apperr.Forbidden("api.llm.errors.owner_only_profile", "kind", kind.Label, "env", OperatorWorkspaceEnv)
	}
	return http.StatusOK, nil
}
