package agentstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type Agent struct {
	types.AgentType
	IsTemplate bool       `json:"is_template,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
}

// Agents are disabled, never deleted: tasks store the agent name per stage, and a deleted row
// would dangle every historical task and break run bundles still in flight.
func (a Agent) Disabled() bool { return a.DisabledAt != nil }

func (s *Store) PutAgent(ctx context.Context, tenant string, a Agent, actor string) (Agent, error) {
	return s.putAgent(ctx, s.db, tenant, a, actor)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) putAgent(ctx context.Context, ex execer, tenant string, a Agent, actor string) (Agent, error) {
	if err := ValidateAgentName(a.Name); err != nil {
		return Agent{}, err
	}
	cols, err := marshalAgent(a)
	if err != nil {
		return Agent{}, err
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now

	_, err = ex.ExecContext(ctx, s.rb(
		`INSERT INTO agents (tenant_id, name, description, type, engine, supported_engines,
		                     model, effort, skills_mode, works_without_repo, valid_stages,
		                     turns_by_stage, skills_by_stage, mcp_by_stage, auditor, params,
		                     token_optimizer, image, is_template, created_by, created_at, updated_at, disabled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tenant_id, name) DO UPDATE SET
		     description = EXCLUDED.description, type = EXCLUDED.type,
		     engine = EXCLUDED.engine, supported_engines = EXCLUDED.supported_engines,
		     model = EXCLUDED.model, effort = EXCLUDED.effort,
		     skills_mode = EXCLUDED.skills_mode,
		     works_without_repo = EXCLUDED.works_without_repo,
		     valid_stages = EXCLUDED.valid_stages,
		     turns_by_stage = EXCLUDED.turns_by_stage,
		     skills_by_stage = EXCLUDED.skills_by_stage,
		     mcp_by_stage = EXCLUDED.mcp_by_stage,
		     auditor = EXCLUDED.auditor, params = EXCLUDED.params,
		     token_optimizer = EXCLUDED.token_optimizer,
		     image = EXCLUDED.image, is_template = EXCLUDED.is_template,
		     updated_at = EXCLUDED.updated_at, disabled_at = EXCLUDED.disabled_at`),
		tenant, a.Name, a.Description, a.Type, a.Engine, cols.supportedEngines,
		a.Model, a.Effort, a.SkillsMode, a.WorksWithoutRepo, cols.validStages,
		cols.turns, cols.skills, cols.mcp, cols.auditor, cols.params,
		cols.tokenOptimizer, a.Image, a.IsTemplate, actor, a.CreatedAt, a.UpdatedAt, a.DisabledAt)
	if err != nil {
		return Agent{}, apperr.Wrap(err, "agentstore.errors.put_agent", "name", a.Name)
	}
	a.CreatedBy = actor
	return a, nil
}

func (s *Store) CreateAgent(ctx context.Context, tenant string, a Agent, actor string) (Agent, error) {
	retired, err := s.retiredName(ctx, tenant, a.Name)
	if err != nil {
		return Agent{}, err
	}
	if !retired {
		return s.PutAgent(ctx, tenant, a, actor)
	}
	a.DisabledAt = nil
	if txErr := s.InTx(ctx, func(tx *Tx) error {
		if clearErr := tx.resetRetiredAgent(tenant, a.Name, actor); clearErr != nil {
			return clearErr
		}
		_, putErr := tx.PutAgent(tenant, a, actor)
		return putErr
	}); txErr != nil {
		return Agent{}, txErr
	}
	return s.GetAgent(ctx, tenant, a.Name)
}

func (s *Store) retiredName(ctx context.Context, tenant, name string) (bool, error) {
	existing, err := s.GetAgent(ctx, tenant, name)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case !existing.Disabled():
		return false, ErrExists
	}
	return true, nil
}

// Files go through DeleteFile so the old prompts stay in the history; the origin must go too,
// or the Workshop treats the new agent as a copy of the old one's catalog entry.
func (t *Tx) resetRetiredAgent(tenant, name, actor string) error {
	paths, err := dbpkg.CollectStrings(t.ctx, t.tx,
		`SELECT path FROM content_files WHERE tenant_id = ? AND kind = ? AND owner = ? ORDER BY path`,
		tenant, KindAgent, name)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.list_content", "kind", KindAgent, "owner", name)
	}
	for _, p := range paths {
		if err := t.DeleteFile(tenant, KindAgent, name, p, actor); err != nil {
			return err
		}
	}
	return t.ForgetOrigin(tenant, KindAgent, name)
}

func (s *Store) CloneAgent(ctx context.Context, tenant, from, to, actor string) (Agent, error) {
	return s.CloneAgentWith(ctx, tenant, from, to, actor, nil)
}

func (s *Store) CloneAgentWith(ctx context.Context, tenant, from, to, actor string,
	edit func(*types.AgentType),
) (Agent, error) {
	if err := ValidateAgentName(to); err != nil {
		return Agent{}, err
	}
	src, err := s.GetAgent(ctx, tenant, from)
	if err != nil {
		return Agent{}, err
	}
	retired, err := s.retiredName(ctx, tenant, to)
	if err != nil {
		return Agent{}, err
	}
	if retired {
		if err := s.InTx(ctx, func(tx *Tx) error { return tx.resetRetiredAgent(tenant, to, actor) }); err != nil {
			return Agent{}, err
		}
	}

	clone := src
	clone.Name = to
	clone.Dir = ""
	clone.CreatedAt = time.Time{}
	clone.DisabledAt = nil
	clone.IsTemplate = false
	if clone.Type == from {
		clone.Type = to
	}
	if edit != nil {
		edit(&clone.AgentType)
	}
	if _, err := s.PutAgent(ctx, tenant, clone, actor); err != nil {
		return Agent{}, err
	}

	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO content_files (tenant_id, kind, owner, path, blob_sha, updated_at, updated_by)
		 SELECT tenant_id, kind, ?, path, blob_sha, ?, ?
		   FROM content_files
		  WHERE tenant_id = ? AND kind = ? AND owner = ?
		 ON CONFLICT (tenant_id, kind, owner, path) DO NOTHING`),
		to, time.Now().UTC(), actor, tenant, KindAgent, from); err != nil {
		return Agent{}, apperr.Wrap(err, "agentstore.errors.clone_content", "from", from, "to", to)
	}
	return s.GetAgent(ctx, tenant, to)
}

