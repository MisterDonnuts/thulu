package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// The dispatcher mints the key and the pod only presents it, so a prompt-injected agent cannot
// claim another agent's scope.
type RunScope struct {
	TaskID string
	ChatID string
	Agent  string
	Stage  string
	// A key that carries it may write any task in its workspace. Mint it only for a delegated chat,
	// and never let it outlive the delegation.
	DelegatedBy string
	// An auditor's key names the auditor, which is not in the catalog. LLMAgent is the agent it audits:
	// the run bundle comes from it, and so does the model credential unless LLMEngine names the engine
	// the auditor was set to run on. Neither comes from the pod.
	LLMAgent  string
	LLMEngine string
}

func (s RunScope) Delegated() bool { return s.DelegatedBy != "" && s.ChatID != "" }

// Every run bound depends on this. It counts rows minted without Agent.
func (s RunScope) IsRun() bool {
	return s.Agent != "" || s.TaskID != "" || s.ChatID != ""
}

func runKeyName(s RunScope) string {
	var b strings.Builder
	b.WriteString("run:")
	if s.Agent != "" {
		b.WriteString(s.Agent)
	} else {
		b.WriteString("agent?")
	}
	if s.Stage != "" {
		b.WriteString("@" + s.Stage)
	}
	switch {
	case s.ChatID != "":
		b.WriteString(" chat:" + s.ChatID)
	case s.TaskID != "":
		b.WriteString(" task:" + s.TaskID)
	}
	if s.DelegatedBy != "" {
		b.WriteString(" for:" + s.DelegatedBy)
	}
	return b.String()
}

func runScopeMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 25, SQL: `
			ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_agent   TEXT NOT NULL DEFAULT '';
			ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_stage   TEXT NOT NULL DEFAULT '';
			ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_chat_id TEXT NOT NULL DEFAULT ''`},
		{ID: 26, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_delegated_by TEXT NOT NULL DEFAULT ''`},
	}
}

// A chat with no linked task gets no tasks:write unless it is delegated: a conversation must not
// move somebody's task.
func runKeyLLMMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 33, SQL: `
			ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_llm_agent  TEXT NOT NULL DEFAULT '';
			ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_llm_engine TEXT NOT NULL DEFAULT ''`},
	}
}

func runKeyPermissions(s RunScope) []Permission {
	perms := []Permission{
		PermTasksRead, PermFlowsRead, PermProjectsRead,
		PermRunsRead, PermRunsWrite, PermAgentsRead, PermInternalUse,
	}
	if s.TaskID != "" || s.Delegated() {
		perms = append(perms, PermTasksWrite)
	}
	return perms
}

func (s *UserStore) CreateRunScopedAPIKey(ctx context.Context,
	scope RunScope, workspaceID string, ttl time.Duration,
) (rawKey string, meta *APIKey, err error) {
	exp := time.Now().Add(ttl)
	grant := KeyGrant{
		WorkspaceID: workspaceID,
		Role:        RoleDeveloper,
		Permissions: runKeyPermissions(scope),
	}
	if scope.Delegated() {
		grant.Role, err = s.delegatedKeyRole(ctx, workspaceID, scope.DelegatedBy)
		if err != nil {
			return "", nil, err
		}
		grant.Permissions = permissionsWithin(grant.Permissions, grant.Role)
	}
	return s.createAPIKey(ctx, runKeyName(scope), "dispatcher", scope, grant, &exp)
}

func (s *UserStore) delegatedKeyRole(ctx context.Context, workspaceID, person string) (Role, error) {
	role, err := s.personRole(ctx, workspaceID, person)
	if err != nil {
		return "", err
	}
	switch {
	case role == "":
		return RoleViewer, nil
	case roleAtLeast(role, RoleDeveloper):
		return RoleDeveloper, nil
	}
	return role, nil
}

// person follows Author's naming: an email, "user:<id>", or DevModeAuthorName.
func (s *UserStore) personRole(ctx context.Context, workspaceID, person string) (Role, error) {
	if person == DevModeAuthorName {
		if DevMode() {
			return RoleOwner, nil
		}
		return "", nil
	}
	if id, ok := strings.CutPrefix(person, "user:"); ok {
		return s.GetMemberRole(ctx, workspaceID, id)
	}
	var role string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT m.role FROM workspace_members m JOIN users u ON u.id = m.user_id
		  WHERE m.workspace_id = ? AND LOWER(u.email) = LOWER(?)`), workspaceID, person,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return Role(role), err
}

func permissionsWithin(perms []Permission, role Role) []Permission {
	out := make([]Permission, 0, len(perms))
	for _, p := range perms {
		if roleAtLeast(role, MinRoleForPermission(p)) {
			out = append(out, p)
		}
	}
	return out
}

func (s *UserStore) DeleteTaskRunKeys(ctx context.Context, taskID string) (int64, error) {
	if taskID == "" {
		// '' would match every operator key.
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM api_keys WHERE scope_task_id = ?`), taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *UserStore) RevokeChatRunKeys(ctx context.Context, chatID string) error {
	if chatID == "" {
		// '' would match every key that is not a chat's.
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET revoked_at = ? WHERE scope_chat_id = ? AND revoked_at IS NULL`), time.Now(), chatID)
	return err
}

func (s *UserStore) MintRunKey(ctx context.Context, scope RunScope, workspaceID string, ttl time.Duration) (rawKey, keyID string, err error) {
	raw, meta, err := s.CreateRunScopedAPIKey(ctx, scope, workspaceID, ttl)
	if err != nil {
		return "", "", err
	}
	return raw, meta.ID, nil
}
