package githubapp

import (
	"context"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type ProjectLookup interface {
	Get(ctx context.Context, id string) (*project.Project, error)
}

type CredentialLookup interface {
	GithubTokenForProject(ctx context.Context, workspaceID, projectID string) (*credentials.GitToken, error)
}

type InstallationLookup interface {
	GetInstallation(ctx context.Context, id int64) (*Installation, error)
}

// The installation is re-authorized on every mint so unlink, uninstall, suspension or re-claim by
// another workspace cuts minting immediately.
// Mint failures fall back to the stored credential instead of failing.
type Resolver struct {
	svc      *Service
	installs InstallationLookup
	projects ProjectLookup
	creds    CredentialLookup
}

type Token struct {
	ProjectID    string `json:"project_id"`
	Token        string `json:"token"`
	Login        string `json:"login"`
	Source       string `json:"source"`
	CredentialID string `json:"credential_id,omitempty"`
}

const (
	SourceApp    = "app"
	SourceStored = "credential"
)

var ErrNoCredential error = apperr.NotFound("githubapp.errors.no_credential")

// A nil dependency disables its leg; pass a nil interface, not a typed nil pointer.
func NewResolver(svc *Service, installs InstallationLookup, projects ProjectLookup, creds CredentialLookup) *Resolver {
	return &Resolver{svc: svc, installs: installs, projects: projects, creds: creds}
}

// The tokenLookup interface carries no deadline from callers, so the resolver owns this one.
const mintTimeout = 20 * time.Second

const credentialTimeout = 5 * time.Second

// PermsAgentTools is the smallest set covering every caller of this entry point (MCP token
// endpoints, branch push, fallback PR).
func (r *Resolver) GetGithubToken(ctx context.Context, projectID string) (*Token, error) {
	return r.GetGithubTokenFor(ctx, projectID, PermsAgentTools)
}

func (r *Resolver) GetGithubTokenFor(ctx context.Context, projectID string, perms Permissions) (*Token, error) {
	if r.svc != nil && r.projects != nil && r.svc.Configured(ctx) {
		if proj, err := r.projects.Get(ctx, projectID); err == nil && proj.GithubInstallationID != 0 {
			tok, err := r.mintAuthorized(ctx, proj, perms)
			if err == nil {
				return &Token{
					ProjectID: projectID,
					Token:     tok,
					Login:     r.svc.BotLogin(ctx),
					Source:    SourceApp,
				}, nil
			}
			logx.Warn(err, "githubapp.log.app_token_fallback",
				"project", projectID, "installation", proj.GithubInstallationID)
		}
	}
	if tok, ok := r.credentialToken(ctx, projectID); ok {
		return tok, nil
	}
	return nil, ErrNoCredential
}

const noChecksTTL = 5 * time.Minute

func (r *Resolver) GetPRStatusToken(ctx context.Context, projectID string) (*Token, bool, error) {
	if r.svc != nil && r.projects != nil && r.svc.Configured(ctx) {
		if proj, err := r.projects.Get(ctx, projectID); err == nil && proj.GithubInstallationID != 0 {
			if tok, checks, ok := r.prStatusAppToken(ctx, proj); ok {
				return tok, checks, nil
			}
		}
	}
	if tok, ok := r.credentialToken(ctx, projectID); ok {
		return tok, true, nil
	}
	return nil, false, ErrNoCredential
}

func (r *Resolver) prStatusAppToken(ctx context.Context, proj *project.Project) (tok *Token, checks, ok bool) {
	inst := proj.GithubInstallationID
	wrap := func(t string) *Token {
		return &Token{ProjectID: proj.ID, Token: t, Login: r.svc.BotLogin(ctx), Source: SourceApp}
	}
	if !r.svc.lacksChecks(inst) {
		t, err := r.mintAuthorized(ctx, proj, PermsPRStatus)
		if err == nil {
			return wrap(t), true, true
		}
		if apperr.KindOf(err) != apperr.KindUnprocessable {
			logx.Warn(err, "githubapp.log.app_token_fallback", "project", proj.ID, "installation", inst)
			return nil, false, false
		}
		r.svc.markLacksChecks(inst, noChecksTTL)
	}
	t, err := r.mintAuthorized(ctx, proj, PermsPRStatusActions)
	if err != nil {
		logx.Warn(err, "githubapp.log.app_token_fallback", "project", proj.ID, "installation", inst)
		return nil, false, false
	}
	return wrap(t), false, true
}

func (r *Resolver) credentialToken(ctx context.Context, projectID string) (*Token, bool) {
	if r.creds == nil || r.projects == nil {
		return nil, false
	}
	proj, err := r.projects.Get(ctx, projectID)
	if err != nil || proj == nil || proj.WorkspaceID == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	git, err := r.creds.GithubTokenForProject(ctx, proj.WorkspaceID, projectID)
	if err != nil {
		if !errors.Is(err, credentials.ErrNotFound) {
			logx.Warn(err, "githubapp.log.read_credential_failed", "project", projectID)
		}
		return nil, false
	}
	return &Token{
		ProjectID:    projectID,
		Token:        git.Token,
		Login:        git.Identity,
		Source:       SourceStored,
		CredentialID: git.CredentialID,
	}, true
}

func (r *Resolver) mintAuthorized(ctx context.Context, proj *project.Project, perms Permissions) (string, error) {
	if err := r.authorizeInstallation(ctx, proj); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, mintTimeout)
	defer cancel()
	return r.mintForProject(ctx, proj, perms)
}

// A workspace-unscoped side (operator-created project or pre-claim link) passes.
func (r *Resolver) authorizeInstallation(ctx context.Context, proj *project.Project) error {
	if r.installs == nil {
		return apperr.Internal("githubapp.errors.no_installation_store")
	}
	inst, err := r.installs.GetInstallation(ctx, proj.GithubInstallationID)
	if err != nil {
		return apperr.Wrap(err, "githubapp.errors.installation_not_linked", "installation", proj.GithubInstallationID)
	}
	if inst.SuspendedAt != nil {
		return apperr.Precondition("githubapp.errors.installation_suspended", "installation", inst.InstallationID)
	}
	if inst.WorkspaceID != "" && proj.WorkspaceID != "" && inst.WorkspaceID != proj.WorkspaceID {
		return apperr.Forbidden("githubapp.errors.installation_other_workspace", "installation", inst.InstallationID)
	}
	return nil
}

// A repo URL that does not parse must fail the mint, never widen it to the whole installation.
func (r *Resolver) mintForProject(ctx context.Context, proj *project.Project, perms Permissions) (string, error) {
	_, repo, ok := github.ParseRemote(proj.RepoURL)
	if !ok || repo == "" {
		return "", apperr.Invalid("githubapp.errors.repo_url_not_a_repo", "url", proj.RepoURL)
	}
	return r.svc.MintToken(ctx, proj.GithubInstallationID, repo, perms)
}
