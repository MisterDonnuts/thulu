package mcpoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

// Never change it.
const MigrationComponent = "mcp_oauth"

const FlowLabel = "octopus-mcp-oauth-flow-v1"

func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS mcp_oauth_flows (
				state_hash   TEXT PRIMARY KEY,
				binder_hash  TEXT NOT NULL,
				workspace_id TEXT NOT NULL,
				flow_sealed  TEXT NOT NULL,
				expires_at   TIMESTAMP NOT NULL,
				created_at   TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_mcp_oauth_flows_expires ON mcp_oauth_flows(expires_at);
		`},
	}, nil
}

func migrateFlows(ctx context.Context, db *sql.DB) error {
	list, err := Migrations(migrate.DBInspector(ctx, db))
	if err == nil {
		err = migrate.Run(ctx, db, MigrationComponent, list)
	}
	return apperr.Wrap(err, "mcpoauth.errors.create_tables")
}

type StartRequest struct {
	WorkspaceID  string
	Author       string
	CredentialID string
	Name         string
	Server       string
	ServerURL    string
	Transport    string
	Client       ClientRequest
	Scope        string
	Grants       []credentials.Grant
	// The caller must restrict Return to a fixed set; this package does not validate it.
	Return string
}

type Started struct {
	AuthorizeURL string
	Binder       string
	Registration string
	CredentialID string
	Name         string
}

type Finished struct {
	WorkspaceID  string
	Author       string
	CredentialID string
	Name         string
	Server       string
	Created      bool
	Return       string
}

type flow struct {
	WorkspaceID        string              `json:"workspace_id"`
	Author             string              `json:"author"`
	CredentialID       string              `json:"credential_id,omitempty"`
	Name               string              `json:"name"`
	Server             string              `json:"server"`
	Return             string              `json:"return"`
	Grants             []credentials.Grant `json:"grants,omitempty"`
	Verifier           string              `json:"verifier"`
	RedirectURI        string              `json:"redirect_uri"`
	Resource           string              `json:"resource"`
	Scope              string              `json:"scope,omitempty"`
	Issuer             string              `json:"issuer"`
	TokenEndpoint      string              `json:"token_endpoint"`
	RevocationEndpoint string              `json:"revocation_endpoint,omitempty"`
	Client             Client              `json:"client"`
}

func (s *Service) Start(ctx context.Context, req StartRequest) (Started, error) {
	if req.WorkspaceID == "" {
		return Started{}, apperr.Invalid("mcpoauth.errors.workspace_required")
	}
	if _, err := CanonicalResource(req.Client.RedirectURI); err != nil {
		return Started{}, err
	}
	prev, err := s.previous(ctx, &req)
	if err != nil {
		return Started{}, err
	}
	disc, err := s.Discover(ctx, req.ServerURL, req.Transport)
	if err != nil {
		return Started{}, err
	}
	if prev != nil && req.Client.ID == "" && prev.Registration == RegistrationManual && prev.Issuer == disc.Server.Issuer {
		req.Client.ID, req.Client.Secret = prev.ClientID, prev.ClientSecret
	}
	client, err := s.RegisterClient(ctx, req.ServerURL, disc.Server, req.Client)
	if err != nil {
		return Started{}, err
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" && prev != nil {
		scope = prev.Scope
	}
	if scope == "" {
		scope = disc.Scope
	}

	verifier, err := randomToken()
	if err != nil {
		return Started{}, err
	}
	state, err := randomToken()
	if err != nil {
		return Started{}, err
	}
	binder, err := randomToken()
	if err != nil {
		return Started{}, err
	}
	f := flow{
		WorkspaceID: req.WorkspaceID, Author: req.Author, CredentialID: req.CredentialID,
		Name: req.Name, Server: req.Server, Return: req.Return, Grants: req.Grants,
		Verifier: verifier, RedirectURI: req.Client.RedirectURI, Resource: disc.Resource, Scope: scope,
		Issuer: disc.Server.Issuer, TokenEndpoint: disc.Server.TokenEndpoint,
		RevocationEndpoint: disc.Server.RevocationEndpoint, Client: client,
	}
	if err := s.saveFlow(ctx, state, binder, f); err != nil {
		return Started{}, err
	}

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ID},
		"redirect_uri":          {req.Client.RedirectURI},
		"state":                 {state},
		"code_challenge":        {challengeS256(verifier)},
		"code_challenge_method": {"S256"},
		"resource":              {disc.Resource},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL := disc.Server.AuthorizationEndpoint
	sep := "?"
	if strings.Contains(authURL, "?") {
		sep = "&"
	}
	return Started{
		AuthorizeURL: authURL + sep + q.Encode(), Binder: binder, Registration: client.Registration,
		CredentialID: req.CredentialID, Name: req.Name,
	}, nil
}

func (s *Service) previous(ctx context.Context, req *StartRequest) (*credentials.OAuthConnection, error) {
	if req.CredentialID == "" {
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			return nil, apperr.Invalid("credentials.errors.name_required")
		}
		list, err := s.creds.List(ctx, req.WorkspaceID)
		if err != nil {
			return nil, err
		}
		for _, c := range list {
			if c.Name != req.Name {
				continue
			}
			if !credentials.IsOAuth(c.Provider) {
				return nil, credentials.ErrExists
			}
			req.CredentialID = c.ID
		}
		if req.CredentialID == "" {
			return nil, nil
		}
	}
	cred, err := s.creds.Get(ctx, req.WorkspaceID, req.CredentialID)
	if err != nil {
		return nil, err
	}
	if !credentials.IsOAuth(cred.Provider) {
		return nil, ErrNotOAuth
	}
	req.Name = cred.Name
	payload, err := s.creds.Payload(ctx, req.WorkspaceID, cred.ID)
	if err != nil {
		return nil, err
	}
	conn := credentials.ParseOAuthConnection(payload)
	return &conn, nil
}

func (s *Service) Finish(ctx context.Context, state, binder, code, errCode, errDetail string) (Finished, error) {
	f, err := s.consumeFlow(ctx, state, binder)
	if err != nil {
		return Finished{}, err
	}
	out := Finished{WorkspaceID: f.WorkspaceID, Author: f.Author, CredentialID: f.CredentialID, Name: f.Name, Server: f.Server, Return: f.Return}
	if errCode != "" {
		return out, apperr.Invalid("mcpoauth.errors.denied", "code", errCode, "detail", errDetail)
	}
	if code == "" {
		return out, ErrState
	}
	conn := credentials.OAuthConnection{
		Server: f.Server, Resource: f.Resource, Issuer: f.Issuer,
		TokenEndpoint: f.TokenEndpoint, RevocationEndpoint: f.RevocationEndpoint,
		Registration: f.Client.Registration, ClientID: f.Client.ID, ClientSecret: f.Client.Secret,
		ClientAuth: f.Client.Auth, Scope: f.Scope, ConnectedBy: f.Author, ConnectedAt: s.now().UTC(),
	}
	err = s.requestToken(ctx, &conn, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {f.RedirectURI},
		"code_verifier": {f.Verifier},
	})
	if err != nil {
		return out, err
	}

	if f.CredentialID != "" {
		existing, getErr := s.creds.Get(ctx, f.WorkspaceID, f.CredentialID)
		if getErr != nil {
			return out, getErr
		}
		if !credentials.IsOAuth(existing.Provider) {
			return out, ErrNotOAuth
		}
		return out, s.creds.SetPayload(ctx, f.WorkspaceID, existing.ID, conn.Payload(), conn.Introspection())
	}
	cred, err := s.creds.Create(ctx, f.WorkspaceID, credentials.Input{
		Name: f.Name, Provider: credentials.OAuthProviderID, Kind: credentials.OAuthKindMCP, Payload: conn.Payload(),
	}, f.Author)
	if err != nil {
		return out, err
	}
	out.CredentialID, out.Created = cred.ID, true
	if err := s.creds.SetPayload(ctx, f.WorkspaceID, cred.ID, conn.Payload(), conn.Introspection()); err != nil {
		return out, err
	}
	if len(f.Grants) > 0 {
		if err := s.creds.SetGrants(ctx, f.WorkspaceID, cred.ID, f.Grants); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Service) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Service) saveFlow(ctx context.Context, state, binder string, f flow) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	sealed, err := secretbox.Encrypt(FlowLabel, string(raw))
	if err != nil {
		return apperr.Wrap(err, "mcpoauth.errors.save_flow")
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(`DELETE FROM mcp_oauth_flows WHERE expires_at <= ?`), now)
	logx.Warn(err, "mcpoauth.log.prune_failed")
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO mcp_oauth_flows (state_hash, binder_hash, workspace_id, flow_sealed, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		tokenHash(state), tokenHash(binder), f.WorkspaceID, sealed, now.Add(FlowTTL), now)
	return apperr.Wrap(err, "mcpoauth.errors.save_flow")
}

// Delete-and-return in one statement so racing callbacks cannot both get the flow. A wrong binder
// leaves the row,
// so someone holding only the state cannot burn the real browser's flow.
func (s *Service) consumeFlow(ctx context.Context, state, binder string) (*flow, error) {
	if state == "" || binder == "" {
		return nil, ErrState
	}
	var sealed string
	err := s.db.QueryRowContext(ctx, s.rb(
		`DELETE FROM mcp_oauth_flows
		  WHERE state_hash = ? AND binder_hash = ? AND expires_at > ?
		 RETURNING flow_sealed`),
		tokenHash(state), tokenHash(binder), s.now().UTC()).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrState
	}
	if err != nil {
		return nil, err
	}
	raw, err := secretbox.Decrypt(FlowLabel, sealed)
	if err != nil {
		return nil, apperr.Wrap(err, "mcpoauth.errors.read_flow")
	}
	var f flow
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, apperr.Wrap(err, "mcpoauth.errors.read_flow")
	}
	return &f, nil
}

// 32 bytes gives a 43-char PKCE verifier, the RFC 7636 minimum.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
