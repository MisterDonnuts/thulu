package api

import (
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

// The /.well-known documents are public: the provider fetches them with no Thulu credential. They
// hold public keys only.

// Must stay well under workloadid.RotationLead, how long a rotated-in key is published before it
// signs.
//
//i18n:ignore HTTP header value, not a message
const issuerCacheControl = "public, max-age=300"

func (s *Server) handleOIDCDiscovery(w http.ResponseWriter, r *http.Request) {
	d, err := s.workloadIdentity.Discovery()
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Cache-Control", issuerCacheControl)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	if err := s.workloadIdentity.Ready(); err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	set, err := s.workloadIdentity.JWKS(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", issuerCacheControl)
	writeJSON(w, http.StatusOK, set)
}

type workloadIdentityView struct {
	Configured    bool                 `json:"configured"`
	Error         string               `json:"error,omitempty"`
	Issuer        string               `json:"issuer,omitempty"`
	DiscoveryURL  string               `json:"discovery_url,omitempty"`
	JWKSURL       string               `json:"jwks_url,omitempty"`
	JWKS          *workloadid.JWKS     `json:"jwks,omitempty"`
	Keys          []workloadid.KeyInfo `json:"keys,omitempty"`
	SubjectPrefix string               `json:"subject_prefix,omitempty"`
	CanRotate     bool                 `json:"can_rotate"`
}

func (s *Server) handleGetWorkloadIdentity(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	view := workloadIdentityView{CanRotate: s.callerIsInstallationOwner(r)}
	if err := s.workloadIdentity.Ready(); err != nil {
		view.Error = apperr.LocalizeCtx(r.Context(), err)
		writeJSON(w, http.StatusOK, view)
		return
	}
	set, err := s.workloadIdentity.JWKS(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	keys, err := s.workloadIdentity.Keys(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	iss := s.workloadIdentity.URL()
	view.Configured = true
	view.Issuer = iss
	view.DiscoveryURL = iss + workloadid.DiscoveryPath
	view.JWKSURL = iss + workloadid.JWKSPath
	view.JWKS = &set
	view.Keys = keys
	view.SubjectPrefix = workloadid.SubjectPrefix(wsID)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRotateWorkloadIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.callerIsInstallationOwner(r) {
		writeErrKey(w, r, http.StatusForbidden, "api.llm.errors.owner_only_rotate", "env", OperatorWorkspaceEnv)
		return
	}
	if err := s.workloadIdentity.Ready(); err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	key, err := s.workloadIdentity.Rotate(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, key)
}
