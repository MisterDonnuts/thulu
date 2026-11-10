package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

func CreateWorkspace(ctx context.Context, name, slug, ownerEmail string) error {
	dsn, err := requireDSN()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return apperr.Invalid("cli.errors.name_required")
	}
	if slug = strings.TrimSpace(slug); slug == "" {
		return apperr.Invalid("cli.errors.slug_required")
	}
	if ownerEmail = strings.TrimSpace(ownerEmail); ownerEmail == "" {
		return apperr.Invalid("cli.errors.owner_email_required")
	}

	boot, err := initwizard.OpenBootstrap(dsn)
	if err != nil {
		return err
	}
	defer boot.Close()

	id, created, err := boot.EnsureWorkspace(ctx, ownerEmail, i18n.Text("cli.labels.operator_name"), name, slug)
	if err != nil {
		return err
	}
	if !created {
		fmt.Println(i18n.Text("cli.cli.workspace_already_existed", "id", id))
		return nil
	}
	// scripts/compat.sh reads the id as the first field of this line; keep it first in every
	// locale.
	fmt.Println(i18n.Text("cli.cli.workspace_created", "id", id, "name", name, "slug", slug))
	return nil
}
