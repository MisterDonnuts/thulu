package agentstore

import "github.com/MisterDonnuts/thulu/internal/migrate"

// Data migration, 1.0.0: stored references to the names used before 1.0 become the current ones.
// The MCP servers argus-<x> are thulu-<x> (also inside "argus-github@write+tool" entries and
// mcp__argus-github__<tool> tool names), label keys argus.* are thulu.*, and the project sidecar
// directory .argus is .thulu. Only the five servers Thulu ships are renamed: an operator's own
// server keeps its name.
//
// It runs in agentstore but reaches tables of other stores, so every statement checks the table
// exists: on a fresh install those stores may migrate later, and have nothing to rewrite. Every
// statement only replaces old spellings, so a second run changes nothing. Agent image names are
// left alone: an image tag on disk does not change with this release.
//
// Rows hold the new names after this; 1.0.0 reads only those. A 0.x binary never runs on the result
// because rollback does not cross a major version (cmd/release-manifest rollbackTo).
const renameMigrationSQL = `
SET LOCAL statement_timeout = '15min';

CREATE FUNCTION pg_temp.thulu_names(t text) RETURNS text LANGUAGE sql IMMUTABLE AS $f$
	SELECT regexp_replace(regexp_replace(regexp_replace(regexp_replace(
		replace(replace(t, 'OCTOPUS_MCP_ARGUS_', 'OCTOPUS_MCP_'), 'octopus-mcp-argus-', 'octopus-mcp-'),
		'MCP_ARGUS_(GITHUB|BROWSER|CLOUD|FIGMA|SANDBOX)_', 'MCP_THULU_\1_', 'g'),
		'argus-(github|browser|cloud|figma|sandbox)', 'thulu-\1', 'g'),
		'\margus\.([a-z_])', 'thulu.\1', 'g'),
		'\.argus\M', '.thulu', 'g')
$f$;

DO $m$
DECLARE
	r      record;
	txt    text;
	newb   bytea;
	newsha text;
BEGIN
	-- Prompts, skill and tool manuals, compose files: blobs are content-addressed, so a rewritten
	-- body is a new blob and every reference moves to it.
	FOR r IN SELECT sha256, content_blobs.body AS raw FROM content_blobs
		WHERE position(convert_to('argus', 'UTF8') IN content_blobs.body) > 0
	LOOP
		BEGIN
			txt := convert_from(r.raw, 'UTF8');
		EXCEPTION WHEN OTHERS THEN
			CONTINUE; -- not text
		END;
		newb := convert_to(pg_temp.thulu_names(txt), 'UTF8');
		CONTINUE WHEN newb = r.raw;
		newsha := encode(sha256(newb), 'hex');
		INSERT INTO content_blobs (sha256, body, size, created_at)
			VALUES (newsha, newb, length(newb), now()) ON CONFLICT (sha256) DO NOTHING;
		UPDATE content_files SET blob_sha = newsha WHERE blob_sha = r.sha256;
		UPDATE content_revisions SET blob_sha = newsha WHERE blob_sha = r.sha256;
		UPDATE content_revisions SET prev_blob_sha = newsha WHERE prev_blob_sha = r.sha256;
	END LOOP;

	-- Tool manuals an operator imported are stored under the server's name.
	UPDATE content_files f SET owner = pg_temp.thulu_names(f.owner)
		WHERE f.kind = 'tool'
		  AND f.owner IN ('argus-github', 'argus-browser', 'argus-cloud', 'argus-figma', 'argus-sandbox')
		  AND NOT EXISTS (SELECT 1 FROM content_files g WHERE g.tenant_id = f.tenant_id AND g.kind = f.kind
		                  AND g.owner = pg_temp.thulu_names(f.owner) AND g.path = f.path);
	UPDATE content_revisions SET owner = pg_temp.thulu_names(owner)
		WHERE kind = 'tool'
		  AND owner IN ('argus-github', 'argus-browser', 'argus-cloud', 'argus-figma', 'argus-sandbox');
	UPDATE content_origins o SET owner = pg_temp.thulu_names(o.owner)
		WHERE o.kind = 'tool'
		  AND o.owner IN ('argus-github', 'argus-browser', 'argus-cloud', 'argus-figma', 'argus-sandbox')
		  AND NOT EXISTS (SELECT 1 FROM content_origins p WHERE p.tenant_id = o.tenant_id AND p.kind = o.kind
		                  AND p.owner = pg_temp.thulu_names(o.owner));

	UPDATE agents SET
		mcp_by_stage    = pg_temp.thulu_names(mcp_by_stage::text)::jsonb,
		skills_by_stage = pg_temp.thulu_names(skills_by_stage::text)::jsonb,
		auditor         = pg_temp.thulu_names(auditor::text)::jsonb,
		params          = pg_temp.thulu_names(params::text)::jsonb,
		token_optimizer = pg_temp.thulu_names(token_optimizer::text)::jsonb
		WHERE mcp_by_stage::text <> pg_temp.thulu_names(mcp_by_stage::text)
		   OR skills_by_stage::text <> pg_temp.thulu_names(skills_by_stage::text)
		   OR coalesce(auditor::text, '') <> coalesce(pg_temp.thulu_names(auditor::text), '')
		   OR params::text <> pg_temp.thulu_names(params::text)
		   OR token_optimizer::text <> pg_temp.thulu_names(token_optimizer::text);
	UPDATE agent_def_revisions SET definition = pg_temp.thulu_names(definition::text)::jsonb
		WHERE definition::text <> pg_temp.thulu_names(definition::text);
	UPDATE system_skills SET mcp = pg_temp.thulu_names(mcp) WHERE mcp <> pg_temp.thulu_names(mcp);

	IF to_regclass('settings') IS NOT NULL THEN
		UPDATE settings SET value = pg_temp.thulu_names(value) WHERE value <> pg_temp.thulu_names(value);
	END IF;
	IF to_regclass('workspace_settings') IS NOT NULL THEN
		UPDATE workspace_settings SET value = pg_temp.thulu_names(value)
			WHERE value <> pg_temp.thulu_names(value);
	END IF;
	IF to_regclass('automations') IS NOT NULL THEN
		UPDATE automations SET spec = pg_temp.thulu_names(spec) WHERE spec <> pg_temp.thulu_names(spec);
	END IF;
	IF to_regclass('mcp_registrations') IS NOT NULL THEN
		UPDATE mcp_registrations m SET name = pg_temp.thulu_names(m.name)
			WHERE m.name IN ('argus-github', 'argus-browser', 'argus-cloud', 'argus-figma', 'argus-sandbox')
			  AND NOT EXISTS (SELECT 1 FROM mcp_registrations n WHERE n.name = pg_temp.thulu_names(m.name));
		UPDATE mcp_registrations SET url = pg_temp.thulu_names(url), description = pg_temp.thulu_names(description)
			WHERE url <> pg_temp.thulu_names(url) OR description <> pg_temp.thulu_names(description);
	END IF;
	IF to_regclass('mcp_run_grants') IS NOT NULL THEN
		UPDATE mcp_run_grants SET tools = pg_temp.thulu_names(tools) WHERE tools <> pg_temp.thulu_names(tools);
	END IF;
	IF to_regclass('credential_tool_policy') IS NOT NULL THEN
		UPDATE credential_tool_policy c SET server = pg_temp.thulu_names(c.server)
			WHERE c.server <> pg_temp.thulu_names(c.server)
			  AND NOT EXISTS (SELECT 1 FROM credential_tool_policy d WHERE d.credential_id = c.credential_id
			                  AND d.server = pg_temp.thulu_names(c.server) AND d.tool = c.tool);
	END IF;
	IF to_regclass('credential_decisions') IS NOT NULL THEN
		UPDATE credential_decisions SET server = pg_temp.thulu_names(server), tool = pg_temp.thulu_names(tool)
			WHERE server <> pg_temp.thulu_names(server) OR tool <> pg_temp.thulu_names(tool);
	END IF;
	IF to_regclass('session_tool_events') IS NOT NULL THEN
		UPDATE session_tool_events SET tool_name = pg_temp.thulu_names(tool_name),
		                               mcp_server = pg_temp.thulu_names(mcp_server)
			WHERE tool_name ~ '^mcp__argus-(github|browser|cloud|figma|sandbox)__'
			   OR mcp_server IN ('argus-github', 'argus-browser', 'argus-cloud', 'argus-figma', 'argus-sandbox');
	END IF;
END
$m$;

DROP FUNCTION pg_temp.thulu_names(text);
`

func renameMigration(id int) migrate.Migration {
	return migrate.Migration{ID: id, SQL: renameMigrationSQL, Contract: &migrate.Contract{
		StoppedUsingIn: "1.0.0",
		Reason:         "1.0.0 reads only the thulu names; no 0.x binary runs on 1.0 rows", //i18n:ignore migration contract note
	}}
}
