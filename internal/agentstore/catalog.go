package agentstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Workspace ids are 16 hex characters, so these can never collide with a workspace.
const (
	SystemTenant  = "_system"
	StarterTenant = "_starter"
)

const (
	OriginStarter = "starter"
	OriginLegacy  = "legacy"
)

const ActionSeeded = "seeded"

const (
	packStarter        = "starter"
	packLegacyAdoption = "legacy-adoption"
)

const (
	SkillMetaFile   = "skill.yaml"
	SkillPromptFile = "prompt.md"
)

var (
	ErrSystemSkillName error = apperr.Conflict("agentstore.errors.system_skill_name")
	ErrSkillIncomplete error = apperr.Invalid("agentstore.errors.skill_incomplete")
)

type SystemSkill struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	MCP       string    `json:"mcp,omitempty"`
	When      string    `json:"when,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

type Origin struct {
	Origin   string    `json:"origin"`
	Revision int       `json:"origin_revision"`
	CopiedAt time.Time `json:"copied_at"`
	// Empty for origins recorded before the column existed; those fall back to timestamps.
	Hash         string            `json:"content_hash,omitempty"`
	SkillRenames map[string]string `json:"skill_renames,omitempty"`
}

type skillMeta struct {
	Name    string `yaml:"name"`
	Version int    `yaml:"version"`
	MCP     string `yaml:"mcp"`
	When    string `yaml:"when"`
}

// Appended to Migrations as IDs 8+index: only append; never reorder or edit a published one.
func catalogMigrations() []string {
	return []string{
		// The row is the lock: inserting it is what makes concurrent first requests seed once.
		`CREATE TABLE IF NOT EXISTS catalog_seeds (
			workspace_id  TEXT PRIMARY KEY,
			pack_revision INTEGER NOT NULL DEFAULT 0,
			seeded_at     TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS system_skills (
			name       TEXT PRIMARY KEY,
			version    INTEGER NOT NULL DEFAULT 0,
			mcp        TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL,
			updated_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS catalog_packs (
			name       TEXT PRIMARY KEY,
			revision   INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS content_origins (
			tenant_id       TEXT NOT NULL,
			kind            TEXT NOT NULL,
			owner           TEXT NOT NULL,
			origin          TEXT NOT NULL,
			origin_revision INTEGER NOT NULL DEFAULT 0,
			copied_at       TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (tenant_id, kind, owner)
		)`,
		// New columns with defaults so a release that does not know them keeps inserting without
		// them.
		`ALTER TABLE content_origins ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE content_origins ADD COLUMN IF NOT EXISTS skill_renames JSONB NOT NULL DEFAULT '{}'`,
		`CREATE TABLE IF NOT EXISTS agent_def_revisions (
			id         BIGSERIAL PRIMARY KEY,
			tenant_id  TEXT NOT NULL,
			name       TEXT NOT NULL,
			definition JSONB NOT NULL,
			reason     TEXT NOT NULL DEFAULT '',
			actor      TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS agent_def_revisions_target
			ON agent_def_revisions (tenant_id, name, id DESC)`,
		// New columns with defaults so a release that does not know them keeps inserting without
		// them.
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS token_optimizer JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE system_skills ADD COLUMN IF NOT EXISTS applies_when TEXT NOT NULL DEFAULT ''`,
	}
}

func IsReservedTenant(tenant string) bool {
	return tenant == SystemTenant || tenant == StarterTenant || tenant == GlobalTenant
}

// The seed is recorded rather than inferred from content, so a workspace that deletes what the
// pack gave it does not get it back.
func (s *Store) EnsureSeeded(ctx context.Context, ws string) error {
	if IsReservedTenant(ws) {
		return nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT 1 FROM catalog_seeds WHERE workspace_id = ?`), ws).Scan(&one)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap(err, "agentstore.errors.read_seed", "workspace", ws)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.begin_tx")
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.seedTx(ctx, tx, ws); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "agentstore.errors.commit_tx")
	}
	return nil
}

func (s *Store) seedTx(ctx context.Context, tx *sql.Tx, ws string) error {
	rev, err := s.packRevision(ctx, tx, packStarter)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO catalog_seeds (workspace_id, pack_revision, seeded_at)
		 VALUES (?, ?, ?) ON CONFLICT (workspace_id) DO NOTHING`), ws, rev, time.Now().UTC())
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.write_seed", "workspace", ws)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return s.copyTenant(ctx, tx, StarterTenant, ws, OriginStarter, rev)
}

func (s *Store) copyTenant(ctx context.Context, tx *sql.Tx, from, to, origin string, rev int) error {
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO agents (tenant_id, name, description, type, engine, supported_engines,
		                     model, effort, skills_mode, works_without_repo, valid_stages,
		                     turns_by_stage, skills_by_stage, mcp_by_stage, auditor, params,
		                     token_optimizer, image, is_template, created_by, created_at, updated_at, disabled_at)
		 SELECT ?::text, name, description, type, engine, supported_engines,
		        model, effort, skills_mode, works_without_repo, valid_stages,
		        turns_by_stage, skills_by_stage, mcp_by_stage, auditor, params,
		        token_optimizer, image, is_template, ?::text, ?::timestamptz, ?::timestamptz, NULL
		   FROM agents WHERE tenant_id = ? AND disabled_at IS NULL
		 ON CONFLICT (tenant_id, name) DO NOTHING`),
		to, origin, now, now, from); err != nil {
		return apperr.Wrap(err, "agentstore.errors.copy_catalog", "from", from, "to", to)
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`WITH ins AS (
		   INSERT INTO content_files (tenant_id, kind, owner, path, blob_sha, updated_at, updated_by)
		   SELECT ?::text, kind, owner, path, blob_sha, ?::timestamptz, ?::text
		     FROM content_files
		    WHERE tenant_id = ? AND kind IN ('agent', 'skill')
		   ON CONFLICT (tenant_id, kind, owner, path) DO NOTHING
		   RETURNING tenant_id, kind, owner, path, blob_sha)
		 INSERT INTO content_revisions
		   (tenant_id, kind, owner, path, blob_sha, prev_blob_sha, action, actor, created_at)
		 SELECT tenant_id, kind, owner, path, blob_sha, NULL, ?::text, ?::text, ?::timestamptz FROM ins`),
		to, now, origin, from, ActionSeeded, origin, now); err != nil {
		return apperr.Wrap(err, "agentstore.errors.copy_catalog", "from", from, "to", to)
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO content_origins (tenant_id, kind, owner, origin, origin_revision, copied_at)
		 SELECT ?::text, 'agent'::text, name, ?::text, ?::integer, ?::timestamptz
		   FROM agents WHERE tenant_id = ? AND disabled_at IS NULL
		 UNION
		 SELECT DISTINCT ?::text, 'skill'::text, owner, ?::text, ?::integer, ?::timestamptz
		   FROM content_files WHERE tenant_id = ? AND kind = 'skill'
		 ON CONFLICT (tenant_id, kind, owner) DO NOTHING`),
		to, origin, rev, now, from, to, origin, rev, now, from); err != nil {
		return apperr.Wrap(err, "agentstore.errors.copy_catalog", "from", from, "to", to)
	}
	return nil
}

