package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

func MintKey(ctx context.Context, name, workspace, role string, permissions []string, expiresInDays int) error {
	store, closeStore, err := openStore(auth.NewUserStore)
	if err != nil {
		return err
	}
	defer closeStore()

	workspaceID, err := resolveWorkspace(ctx, store.DB(), workspace, false)
	if err != nil {
		return err
	}
	if capErr := adoptKeyCap(); capErr != nil {
		return capErr
	}

	grant := auth.KeyGrant{WorkspaceID: workspaceID, Role: auth.Role(strings.TrimSpace(role))}
	if grant.Role == "" {
		grant.Role = auth.RoleDeveloper
	}
	perms, err := auth.ParsePermissions(permissions)
	if err != nil {
		return err
	}
	if len(perms) == 0 {
		return apperr.Invalid("cli.errors.permissions_required", "presets", strings.Join(auth.PresetNames(), ", "))
	}
	grant.Permissions = perms
	if expiresInDays > 0 {
		grant.ExpiresIn = time.Duration(expiresInDays) * 24 * time.Hour
	}

	raw, meta, err := store.CreateAPIKey(ctx, name, "cli", grant)
	if err != nil {
		return apperr.Wrap(err, "cli.errors.mint_key")
	}

	out, err := json.MarshalIndent(map[string]any{
		"id":           meta.ID,
		"name":         meta.Name,
		"prefix":       meta.Prefix,
		"key":          raw,
		"workspace_id": meta.WorkspaceID,
		"role":         meta.Role,
		"permissions":  meta.Permissions,
		"expires_at":   meta.ExpiresAt,
	}, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "cli.errors.encode_result")
	}
	fmt.Println(string(out))
	return nil
}

func adoptKeyCap() error {
	return initwizard.AdoptEnv(installEnvFile(), auth.MaxKeyTTLEnv)
}

func ListWorkspaces() error {
	store, closeStore, err := openStore(auth.NewUserStore)
	if err != nil {
		return err
	}
	defer closeStore()

	rows, err := store.DB().QueryContext(context.Background(),
		`SELECT id, name, slug FROM workspaces ORDER BY name`)
	if err != nil {
		return apperr.Wrap(err, "cli.errors.list_workspaces")
	}
	defer rows.Close()

	var list []namedWorkspace
	for rows.Next() {
		var w namedWorkspace
		if err := rows.Scan(&w.id, &w.name, &w.slug); err != nil {
			return err
		}
		list = append(list, w)
	}
	if err := rows.Err(); err != nil {
		return apperr.Wrap(err, "cli.errors.list_workspaces")
	}
	printWorkspaces(os.Stdout, os.Stderr, list)
	return nil
}

type namedWorkspace struct{ id, name, slug string }

// The empty-install message goes to errOut so scripts reading rows from stdout get none.
func printWorkspaces(out, errOut io.Writer, list []namedWorkspace) {
	if len(list) == 0 {
		fmt.Fprintln(errOut, i18n.Text("cli.cli.no_workspaces"))
		return
	}
	for _, w := range list {
		fmt.Fprintf(out, "%s  %-30s %s\n", w.id, w.name, w.slug)
	}
}
