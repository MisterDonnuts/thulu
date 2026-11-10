package initwizard

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/db"
)

// Runs before the daemon starts: no HTTP route can create the first workspace or key without a
// workspace-scoped credential.
type Bootstrap struct {
	store  *auth.UserStore
	handle *sql.DB
}

func OpenBootstrap(dsn string) (*Bootstrap, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, apperr.Invalid("initwizard.errors.no_database_dsn")
	}
	handle, err := db.Open(dsn)
	if err != nil {
		return nil, apperr.Wrap(err, "initwizard.errors.open_database")
	}
	store, err := auth.NewUserStore(handle)
	if err != nil {
		handle.Close()
		return nil, apperr.Wrap(err, "initwizard.errors.prepare_auth_schema")
	}
	return &Bootstrap{store: store, handle: handle}, nil
}

func (b *Bootstrap) Close() {
	if b.handle != nil {
		b.handle.Close()
	}
}

func (b *Bootstrap) EnsureWorkspace(ctx context.Context, ownerEmail, ownerName, wsName, slug string) (id string, created bool, err error) {
	if existing, lookupErr := b.store.GetWorkspaceBySlug(ctx, slug); lookupErr == nil && existing != nil {
		return existing.ID, false, nil
	}
	owner, err := b.store.GetUserByEmail(ctx, ownerEmail)
	if err != nil || owner == nil {
		owner, err = b.store.CreateUser(ctx, ownerEmail, ownerName)
		if err != nil {
			return "", false, apperr.Wrap(err, "initwizard.errors.create_owner", "email", ownerEmail)
		}
	}
	ws, err := b.store.CreateWorkspace(ctx, owner.ID, wsName, slug)
	if err != nil {
		return "", false, apperr.Wrap(err, "initwizard.errors.create_workspace", "slug", slug)
	}
	return ws.ID, true, nil
}

func (b *Bootstrap) InviteOwner(ctx context.Context, workspaceID, email string) error {
	//i18n:ignore the inviter recorded on the invitation: the command that made it
	if _, err := b.store.UpsertInvitation(ctx, workspaceID, email, auth.RoleOwner, "thulu init"); err != nil {
		return apperr.Wrap(err, "initwizard.errors.invite_admin", "email", email)
	}
	return nil
}

func (b *Bootstrap) MintOperatorKey(ctx context.Context, label, workspaceID string, role auth.Role, preset string) (string, *time.Time, error) {
	perms, err := auth.ParsePermissions([]string{preset})
	if err != nil {
		return "", nil, apperr.Wrap(err, "initwizard.errors.permissions", "preset", preset)
	}
	//i18n:ignore the creator recorded on the key: the command that minted it
	raw, meta, err := b.store.CreateAPIKey(ctx, label, "thulu init", auth.KeyGrant{
		WorkspaceID: workspaceID,
		Role:        role,
		Permissions: perms,
	})
	if err != nil {
		return "", nil, apperr.Wrap(err, "initwizard.errors.mint_key")
	}
	return raw, meta.ExpiresAt, nil
}

// Full-permission owner key for this wizard run only; the caller must RevokeKey it before the
// wizard exits.
func (b *Bootstrap) MintSessionKey(ctx context.Context, label, workspaceID string) (raw, id string, err error) {
	perms, err := auth.ParsePermissions([]string{"full"})
	if err != nil {
		return "", "", apperr.Wrap(err, "initwizard.errors.permissions", "preset", "full")
	}
	//i18n:ignore the creator recorded on the key: the command that minted it
	raw, meta, err := b.store.CreateAPIKey(ctx, label, "thulu init", auth.KeyGrant{
		WorkspaceID: workspaceID,
		Role:        auth.RoleOwner,
		Permissions: perms,
	})
	if err != nil {
		return "", "", apperr.Wrap(err, "initwizard.errors.mint_key")
	}
	return raw, meta.ID, nil
}

func (b *Bootstrap) RevokeKey(ctx context.Context, id string) error {
	return b.store.RevokeAPIKey(ctx, id)
}

// Counts only live operator keys: run keys (scope_task_id/chat_id/agent set) die with their run,
// and an expired key must not stop a rerun from minting its replacement.
func (b *Bootstrap) HasAPIKey(ctx context.Context, workspaceID string) (bool, error) {
	var n int
	err := b.handle.QueryRowContext(ctx, db.Rebind(
		`SELECT COUNT(*) FROM api_keys
		  WHERE workspace_id = ? AND revoked_at IS NULL
		    AND (expires_at IS NULL OR expires_at > ?)
		    AND scope_task_id = '' AND scope_chat_id = '' AND scope_agent = ''`),
		workspaceID, time.Now().UTC()).Scan(&n)
	if err != nil {
		return false, apperr.Wrap(err, "initwizard.errors.count_api_keys")
	}
	return n > 0, nil
}