// The originals stay under GlobalTenant: a rollback to a release that reads GlobalTenant needs
// them.
func (s *Store) AdoptLegacy(ctx context.Context, workspaces []string) (int, error) {
	done, err := s.markerSet(ctx, packLegacyAdoption)
	if err != nil || done {
		return 0, err
	}
	adopted := 0
	for _, ws := range workspaces {
		if IsReservedTenant(ws) {
			continue
		}
		if err := s.adoptOne(ctx, ws); err != nil {
			return adopted, err
		}
		adopted++
	}
	// Written last: a crash halfway leaves the marker unset, and a rerun copies without replacing.
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO catalog_packs (name, revision, updated_at) VALUES (?, 1, ?)
		 ON CONFLICT (name) DO NOTHING`), packLegacyAdoption, time.Now().UTC()); err != nil {
		return adopted, apperr.Wrap(err, "agentstore.errors.write_pack", "pack", packLegacyAdoption)
	}
	return adopted, nil
}

func (s *Store) adoptOne(ctx context.Context, ws string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.begin_tx")
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.copyTenant(ctx, tx, GlobalTenant, ws, OriginLegacy, 0); err != nil {
		return err
	}
	if err := s.seedTx(ctx, tx, ws); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "agentstore.errors.commit_tx")
	}
	return nil
}

func (s *Store) markerSet(ctx context.Context, name string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT 1 FROM catalog_packs WHERE name = ?`), name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "agentstore.errors.read_pack", "pack", name)
	}
	return true, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) packRevision(ctx context.Context, q queryRower, name string) (int, error) {
	var rev int
	err := q.QueryRowContext(ctx, s.rb(
		`SELECT revision FROM catalog_packs WHERE name = ?`), name).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, apperr.Wrap(err, "agentstore.errors.read_pack", "pack", name)
	}
	return rev, nil
}

