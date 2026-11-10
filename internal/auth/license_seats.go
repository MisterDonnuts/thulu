package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// How many people the license covers is a term of the license, never a gate: this only lets the
// members page tell an admin. Nothing leaves the install.
type licenseSeats struct {
	Applies  bool `json:"applies"`
	People   int  `json:"people,omitempty"`
	Included int  `json:"included,omitempty"`
	Licensed bool `json:"licensed,omitempty"`
}

// People who signed in at least once and belong to a workspace. The placeholder owner that
// `thulu init` creates has no identity, and pending invitations are not members yet.
func (s *UserStore) countSignedInMembers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT m.user_id) FROM workspace_members m
		WHERE EXISTS (SELECT 1 FROM oidc_identities i WHERE i.user_id = m.user_id)`).Scan(&n)
	return n, err
}

func (h *Handler) licenseSeats(ctx context.Context) (licenseSeats, error) {
	// A hosted install bills by workspace plan; the license terms are for self-hosted installs.
	if SaaSMode() {
		return licenseSeats{}, nil
	}
	st := edition.Current()
	licensed := st.Edition == edition.Enterprise
	included := edition.CommunitySeats
	if licensed {
		included = st.Seats
	}
	if included <= 0 {
		return licenseSeats{}, nil
	}
	n, err := h.store.countSignedInMembers(ctx)
	if err != nil {
		return licenseSeats{}, err
	}
	return licenseSeats{Applies: true, People: n, Included: included, Licensed: licensed}, nil
}

func (h *Handler) handleLicenseSeats(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersRead) {
		return
	}
	out, err := h.licenseSeats(r.Context())
	if err != nil {
		logx.Warn(err, "auth.log.seats_failed", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
