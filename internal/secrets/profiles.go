package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

type LLMProfile struct {
	ID             string                   `json:"id"`
	WorkspaceID    string                   `json:"workspace_id"`
	Name           string                   `json:"name"`
	Engine         string                   `json:"engine"`
	Model          string                   `json:"model,omitempty"`
	Effort         string                   `json:"effort,omitempty"`
	BaseURL        string                   `json:"base_url,omitempty"`
	OrgID          string                   `json:"org_id,omitempty"`
	Provider       string                   `json:"provider,omitempty"`
	AuthHeader     string                   `json:"auth_header,omitempty"`
	Region         string                   `json:"region,omitempty"`
	ProjectID      string                   `json:"project_id,omitempty"`
	SmallFastModel string                   `json:"small_fast_model,omitempty"`
	Federation     *engine.FederationConfig `json:"federation,omitempty"`
	KeyHint        string                   `json:"key_hint,omitempty"`
	HasKey         bool                     `json:"has_key"`
	IsDefault      bool                     `json:"is_default"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
	LastUsedAt     time.Time                `json:"last_used_at,omitzero"`
	KeyState       string                   `json:"key_state,omitempty"`
	KeyCheckedAt   time.Time                `json:"key_checked_at,omitzero"`
	KeyReason      string                   `json:"key_reason,omitempty"`
	KeyStatus      int                      `json:"key_status,omitempty"`
	KeyHost        string                   `json:"key_host,omitempty"`
	RefusedStatus  int                      `json:"refused_status,omitempty"`
	RefusedAt      time.Time                `json:"refused_at,omitzero"`
}

const (
	KeyValid       = "valid"
	KeyInvalid     = "invalid"
	KeyUnreachable = "unreachable"
	KeyUnchecked   = "unchecked"
)

// Stored codes the web UI translates; a changed or new value needs the matching UI string.
const (
	KeyNoUpstream      = "no_upstream"
	KeyNotCheckable    = "not_checkable"
	KeyEndpointRefused = "endpoint_refused"
	KeyNoAnswer        = "no_answer"
	KeyRejected        = "rejected"
	KeyUnexpected      = "unexpected"
)

type KeyCheck struct {
	State  string
	Reason string
	Status int
	Host   string
}

type LLMProfileInput struct {
	Name           string
	Engine         string
	Model          string
	Effort         string
	BaseURL        string
	OrgID          string
	Provider       string
	AuthHeader     string
	Region         string
	ProjectID      string
	SmallFastModel string
	// A federating profile stores no key: set ClearKey (update) or KeyOptional (create) with it.
	Federation  engine.FederationConfig
	RawKey      string
	KeyHint     string
	KeyOptional bool
	ClearKey    bool
	IsDefault   bool
}

type AgentBinding struct {
	WorkspaceID string    `json:"workspace_id"`
	AgentName   string    `json:"agent_name"`
	ProfileID   string    `json:"profile_id"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
}

type ResolvedLLM struct {
	Source      string
	ProfileID   string
	ProfileName string

	Engine   string
	Model    string
	Effort   string
	BaseURL  string
	OrgID    string
	Provider string

	AuthHeader     string
	Region         string
	ProjectID      string
	SmallFastModel string
	Federation     engine.FederationConfig

	Credential string
}

func (r ResolvedLLM) ForAgent(agentModel, agentEffort string) ResolvedLLM {
	if r.Source == "binding" {
		if r.Model == "" {
			r.Model = agentModel
		}
		if r.Effort == "" {
			r.Effort = agentEffort
		}
		return r
	}
	if agentModel != "" {
		r.Model = agentModel
	}
	if agentEffort != "" {
		r.Effort = agentEffort
	}
	return r
}