func (s *Store) GetAgent(ctx context.Context, tenant, name string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, s.rb(agentSelect+` WHERE tenant_id = ? AND name = ?`), tenant, name)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, apperr.Wrap(err, "agentstore.errors.get_agent", "name", name)
	}
	return a, nil
}

// Includes every tenant and disabled agents: the janitor must keep a local-only image an agent
// may run again, since it could not be pulled back.
func (s *Store) Images(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT image FROM agents WHERE image <> ''`)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_agent_images")
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var img string
		if err := rows.Scan(&img); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.scan_agent_image")
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

func (s *Store) ListAgents(ctx context.Context, tenant string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		agentSelect+` WHERE tenant_id = ? AND disabled_at IS NULL ORDER BY name`), tenant)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_agents")
	}
	defer func() { _ = rows.Close() }()

	var out []Agent
	for rows.Next() {
		a, scanErr := scanAgent(rows)
		if scanErr != nil {
			return nil, apperr.Wrap(scanErr, "agentstore.errors.scan_agent")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_agents")
	}
	return out, nil
}

func (s *Store) SetDisabled(ctx context.Context, tenant, name string, disabled bool) error {
	var at any
	if disabled {
		at = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE agents SET disabled_at = ?, updated_at = ? WHERE tenant_id = ? AND name = ?`),
		at, time.Now().UTC(), tenant, name)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.set_disabled", "name", name)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Fingerprint(ctx context.Context, tenant string) (string, error) {
	var (
		n       int
		updated sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*), max(updated_at) FROM agents WHERE tenant_id = ?`), tenant).Scan(&n, &updated)
	if err != nil {
		return "", apperr.Wrap(err, "agentstore.errors.agents_fingerprint")
	}
	if !updated.Valid {
		return fmt.Sprintf("%d:none", n), nil
	}
	return fmt.Sprintf("%d:%d", n, updated.Time.UnixNano()), nil
}

const agentSelect = `SELECT name, description, type, engine, supported_engines, model, effort,
	skills_mode, works_without_repo, valid_stages, turns_by_stage, skills_by_stage,
	mcp_by_stage, auditor, params, token_optimizer, image, is_template, created_by,
	created_at, updated_at, disabled_at FROM agents`

type agentCols struct {
	supportedEngines, validStages, turns, skills, mcp, params []byte
	tokenOptimizer                                            []byte
	auditor                                                   any
}

func marshalAgent(a Agent) (agentCols, error) {
	var (
		c   agentCols
		err error
	)
	enc := func(v any) []byte {
		if err != nil {
			return nil
		}
		var b []byte
		b, err = json.Marshal(v)
		return b
	}
	// The columns are NOT NULL: encode nil slices/maps as empty JSON, never null.
	c.supportedEngines = enc(nonNilStrings(a.SupportedEngines))
	c.validStages = enc(nonNilStages(a.ValidStages))
	c.turns = enc(nonNilStringMap(a.TurnsByStage))
	c.skills = enc(skillsColumn(a.SkillsByStage, a.SystemSkillsOff))
	c.mcp = enc(nonNilSliceMap(a.MCPByStage))
	c.params = enc(nonNilParams(a.Params))
	c.tokenOptimizer = enc(a.TokenOptimizer)
	if a.Auditor != nil {
		c.auditor = enc(a.Auditor)
	}
	if err != nil {
		return agentCols{}, apperr.Wrap(err, "agentstore.errors.encode_agent", "name", a.Name)
	}
	return c, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanAgent(sc rowScanner) (Agent, error) {
	var (
		a                                     Agent
		supported, stages, turns, skills, mcp []byte
		params, tokenOpt                      []byte
		auditor                               []byte
		disabled                              sql.NullTime
	)
	if err := sc.Scan(&a.Name, &a.Description, &a.Type, &a.Engine, &supported, &a.Model,
		&a.Effort, &a.SkillsMode, &a.WorksWithoutRepo, &stages, &turns, &skills, &mcp,
		&auditor, &params, &tokenOpt, &a.Image, &a.IsTemplate, &a.CreatedBy, &a.CreatedAt,
		&a.UpdatedAt, &disabled); err != nil {
		return Agent{}, err
	}
	if disabled.Valid {
		t := disabled.Time
		a.DisabledAt = &t
	}
	for _, d := range []struct {
		raw []byte
		dst any
	}{
		{supported, &a.SupportedEngines}, {stages, &a.ValidStages},
		{turns, &a.TurnsByStage}, {skills, &a.SkillsByStage},
		{mcp, &a.MCPByStage}, {params, &a.Params}, {tokenOpt, &a.TokenOptimizer},
	} {
		if len(d.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(d.raw, d.dst); err != nil {
			return Agent{}, apperr.Wrap(err, "agentstore.errors.decode_agent", "name", a.Name)
		}
	}
	if a.SkillsByStage != nil {
		a.SkillsByStage, a.SystemSkillsOff = splitSkillsColumn(a.SkillsByStage)
	}
	// Policies stored in the earlier shape must load as off (see TokenOptimizer.OrOff).
	a.TokenOptimizer = a.TokenOptimizer.OrOff()
	if len(auditor) > 0 && string(auditor) != "null" {
		var spec types.AuditorSpec
		if err := json.Unmarshal(auditor, &spec); err != nil {
			return Agent{}, apperr.Wrap(err, "agentstore.errors.decode_auditor", "name", a.Name)
		}
		a.Auditor = &spec
	}
	if a.SkillsMode == "" {
		a.SkillsMode = types.SkillsModeAuto
	}
	return a, nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilStages(v []types.Stage) []types.Stage {
	if v == nil {
		return []types.Stage{}
	}
	return v
}

func nonNilParams(v []types.ParamDef) []types.ParamDef {
	if v == nil {
		return []types.ParamDef{}
	}
	return v
}

func nonNilStringMap(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}

func nonNilSliceMap(v map[string][]string) map[string][]string {
	if v == nil {
		return map[string][]string{}
	}
	return v
}

// Keys in skills_by_stage holding SystemSkillsOff, so the setting needed no migration. Stage ids
// are
// [a-z0-9_] (types.ValidStageID), so no stage can start with "!"; an older daemon reads these as
// grants for a nonexistent stage, which is harmless.
const systemOffKeyPrefix = "!system_off:"

func skillsColumn(granted, off map[string][]string) map[string][]string {
	out := make(map[string][]string, len(granted)+len(off))
	for stage, names := range granted {
		out[stage] = names
	}
	for stage, names := range off {
		if len(names) > 0 {
			out[systemOffKeyPrefix+stage] = names
		}
	}
	return out
}

func splitSkillsColumn(col map[string][]string) (granted, off map[string][]string) {
	granted = make(map[string][]string, len(col))
	for key, names := range col {
		if stage, ok := strings.CutPrefix(key, systemOffKeyPrefix); ok {
			if off == nil {
				off = map[string][]string{}
			}
			off[stage] = names
			continue
		}
		granted[key] = names
	}
	return granted, off
}
