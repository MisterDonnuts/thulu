package usage

import "github.com/MisterDonnuts/thulu/internal/migrate"

// Additive only: nothing an older binary reads may change shape, so a rollback keeps working on the
// new schema.
func attributionMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 34, SQL: `ALTER TABLE session_tool_events
			ADD COLUMN IF NOT EXISTS tool_use_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS parent_tool_use_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS mcp_server TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS result_bytes BIGINT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS result_tokens BIGINT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS args_tokens BIGINT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS carried_calls INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS weighted_tokens DOUBLE PRECISION NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS is_error BOOLEAN NOT NULL DEFAULT false,
			ADD COLUMN IF NOT EXISTS delegation_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS delegated_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS attributed BOOLEAN NOT NULL DEFAULT false,
			ADD COLUMN IF NOT EXISTS input_summary TEXT NOT NULL DEFAULT ''`},

		{ID: 35, SQL: `ALTER TABLE sessions
			ADD COLUMN IF NOT EXISTS token_optimizer TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS attribution_version INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS api_calls INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS base_tokens BIGINT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS compactions INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS unattributed_usd DOUBLE PRECISION NOT NULL DEFAULT 0`},

		{ID: 36, SQL: `CREATE TABLE IF NOT EXISTS session_calls (
			session_id         TEXT NOT NULL,
			idx                INTEGER NOT NULL,
			message_id         TEXT NOT NULL DEFAULT '',
			model              TEXT NOT NULL DEFAULT '',
			parent_tool_use_id TEXT NOT NULL DEFAULT '',
			input_tokens       BIGINT NOT NULL DEFAULT 0,
			cache_read         BIGINT NOT NULL DEFAULT 0,
			cache_creation     BIGINT NOT NULL DEFAULT 0,
			output_tokens      BIGINT NOT NULL DEFAULT 0,
			prompt_size        BIGINT NOT NULL DEFAULT 0,
			compaction         BOOLEAN NOT NULL DEFAULT false,
			ts                 TIMESTAMP,
			PRIMARY KEY (session_id, idx)
		)`},

		{ID: 37, SQL: `CREATE TABLE IF NOT EXISTS session_cost_breakdown (
			session_id      TEXT NOT NULL,
			category        TEXT NOT NULL,
			weighted_tokens DOUBLE PRECISION NOT NULL DEFAULT 0,
			cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, category)
		)`},

		{ID: 38, SQL: `CREATE TABLE IF NOT EXISTS delegations (
			id             TEXT PRIMARY KEY,
			workspace_id   TEXT NOT NULL DEFAULT '',
			task_id        TEXT NOT NULL DEFAULT '',
			chat_id        TEXT NOT NULL DEFAULT '',
			agent          TEXT NOT NULL DEFAULT '',
			stage          TEXT NOT NULL DEFAULT '',
			kind           TEXT NOT NULL DEFAULT '',
			profile_id     TEXT NOT NULL DEFAULT '',
			provider       TEXT NOT NULL DEFAULT '',
			model          TEXT NOT NULL DEFAULT '',
			files          INTEGER NOT NULL DEFAULT 0,
			request_bytes  BIGINT NOT NULL DEFAULT 0,
			response_bytes BIGINT NOT NULL DEFAULT 0,
			input_tokens   BIGINT NOT NULL DEFAULT 0,
			output_tokens  BIGINT NOT NULL DEFAULT 0,
			cost_usd       DOUBLE PRECISION NOT NULL DEFAULT 0,
			cost_estimated BOOLEAN NOT NULL DEFAULT false,
			duration_ms    BIGINT NOT NULL DEFAULT 0,
			status         TEXT NOT NULL DEFAULT '',
			error_code     TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL
		)`},
		{ID: 39, SQL: `CREATE INDEX IF NOT EXISTS delegations_workspace_created_idx ON delegations(workspace_id, created_at)`},
		{ID: 40, SQL: `CREATE INDEX IF NOT EXISTS delegations_task_idx ON delegations(task_id) WHERE task_id != ''`},
		{ID: 41, SQL: `CREATE INDEX IF NOT EXISTS tool_events_mcp_server_idx ON session_tool_events(mcp_server) WHERE mcp_server != ''`},
	}
}