func (s *Store) GetOrigin(ctx context.Context, tenant, kind, owner string) (Origin, error) {
	var (
		o       Origin
		renames []byte
	)
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT origin, origin_revision, copied_at, content_hash, skill_renames FROM content_origins
		  WHERE tenant_id = ? AND kind = ? AND owner = ?`), tenant, kind, owner).
		Scan(&o.Origin, &o.Revision, &o.CopiedAt, &o.Hash, &renames)
	if errors.Is(err, sql.ErrNoRows) {
		return Origin{}, ErrNotFound
	}
	if err != nil {
		return Origin{}, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", owner)
	}
	if err := decodeRenames(renames, &o); err != nil {
		return Origin{}, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", owner)
	}
	return o, nil
}

func decodeRenames(raw []byte, o *Origin) error {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if len(m) > 0 {
		o.SkillRenames = m
	}
	return nil
}

func (s *Store) Origins(ctx context.Context, tenant, kind string) (map[string]Origin, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT owner, origin, origin_revision, copied_at FROM content_origins
		  WHERE tenant_id = ? AND kind = ?`), tenant, kind)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", "*")
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Origin{}
	for rows.Next() {
		var (
			owner string
			o     Origin
		)
		if err := rows.Scan(&owner, &o.Origin, &o.Revision, &o.CopiedAt); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.read_origin", "kind", kind, "owner", "*")
		}
		out[owner] = o
	}
	return out, rows.Err()
}

func (s *Store) ForgetOrigin(ctx context.Context, tenant, kind, owner string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM content_origins WHERE tenant_id = ? AND kind = ? AND owner = ?`),
		tenant, kind, owner)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.forget_origin", "kind", kind, "owner", owner)
	}
	return nil
}

func (s *Store) SyncStarterPack(ctx context.Context, p Pack, actor string) (bool, error) {
	stored, err := s.packRevision(ctx, s.db, packStarter)
	if err != nil {
		return false, err
	}
	if p.Revision <= stored {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM agents WHERE tenant_id = ?`), StarterTenant); err != nil {
		return false, apperr.Wrap(err, "agentstore.errors.write_pack", "pack", packStarter)
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM content_files WHERE tenant_id = ?`), StarterTenant); err != nil {
		return false, apperr.Wrap(err, "agentstore.errors.write_pack", "pack", packStarter)
	}
	if _, err := s.ImportPack(ctx, StarterTenant, p, actor); err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO catalog_packs (name, revision, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET revision = EXCLUDED.revision, updated_at = EXCLUDED.updated_at`),
		packStarter, p.Revision, time.Now().UTC()); err != nil {
		return false, apperr.Wrap(err, "agentstore.errors.write_pack", "pack", packStarter)
	}
	return true, nil
}

func (s *Store) ListSystemSkills(ctx context.Context) ([]SystemSkill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, version, mcp, applies_when, updated_at, updated_by FROM system_skills ORDER BY name`)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_system_skills")
	}
	defer func() { _ = rows.Close() }()
	var out []SystemSkill
	for rows.Next() {
		var sk SystemSkill
		if err := rows.Scan(&sk.Name, &sk.Version, &sk.MCP, &sk.When, &sk.UpdatedAt, &sk.UpdatedBy); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.list_system_skills")
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func (s *Store) GetSystemSkill(ctx context.Context, name string) (SystemSkill, error) {
	var sk SystemSkill
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT name, version, mcp, applies_when, updated_at, updated_by FROM system_skills WHERE name = ?`), name).
		Scan(&sk.Name, &sk.Version, &sk.MCP, &sk.When, &sk.UpdatedAt, &sk.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemSkill{}, ErrNotFound
	}
	if err != nil {
		return SystemSkill{}, apperr.Wrap(err, "agentstore.errors.get_system_skill", "name", name)
	}
	return sk, nil
}

