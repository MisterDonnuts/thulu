package secretbox

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type sealedColumn struct {
	table, column string
	label         func(labelValue string) string
	selectSQL     string
	// Matching on the old value makes this a compare-and-swap: a row the running daemon changed in
	// between is left for the next start.
	updateSQL string
}

func fixedLabel(l string) func(string) string { return func(string) string { return l } }

// Labels copied from the owning packages; must change with them. A mismatch leaves the row
// undecryptable and counted as failed.
const (
	labelBYOK          = "octopus-byok-v1"    // internal/secrets llmKeyInfo
	labelGitHubApp     = "octopus-ghapp-v1"   // internal/githubapp ghAppInfo
	LabelWebhookSecret = "octopus-webhook-v1" // internal/webhook
	// internal/evidence.
	LabelEvidenceSecrets = "octopus-evidence-env-v1" // gitleaks:allow
	// internal/secrets, internal/workloadid.
	LabelWorkloadIdentityKey = "octopus-workload-identity-v1"
	labelMCPOAuthFlow        = "octopus-mcp-oauth-flow-v1" // internal/mcpoauth FlowLabel
)

// Mirrors internal/credentials secretLabel.
func credentialLabel(provider string) string { return "octopus-credential-" + provider + "-v1" }

// Every column holding secretbox values must be listed here, or its rows stay on whatever key
// sealed them.
var sealedColumns = []sealedColumn{
	{
		table: "llm_keys", column: "key_encrypted", label: fixedLabel(labelBYOK),
		selectSQL: `SELECT key_encrypted, '' FROM llm_keys WHERE key_encrypted <> ''`,
		updateSQL: `UPDATE llm_keys SET key_encrypted = $1 WHERE key_encrypted = $2`,
	},
	{
		table: "llm_profiles", column: "key_encrypted", label: fixedLabel(labelBYOK),
		selectSQL: `SELECT key_encrypted, '' FROM llm_profiles WHERE key_encrypted <> ''`,
		updateSQL: `UPDATE llm_profiles SET key_encrypted = $1 WHERE key_encrypted = $2`,
	},
	{
		table: "github_app_config", column: "private_key", label: fixedLabel(labelGitHubApp),
		selectSQL: `SELECT private_key, '' FROM github_app_config WHERE private_key <> ''`,
		updateSQL: `UPDATE github_app_config SET private_key = $1 WHERE private_key = $2`,
	},
	{
		table: "github_app_config", column: "client_secret", label: fixedLabel(labelGitHubApp),
		selectSQL: `SELECT client_secret, '' FROM github_app_config WHERE client_secret <> ''`,
		updateSQL: `UPDATE github_app_config SET client_secret = $1 WHERE client_secret = $2`,
	},
	{
		table: "github_app_config", column: "webhook_secret", label: fixedLabel(labelGitHubApp),
		selectSQL: `SELECT webhook_secret, '' FROM github_app_config WHERE webhook_secret <> ''`,
		updateSQL: `UPDATE github_app_config SET webhook_secret = $1 WHERE webhook_secret = $2`,
	},
	{
		table: "credentials", column: "payload_encrypted", label: credentialLabel,
		selectSQL: `SELECT payload_encrypted, provider FROM credentials WHERE payload_encrypted <> ''`,
		updateSQL: `UPDATE credentials SET payload_encrypted = $1 WHERE payload_encrypted = $2 AND provider = $3`,
	},
	{
		table: "webhook_endpoints", column: "secret_encrypted", label: fixedLabel(LabelWebhookSecret),
		selectSQL: `SELECT secret_encrypted, '' FROM webhook_endpoints WHERE secret_encrypted <> ''`,
		updateSQL: `UPDATE webhook_endpoints SET secret_encrypted = $1 WHERE secret_encrypted = $2`,
	},
	{
		table: "mcp_oauth_flows", column: "flow_sealed", label: fixedLabel(labelMCPOAuthFlow),
		selectSQL: `SELECT flow_sealed, '' FROM mcp_oauth_flows WHERE flow_sealed <> ''`,
		updateSQL: `UPDATE mcp_oauth_flows SET flow_sealed = $1 WHERE flow_sealed = $2`,
	},
	{
		table: "evidence_environments", column: "secrets_sealed", label: fixedLabel(LabelEvidenceSecrets),
		selectSQL: `SELECT secrets_sealed, '' FROM evidence_environments WHERE secrets_sealed <> ''`,
		updateSQL: `UPDATE evidence_environments SET secrets_sealed = $1 WHERE secrets_sealed = $2`,
	},
	{
		table: "workload_identity_keys", column: "private_key_encrypted", label: fixedLabel(LabelWorkloadIdentityKey),
		selectSQL: `SELECT private_key_encrypted, '' FROM workload_identity_keys WHERE private_key_encrypted <> ''`,
		updateSQL: `UPDATE workload_identity_keys SET private_key_encrypted = $1 WHERE private_key_encrypted = $2`,
	},
}

