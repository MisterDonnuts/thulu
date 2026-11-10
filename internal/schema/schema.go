package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

type Component struct {
	Name string
	List func(migrate.Inspector) ([]migrate.Migration, error)
}

func Components() []Component {
	out := []Component{
		{agentstore.MigrationComponent, agentstore.Migrations},
		{audit.MigrationComponent, audit.Migrations},
		{auth.MigrationComponent, auth.Migrations},
		{automation.MigrationComponent, automation.Migrations},
		{chat.MigrationComponent, chat.Migrations},
		{chatruntime.MigrationComponent, chatruntime.Migrations},
		{credentials.MigrationComponent, credentials.Migrations},
		{entitlements.MigrationComponent, entitlements.Migrations},
		{envmetrics.MigrationComponent, envmetrics.Migrations},
		{githubapp.MigrationComponent, githubapp.Migrations},
		{ingest.MigrationComponent, ingest.Migrations},
		{mcp.MigrationComponent, mcp.Migrations},
		{mcpoauth.MigrationComponent, mcpoauth.Migrations},
		{project.MigrationComponent, project.Migrations},
		{registry.MigrationComponent, registry.Migrations},
		{runner.MigrationComponent, runner.Migrations},
		{sandbox.MigrationComponent, sandbox.Migrations},
		{secrets.MigrationComponent, secrets.Migrations},
		{settings.MigrationComponent, settings.Migrations},
		{stage.MigrationComponent, stage.Migrations},
		{task.MigrationComponent, task.Migrations},
		{tokenopt.MigrationComponent, tokenopt.Migrations},
		{usage.MigrationComponent, usage.Migrations},
		{webhook.MigrationComponent, webhook.Migrations},
		{evidence.MigrationComponent, evidence.Migrations},
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type Entry struct {
	Component string
	ID        int
	SQL       string
	SHA256    string
	Contract  *migrate.Contract
}

func All() ([]Entry, error) {
	var out []Entry
	for _, c := range Components() {
		list, err := c.List(migrate.Everything)
		if err != nil {
			return nil, apperr.Wrap(err, "schema.errors.build_migrations", "component", c.Name)
		}
		for _, m := range list {
			sum := sha256.Sum256([]byte(m.SQL))
			out = append(out, Entry{
				Component: c.Name,
				ID:        m.ID,
				SQL:       m.SQL,
				SHA256:    hex.EncodeToString(sum[:]),
				Contract:  m.Contract,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