// Compares versions, not bytes, so an operator's hotfix (which keeps the version) survives restarts
// of the same release and is replaced only by a release that bumps it.
func (s *Store) SyncSystemSkills(ctx context.Context, fsys fs.FS, actor string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_system_skills")
	}
	var written []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		files, err := readSkillDir(fsys, name)
		if err != nil {
			return written, err
		}
		meta, err := parseSkillMeta(files)
		if err != nil {
			return written, apperr.Wrap(err, "agentstore.errors.read_system_skill", "name", name)
		}
		stored, err := s.GetSystemSkill(ctx, name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return written, err
		}
		if err == nil && stored.Version >= meta.Version {
			continue
		}
		if _, err := s.replaceSystemSkill(ctx, name, files, meta.Version, actor); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	return written, nil
}

func (s *Store) PutSystemSkill(ctx context.Context, name string, files map[string][]byte, actor string) (SystemSkill, error) {
	if err := ValidateName(name); err != nil {
		return SystemSkill{}, err
	}
	if _, err := parseSkillMeta(files); err != nil {
		return SystemSkill{}, err
	}
	version := 0
	if stored, err := s.GetSystemSkill(ctx, name); err == nil {
		version = stored.Version
	} else if !errors.Is(err, ErrNotFound) {
		return SystemSkill{}, err
	}
	return s.replaceSystemSkill(ctx, name, files, version, actor)
}

func (s *Store) PutSystemSkillFile(ctx context.Context, name, rel string, body []byte, actor string) (File, error) {
	stored, err := s.GetSystemSkill(ctx, name)
	if err != nil {
		return File{}, err
	}
	mcp, when := stored.MCP, stored.When
	if rel == SkillMetaFile {
		var m skillMeta
		if yerr := yaml.Unmarshal(body, &m); yerr != nil {
			return File{}, apperr.WrapKind(apperr.KindInvalid, yerr, "agentstore.errors.skill_yaml")
		}
		mcp, when = strings.TrimSpace(m.MCP), strings.TrimSpace(m.When)
	}
	f, err := s.PutFile(ctx, SystemTenant, KindSkill, name, rel, body, actor)
	if err != nil {
		return File{}, err
	}
	if err := s.touchSystemSkill(ctx, name, stored.Version, mcp, when, actor); err != nil {
		return File{}, err
	}
	return f, nil
}

func (s *Store) DeleteSystemSkill(ctx context.Context, name, actor string) error {
	if _, err := s.GetSystemSkill(ctx, name); err != nil {
		return err
	}
	files, err := s.ListOwner(ctx, SystemTenant, KindSkill, name)
	if err != nil {
		return err
	}
	for i := range files {
		if err := s.DeleteFile(ctx, SystemTenant, KindSkill, name, files[i].Path, actor); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM system_skills WHERE name = ?`), name); err != nil {
		return apperr.Wrap(err, "agentstore.errors.delete_system_skill", "name", name)
	}
	return nil
}

func (s *Store) replaceSystemSkill(ctx context.Context, name string, files map[string][]byte,
	version int, actor string,
) (SystemSkill, error) {
	meta, err := parseSkillMeta(files)
	if err != nil {
		return SystemSkill{}, err
	}
	current, err := s.ListOwner(ctx, SystemTenant, KindSkill, name)
	if err != nil {
		return SystemSkill{}, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if _, err := s.PutFile(ctx, SystemTenant, KindSkill, name, p, files[p], actor); err != nil {
			return SystemSkill{}, apperr.Wrap(err, "agentstore.errors.import_file", "kind", KindSkill, "owner", name, "path", p)
		}
	}
	for i := range current {
		if _, keep := files[current[i].Path]; keep {
			continue
		}
		if err := s.DeleteFile(ctx, SystemTenant, KindSkill, name, current[i].Path, actor); err != nil {
			return SystemSkill{}, err
		}
	}
	if err := s.touchSystemSkill(ctx, name, version, strings.TrimSpace(meta.MCP), strings.TrimSpace(meta.When), actor); err != nil {
		return SystemSkill{}, err
	}
	return s.GetSystemSkill(ctx, name)
}

func (s *Store) touchSystemSkill(ctx context.Context, name string, version int, mcp, when, actor string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO system_skills (name, version, mcp, applies_when, updated_at, updated_by)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, mcp = EXCLUDED.mcp,
		     applies_when = EXCLUDED.applies_when,
		     updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`),
		name, version, mcp, when, time.Now().UTC(), actor)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.write_system_skill", "name", name)
	}
	return nil
}