func (s *Store) CreateLLMProfile(ctx context.Context, workspaceID string, in LLMProfileInput) (*LLMProfile, error) {
	if workspaceID == "" {
		return nil, apperr.Invalid("secrets.errors.workspace_id_required")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperr.Invalid("secrets.errors.name_required")
	}
	if strings.TrimSpace(in.Engine) == "" {
		return nil, apperr.Invalid("secrets.errors.engine_required")
	}
	if strings.TrimSpace(in.RawKey) == "" && !in.KeyOptional {
		return nil, apperr.Invalid("secrets.errors.key_required")
	}
	enc, err := secretbox.Encrypt(llmKeyInfo, in.RawKey)
	if err != nil {
		return nil, apperr.Wrap(err, "secrets.errors.encrypt_profile_key")
	}
	id := newProfileID()
	now := time.Now().UTC()
	hint := keyHint(in)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.IsDefault {
		if _, err = tx.ExecContext(ctx, s.rb(
			`UPDATE llm_profiles SET is_default = FALSE
			  WHERE workspace_id = ? AND engine = ? AND is_default`), workspaceID, in.Engine); err != nil {
			return nil, err
		}
	}
	_, err = tx.ExecContext(ctx, s.rb(
		`INSERT INTO llm_profiles
		   (id, workspace_id, name, engine, model, effort, base_url, org_id,
		    provider, key_encrypted, key_hint, created_at, updated_at, is_default,
		    auth_header, region, project_id, small_fast_model, federation)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		id, workspaceID, in.Name, in.Engine, in.Model, in.Effort, in.BaseURL,
		in.OrgID, in.Provider, enc, hint, now, now, in.IsDefault,
		in.AuthHeader, in.Region, in.ProjectID, in.SmallFastModel, in.Federation.Encode())
	if err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return s.GetLLMProfile(ctx, workspaceID, id)
}

func (s *Store) UpdateLLMProfile(ctx context.Context, workspaceID, id string, in LLMProfileInput) (*LLMProfile, error) {
	if workspaceID == "" || id == "" {
		return nil, apperr.Invalid("secrets.errors.workspace_and_id_required")
	}
	now := time.Now().UTC()
	if strings.TrimSpace(in.RawKey) == "" && !in.ClearKey {
		_, err := s.db.ExecContext(ctx, s.rb(
			`UPDATE llm_profiles
			   SET name = COALESCE(NULLIF(?, ''), name),
			       engine = COALESCE(NULLIF(?, ''), engine),
			       model = ?,
			       effort = ?,
			       base_url = ?,
			       org_id = ?,
			       provider = COALESCE(NULLIF(?, ''), provider),
			       auth_header = ?,
			       region = ?,
			       project_id = ?,
			       small_fast_model = ?,
			       federation = ?,
			       updated_at = ?
			 WHERE workspace_id = ? AND id = ?`),
			in.Name, in.Engine, in.Model, in.Effort, in.BaseURL, in.OrgID,
			in.Provider, in.AuthHeader, in.Region, in.ProjectID, in.SmallFastModel,
			in.Federation.Encode(), now, workspaceID, id)
		if err != nil {
			return nil, err
		}
	} else {
		enc, err := secretbox.Encrypt(llmKeyInfo, in.RawKey)
		if err != nil {
			return nil, apperr.Wrap(err, "secrets.errors.encrypt_profile_key")
		}
		hint := keyHint(in)
		_, err = s.db.ExecContext(ctx, s.rb(
			`UPDATE llm_profiles
			   SET name = COALESCE(NULLIF(?, ''), name),
			       engine = COALESCE(NULLIF(?, ''), engine),
			       model = ?,
			       effort = ?,
			       base_url = ?,
			       org_id = ?,
			       provider = COALESCE(NULLIF(?, ''), provider),
			       auth_header = ?,
			       region = ?,
			       project_id = ?,
			       small_fast_model = ?,
			       federation = ?,
			       key_encrypted = ?,
			       key_hint = ?,
			       updated_at = ?
			 WHERE workspace_id = ? AND id = ?`),
			in.Name, in.Engine, in.Model, in.Effort, in.BaseURL, in.OrgID,
			in.Provider, in.AuthHeader, in.Region, in.ProjectID, in.SmallFastModel,
			in.Federation.Encode(), enc, hint, now, workspaceID, id)
		if err != nil {
			return nil, err
		}
	}
	return s.GetLLMProfile(ctx, workspaceID, id)
}

func (s *Store) GetLLMProfile(ctx context.Context, workspaceID, id string) (*LLMProfile, error) {
	if workspaceID == "" || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+profileColumns+`
		   FROM llm_profiles WHERE workspace_id = ? AND id = ?`), workspaceID, id)
	return scanProfile(row)
}

func (s *Store) GetLLMProfileByName(ctx context.Context, workspaceID, name string) (*LLMProfile, error) {
	if workspaceID == "" || name == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+profileColumns+`
		   FROM llm_profiles WHERE workspace_id = ? AND name = ?`), workspaceID, name)
	return scanProfile(row)
}

func (s *Store) ListLLMProfiles(ctx context.Context, workspaceID string) ([]*LLMProfile, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+profileColumns+`
		   FROM llm_profiles WHERE workspace_id = ? ORDER BY name`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LLMProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DefaultLLMProfile(ctx context.Context, workspaceID, engineName string) (*LLMProfile, error) {
	if workspaceID == "" || engineName == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+profileColumns+`
		   FROM llm_profiles WHERE workspace_id = ? AND engine = ? AND is_default`), workspaceID, engineName)
	return scanProfile(row)
}

func (s *Store) SetLLMProfileDefault(ctx context.Context, workspaceID, id string, on bool) (*LLMProfile, error) {
	p, err := s.GetLLMProfile(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if on {
		if _, err = tx.ExecContext(ctx, s.rb(
			`UPDATE llm_profiles SET is_default = FALSE
			  WHERE workspace_id = ? AND engine = ? AND is_default AND id <> ?`),
			workspaceID, p.Engine, id); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, s.rb(
		`UPDATE llm_profiles SET is_default = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`),
		on, time.Now().UTC(), workspaceID, id); err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return s.GetLLMProfile(ctx, workspaceID, id)
}

func (s *Store) BoundAgentsByProfile(ctx context.Context, workspaceID string) (map[string][]string, error) {
	bindings, err := s.ListAgentBindings(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, b := range bindings {
		out[b.ProfileID] = append(out[b.ProfileID], b.AgentName)
	}
	return out, nil
}

func (s *Store) freeProfileName(ctx context.Context, workspaceID, base string) (string, error) {
	for i := 1; i < 100; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		_, err := s.GetLLMProfileByName(ctx, workspaceID, name)
		if errors.Is(err, ErrNotFound) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", apperr.Conflict("secrets.errors.no_free_profile_name", "base", base)
}

func (s *Store) DeleteLLMProfile(ctx context.Context, workspaceID, id string) error {
	if workspaceID == "" || id == "" {
		return apperr.Invalid("secrets.errors.workspace_and_id_required")
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM agent_llm_bindings WHERE workspace_id = ? AND profile_id = ?`), workspaceID, id)
	var n int
	if err := row.Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrProfileInUse
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM llm_profiles WHERE workspace_id = ? AND id = ?`), workspaceID, id)
	return err
}

var ErrProfileInUse error = apperr.Conflict("secrets.errors.profile_in_use")

func (s *Store) GetLLMProfileCredential(ctx context.Context, workspaceID, id string) (rawKey string, err error) {
	if workspaceID == "" || id == "" {
		return "", ErrNotFound
	}
	var enc string
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT key_encrypted FROM llm_profiles WHERE workspace_id = ? AND id = ?`), workspaceID, id)
	if scanErr := row.Scan(&enc); errors.Is(scanErr, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if scanErr != nil {
		return "", scanErr
	}
	rawKey, err = secretbox.Decrypt(llmKeyInfo, enc)
	if err != nil {
		return "", apperr.Wrap(err, "secrets.errors.decrypt_profile_key")
	}
	return rawKey, nil
}

