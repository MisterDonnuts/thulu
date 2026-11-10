package auth

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const defaultSaaSMaxOwnedWorkspaces = 2

var ErrWorkspaceCapReached error = apperr.Forbidden("auth.errors.workspace_cap")

func MaxOwnedWorkspaces() int {
	if n, ok := maxOwnedWorkspacesEnv(); ok {
		return n
	}
	if SaaSMode() {
		return defaultSaaSMaxOwnedWorkspaces
	}
	return 0
}

func maxOwnedWorkspacesEnv() (int, bool) {
	raw := strings.TrimSpace(os.Getenv("OCTOPUS_MAX_OWNED_WORKSPACES"))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func validMaxOwnedWorkspacesEnv() bool {
	if strings.TrimSpace(os.Getenv("OCTOPUS_MAX_OWNED_WORKSPACES")) == "" {
		return true
	}
	_, ok := maxOwnedWorkspacesEnv()
	return ok
}

// The owner row lock stops concurrent requests both slipping under the cap.
// A taken slug gets a suffix instead of failing, which would reveal another tenant's name.
func (s *UserStore) CreateOwnedWorkspace(ctx context.Context, ownerID, name, slug string, maxOwned int) (*Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	owned, err := s.lockOwnerCountOwned(ctx, tx, ownerID)
	if err != nil {
		return nil, err
	}
	if maxOwned > 0 && owned >= maxOwned {
		return nil, apperr.Wrap(ErrWorkspaceCapReached, "auth.errors.workspace_cap_reached", "max", maxOwned)
	}

	slug = strings.ToLower(strings.TrimSpace(slug))
	free, err := s.freeSlug(ctx, tx, slug)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{ID: genID(), Name: strings.TrimSpace(name), Slug: free, OwnerID: ownerID, CreatedAt: time.Now()}
	if err := s.insertWorkspace(ctx, tx, ws); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *UserStore) lockOwnerCountOwned(ctx context.Context, tx *sql.Tx, ownerID string) (int, error) {
	var locked string
	if err := tx.QueryRowContext(ctx, s.rb(`SELECT id FROM users WHERE id = ? FOR UPDATE`), ownerID).Scan(&locked); err != nil {
		return 0, err
	}
	var owned int
	err := tx.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM workspaces WHERE owner_id = ?`), ownerID).Scan(&owned)
	return owned, err
}

func (s *UserStore) freeSlug(ctx context.Context, tx *sql.Tx, slug string) (string, error) {
	candidate := slug
	for range 5 {
		var one int
		err := tx.QueryRowContext(ctx, s.rb(`SELECT 1 FROM workspaces WHERE slug = ?`), candidate).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = slug + "-" + genID()[:6]
	}
	return candidate, nil
}

type createWorkspaceBlock struct {
	Reason string
	Max    int
}

const (
	createBlockReserved = "reserved"
	createBlockCap      = "cap"
)

func (h *Handler) createWorkspaceBlock(ctx context.Context, c *Claims) createWorkspaceBlock {
	if !h.mayCreateWorkspace(ctx, c) {
		return createWorkspaceBlock{Reason: createBlockReserved}
	}
	maxOwned := MaxOwnedWorkspaces()
	if maxOwned == 0 || h.isOperatorAdmin(ctx, c.UserID) {
		return createWorkspaceBlock{}
	}
	var owned int
	err := h.store.db.QueryRowContext(ctx, h.store.rb(
		`SELECT COUNT(*) FROM workspaces WHERE owner_id = ?`), c.UserID).Scan(&owned)
	if err != nil || owned >= maxOwned {
		return createWorkspaceBlock{Reason: createBlockCap, Max: maxOwned}
	}
	return createWorkspaceBlock{}
}