func readSkillDir(fsys fs.FS, name string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return apperr.Wrap(err, "agentstore.errors.read_system_skill", "name", name)
		}
		files[strings.TrimPrefix(p, name+"/")] = body
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_system_skill", "name", name)
	}
	return files, nil
}

func parseSkillMeta(files map[string][]byte) (skillMeta, error) {
	if _, ok := files[SkillPromptFile]; !ok {
		return skillMeta{}, ErrSkillIncomplete
	}
	raw, ok := files[SkillMetaFile]
	if !ok {
		return skillMeta{}, ErrSkillIncomplete
	}
	var m skillMeta
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&m); err != nil {
		return skillMeta{}, apperr.WrapKind(apperr.KindInvalid, err, "agentstore.errors.skill_yaml")
	}
	for p := range files {
		if err := ValidatePath(p); err != nil {
			return skillMeta{}, apperr.Wrap(err, "agentstore.errors.invalid_skill_path", "path", path.Clean(p))
		}
	}
	return m, nil
}

func (s *Store) ExportCatalog(ctx context.Context, ws string) (Pack, error) {
	p, err := s.ExportPack(ctx, ws)
	if err != nil {
		return Pack{}, err
	}
	global, err := s.ExportPack(ctx, GlobalTenant)
	if err != nil {
		return Pack{}, err
	}
	content := make([]PackedFile, 0, len(p.Content))
	for _, f := range p.Content {
		if f.Kind != KindTool {
			content = append(content, f)
		}
	}
	for _, f := range global.Content {
		if f.Kind == KindTool {
			content = append(content, f)
		}
	}
	sort.Slice(content, func(i, j int) bool {
		a, b := content[i], content[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Path < b.Path
	})
	p.Content = content
	return p, nil
}

func (s *Store) ImportCatalog(ctx context.Context, ws string, p Pack, actor string) (ImportStats, error) {
	if ws == "" || IsReservedTenant(ws) {
		return ImportStats{}, apperr.Invalid("agentstore.errors.import_needs_workspace")
	}
	if err := s.EnsureSeeded(ctx, ws); err != nil {
		return ImportStats{}, err
	}
	if err := renameLegacyPack(&p); err != nil {
		return ImportStats{}, apperr.Wrap(err, "agentstore.errors.import_pack_names")
	}
	own, tools := p, Pack{Version: p.Version, CreatedAt: p.CreatedAt}
	own.Content = nil
	for _, f := range p.Content {
		if f.Kind == KindTool {
			tools.Content = append(tools.Content, f)
		} else {
			own.Content = append(own.Content, f)
		}
	}
	st, err := s.ImportPack(ctx, ws, own, actor)
	if err != nil {
		return st, err
	}
	if len(tools.Content) == 0 {
		return st, nil
	}
	toolStats, err := s.ImportPack(ctx, GlobalTenant, tools, actor)
	st.Files += toolStats.Files
	st.Bytes += toolStats.Bytes
	st.Tools += toolStats.Tools
	st.Skipped = append(st.Skipped, toolStats.Skipped...)
	return st, err
}

func (s *Store) VacuumAll(ctx context.Context, keepPerFile int) (prunedRevisions, freedBlobs int64, err error) {
	if keepPerFile < 0 {
		keepPerFile = DefaultKeepPerFile
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM content_revisions
		  WHERE id IN (
		    SELECT id FROM (
		      SELECT id, row_number() OVER (
		               PARTITION BY tenant_id, kind, owner, path ORDER BY id DESC
		             ) AS rn
		        FROM content_revisions
		    ) ranked
		    WHERE ranked.rn > ?
		  )`), keepPerFile)
	if err != nil {
		return 0, 0, apperr.Wrap(err, "agentstore.errors.prune_revisions")
	}
	prunedRevisions, _ = res.RowsAffected()
	freedBlobs, err = s.GCBlobs(ctx)
	return prunedRevisions, freedBlobs, err
}