func (s *Store) TouchLLMProfile(ctx context.Context, workspaceID, id string) error {
	if workspaceID == "" || id == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE llm_profiles SET last_used_at = ? WHERE workspace_id = ? AND id = ?`),
		time.Now().UTC(), workspaceID, id)
	return err
}

func (s *Store) SetLLMProfileKeyCheck(ctx context.Context, workspaceID, id string, c KeyCheck) error {
	if workspaceID == "" || id == "" {
		return nil
	}
	q := `UPDATE llm_profiles SET key_state = ?, key_checked_at = ?, key_reason = ?, key_status = ?, key_host = ?
	       WHERE workspace_id = ? AND id = ?`
	if c.State == KeyValid {
		q = `UPDATE llm_profiles SET key_state = ?, key_checked_at = ?, key_reason = ?, key_status = ?, key_host = ?,
		            refused_status = 0, refused_at = NULL
		      WHERE workspace_id = ? AND id = ?`
	}
	_, err := s.db.ExecContext(ctx, s.rb(q), c.State, time.Now().UTC(), c.Reason, c.Status, c.Host, workspaceID, id)
	return err
}

func (s *Store) SetLLMProfileRefusal(ctx context.Context, workspaceID, id string, status int) (bool, error) {
	if workspaceID == "" || id == "" {
		return false, nil
	}
	var at any
	if status != 0 {
		at = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE llm_profiles SET refused_status = ?, refused_at = ?
		  WHERE workspace_id = ? AND id = ? AND refused_status <> ?`),
		status, at, workspaceID, id, status)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) SetAgentBinding(ctx context.Context, workspaceID, agentName, profileID, updatedBy string) error {
	if workspaceID == "" || agentName == "" || profileID == "" {
		return apperr.Invalid("secrets.errors.binding_fields_required")
	}
	// Reject a profile id from another workspace so a crafted id cannot bind across tenants.
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM llm_profiles WHERE workspace_id = ? AND id = ?`), workspaceID, profileID)
	var n int
	if err := row.Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO agent_llm_bindings(workspace_id, agent_name, profile_id, updated_at, updated_by)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_name) DO UPDATE SET
		   profile_id = excluded.profile_id,
		   updated_at = excluded.updated_at,
		   updated_by = excluded.updated_by`),
		workspaceID, agentName, profileID, now, updatedBy)
	return err
}

