package envmetrics

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type CollectTarget struct {
	Environment CollectEnvironment `json:"environment"`
	Credential  CollectCredential  `json:"credential"`
}

// Shape must match what thulu-cloud's provider code reads (same fields as
// /internal/environments/{id}).
type CollectEnvironment struct {
	ID           string            `json:"environment_id"`
	ProjectID    string            `json:"project_id"`
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Region       string            `json:"region,omitempty"`
	Account      string            `json:"account,omitempty"`
	Selectors    map[string]string `json:"selectors,omitempty"`
	IsProduction bool              `json:"is_production"`
}

// Shape must match /internal/credentials/resolve.
type CollectCredential struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Kind     string            `json:"kind"`
	Payload  map[string]string `json:"payload"`
}

type TargetResolver interface {
	ResolveCollectTarget(ctx context.Context, envID string) (*CollectTarget, error)
}

type Targets struct {
	Projects *project.Store
	Creds    *credentials.Store
	Auth     *credentials.Authorizer
}

func NewTargets(projects *project.Store, creds *credentials.Store) *Targets {
	return &Targets{Projects: projects, Creds: creds, Auth: credentials.NewAuthorizer(creds, credentials.NewCatalog())}
}

// The environment is the named subject, so only a credential granted to it serves; a workspace-wide
// grant does not, same as for an agent's metrics_query.
func (t *Targets) ResolveCollectTarget(ctx context.Context, envID string) (*CollectTarget, error) {
	env, err := t.Projects.FindEnvironment(ctx, envID)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.collect_environment_lookup", "env", envID)
	}
	proj, err := t.Projects.Get(ctx, env.ProjectID)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.collect_environment_lookup", "env", envID)
	}
	subject := credentials.Grant{SubjectType: credentials.SubjectEnvironment, SubjectID: env.ID}
	dec, err := t.Auth.Authorize(ctx, credentials.Request{
		Reach: credentials.Reach{
			WorkspaceID:    proj.WorkspaceID,
			ProjectIDs:     []string{proj.ID},
			EnvironmentIDs: []string{env.ID},
		},
		Server:  "thulu-cloud",
		Tool:    "metrics_query",
		Subject: &subject,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.collect_authorize", "env", envID)
	}
	if !dec.Allowed || dec.CredentialID == "" {
		return nil, apperr.Precondition("envmetrics.errors.collect_credential_refused", "env", envID, "detail", dec.Detail)
	}
	cred, err := t.Creds.Get(ctx, proj.WorkspaceID, dec.CredentialID)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.collect_credential_load", "env", envID)
	}
	payload, err := t.Creds.Payload(ctx, proj.WorkspaceID, dec.CredentialID)
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.collect_credential_load", "env", envID)
	}
	return &CollectTarget{
		Environment: CollectEnvironment{
			ID: env.ID, ProjectID: env.ProjectID, Name: env.Name, Provider: env.Provider,
			Region: env.Region, Account: env.Account, Selectors: env.Selectors, IsProduction: env.IsProduction,
		},
		Credential: CollectCredential{
			Name: cred.Name, Provider: cred.Provider, Kind: cred.Kind, Payload: payload,
		},
	}, nil
}