type RewrapReport struct {
	Rewrapped int
	Failed    int
}

// Run after every store has migrated its schema.
func MigrateAtStartup(ctx context.Context, db *sql.DB) (RewrapReport, error) {
	var rep RewrapReport
	// Validate even with the rewrite off, or a bad key config surfaces only when the first
	// credential fails to save.
	if err := ValidateConfig(); err != nil {
		return rep, err
	}
	if strings.TrimSpace(os.Getenv("OCTOPUS_SECRETBOX_REWRAP")) == "0" {
		return rep, nil
	}
	for _, c := range sealedColumns {
		ok, err := columnExists(ctx, db, c.table, c.column)
		if err != nil {
			return rep, err
		}
		if !ok {
			continue
		}
		if err := rewrapColumn(ctx, db, c, &rep); err != nil {
			return rep, apperr.Wrap(err, "secretbox.errors.rewrap_column", "table", c.table, "column", c.column)
		}
	}
	if rep.Rewrapped > 0 || rep.Failed > 0 {
		log.Printf("[secretbox] %s", i18n.Log("secretbox.log.rewrapped", "rewrapped", rep.Rewrapped, "failed", rep.Failed))
	}
	return rep, nil
}

func columnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
		table, column).Scan(&n)
	return n > 0, err
}

type sealedRow struct{ value, labelValue string }

func rewrapColumn(ctx context.Context, db *sql.DB, c sealedColumn, rep *RewrapReport) error {
	todo, err := staleRows(ctx, db, c.selectSQL)
	if err != nil {
		return err
	}
	for _, r := range todo {
		ok, err := rewrapRow(ctx, db, c, r)
		if err != nil {
			return err
		}
		if ok {
			rep.Rewrapped++
		} else {
			rep.Failed++
		}
	}
	return nil
}

// Read all rows before any UPDATE so no cursor stays open over the table being written.
func staleRows(ctx context.Context, db *sql.DB, selectSQL string) ([]sealedRow, error) {
	rows, err := db.QueryContext(ctx, selectSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todo []sealedRow
	for rows.Next() {
		var r sealedRow
		if scanErr := rows.Scan(&r.value, &r.labelValue); scanErr != nil {
			return nil, scanErr
		}
		if NeedsRewrap(r.value) {
			todo = append(todo, r)
		}
	}
	return todo, rows.Err()
}

func rewrapRow(ctx context.Context, db *sql.DB, c sealedColumn, r sealedRow) (bool, error) {
	label := c.label(r.labelValue)
	plain := r.value
	if strings.HasPrefix(r.value, v1Prefix) || strings.HasPrefix(r.value, v2Prefix) {
		var err error
		if plain, err = Decrypt(label, r.value); err != nil {
			log.Printf("[secretbox] %s", i18n.Log("secretbox.log.value_not_decryptable", "table", c.table, "column", c.column, "err", err))
			return false, nil
		}
	}
	sealed, err := Encrypt(label, plain)
	if err != nil {
		return false, err
	}
	args := []any{sealed, r.value}
	if strings.Contains(c.updateSQL, "$3") {
		args = append(args, r.labelValue)
	}
	if _, err := db.ExecContext(ctx, c.updateSQL, args...); err != nil {
		return false, err
	}
	return true, nil
}