func (s *Store) GetAgentBinding(ctx context.Context, workspaceID, agentName string) (*AgentBinding, error) {
	if workspaceID == "" || agentName == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT workspace_id, agent_name, profile_id, updated_at, updated_by
		   FROM agent_llm_bindings WHERE workspace_id = ? AND agent_name = ?`), workspaceID, agentName)
	var b AgentBinding
	err := row.Scan(&b.WorkspaceID, &b.AgentName, &b.ProfileID, &b.UpdatedAt, &b.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteAgentBinding(ctx context.Context, workspaceID, agentName string) error {
	if workspaceID == "" || agentName == "" {
		return apperr.Invalid("secrets.errors.workspace_and_agent_required")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM agent_llm_bindings WHERE workspace_id = ? AND agent_name = ?`),
		workspaceID, agentName)
	return err
}

func (s *Store) ListAgentBindings(ctx context.Context, workspaceID string) ([]*AgentBinding, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT workspace_id, agent_name, profile_id, updated_at, updated_by
		   FROM agent_llm_bindings WHERE workspace_id = ? ORDER BY agent_name`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentBinding
	for rows.Next() {
		var b AgentBinding
		if err := rows.Scan(&b.WorkspaceID, &b.AgentName, &b.ProfileID, &b.UpdatedAt, &b.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

func (s *Store) ResolveLLMForAgent(ctx context.Context, workspaceID, agentName, fallbackEngine string) (ResolvedLLM, error) {
	out := ResolvedLLM{Source: "host", Engine: fallbackEngine}
	if workspaceID == "" {
		return out, nil
	}
	if res, ok, err := s.resolveBindingLayer(ctx, workspaceID, agentName); err != nil {
		return out, err
	} else if ok {
		return res, nil
	}
	if res, ok, err := s.resolveDefaultProfileLayer(ctx, workspaceID, fallbackEngine); err != nil {
		return out, err
	} else if ok {
		return res, nil
	}
	if res, ok, err := s.resolveLegacyKeyLayer(ctx, workspaceID, fallbackEngine); err != nil {
		return out, err
	} else if ok {
		return res, nil
	}
	return out, nil
}

func (s *Store) resolveBindingLayer(ctx context.Context, workspaceID, agentName string) (ResolvedLLM, bool, error) {
	b, err := s.GetAgentBinding(ctx, workspaceID, agentName)
	if errors.Is(err, ErrNotFound) {
		return ResolvedLLM{}, false, nil
	}
	if err != nil {
		return ResolvedLLM{}, false, err
	}
	p, perr := s.GetLLMProfile(ctx, workspaceID, b.ProfileID)
	if perr != nil {
		return ResolvedLLM{}, false, nil
	}
	cred, cerr := s.GetLLMProfileCredential(ctx, workspaceID, p.ID)
	if cerr != nil {
		return ResolvedLLM{}, false, nil
	}
	return profileToResolved(p, cred, "binding"), true, nil
}

func (s *Store) resolveDefaultProfileLayer(ctx context.Context, workspaceID, fallbackEngine string) (ResolvedLLM, bool, error) {
	if fallbackEngine == "" {
		return ResolvedLLM{}, false, nil
	}
	p, err := s.DefaultLLMProfile(ctx, workspaceID, fallbackEngine)
	if errors.Is(err, ErrNotFound) {
		return ResolvedLLM{}, false, nil
	}
	if err != nil {
		return ResolvedLLM{}, false, err
	}
	cred, cerr := s.GetLLMProfileCredential(ctx, workspaceID, p.ID)
	if cerr != nil {
		return ResolvedLLM{}, false, nil
	}
	return profileToResolved(p, cred, "workspace_default"), true, nil
}

func (s *Store) resolveLegacyKeyLayer(ctx context.Context, workspaceID, fallbackEngine string) (ResolvedLLM, bool, error) {
	if fallbackEngine == "" {
		return ResolvedLLM{}, false, nil
	}
	provider, key, err := s.legacyLLMKey(ctx, workspaceID, fallbackEngine)
	if errors.Is(err, ErrNotFound) || key == "" {
		return ResolvedLLM{}, false, nil
	}
	if err != nil {
		return ResolvedLLM{}, false, err
	}
	return ResolvedLLM{
		Source:     "legacy",
		Engine:     fallbackEngine,
		Provider:   provider,
		Credential: key,
	}, true, nil
}

func profileToResolved(p *LLMProfile, cred, source string) ResolvedLLM {
	return ResolvedLLM{
		Source:         source,
		ProfileID:      p.ID,
		ProfileName:    p.Name,
		Engine:         p.Engine,
		Model:          p.Model,
		Effort:         p.Effort,
		BaseURL:        p.BaseURL,
		OrgID:          p.OrgID,
		Provider:       p.Provider,
		AuthHeader:     p.AuthHeader,
		Region:         p.Region,
		ProjectID:      p.ProjectID,
		SmallFastModel: p.SmallFastModel,
		Federation:     p.FederationConfig(),
		Credential:     cred,
	}
}

func (p *LLMProfile) FederationConfig() engine.FederationConfig {
	if p == nil || p.Federation == nil {
		return engine.FederationConfig{}
	}
	return *p.Federation
}

// Order must match the Scan in scanProfile.
//
//i18n:ignore SQL column list
const profileColumns = `id, workspace_id, name, engine, model, effort, base_url, org_id,
		        provider, key_hint, created_at, updated_at, last_used_at, is_default,
		        auth_header, region, project_id, small_fast_model, key_encrypted <> '',
		        key_state, key_checked_at, key_reason, key_status, key_host, refused_status, refused_at,
		        federation`

func keyHint(in LLMProfileInput) string {
	switch {
	case in.KeyHint != "":
		return in.KeyHint
	case in.RawKey == "":
		return ""
	default:
		return secretbox.Mask(in.RawKey)
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProfile(r rowScanner) (*LLMProfile, error) {
	var (
		p                       LLMProfile
		lastUsedRaw, checkedRaw sql.NullTime
		refusedRaw              sql.NullTime
		federationRaw           string
	)
	if err := r.Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Engine, &p.Model, &p.Effort,
		&p.BaseURL, &p.OrgID, &p.Provider, &p.KeyHint,
		&p.CreatedAt, &p.UpdatedAt, &lastUsedRaw, &p.IsDefault,
		&p.AuthHeader, &p.Region, &p.ProjectID, &p.SmallFastModel, &p.HasKey,
		&p.KeyState, &checkedRaw, &p.KeyReason, &p.KeyStatus, &p.KeyHost, &p.RefusedStatus, &refusedRaw,
		&federationRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	fed, err := engine.DecodeFederation(federationRaw)
	if err != nil {
		return nil, err
	}
	if fed.Set() {
		p.Federation = &fed
	}
	if lastUsedRaw.Valid {
		p.LastUsedAt = lastUsedRaw.Time
	}
	if checkedRaw.Valid {
		p.KeyCheckedAt = checkedRaw.Time
	}
	if refusedRaw.Valid {
		p.RefusedAt = refusedRaw.Time
	}
	return &p, nil
}

func newProfileID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "lp_" + hex.EncodeToString(b)
}
