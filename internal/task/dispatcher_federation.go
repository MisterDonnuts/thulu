package task

import (
	"context"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

type WorkloadIdentity interface {
	Ready() error
	Mint(ctx context.Context, c workloadid.RunClaims) (string, error)
}

func (d *Dispatcher) WithWorkloadIdentity(w WorkloadIdentity) *Dispatcher {
	d.workloadID = w
	return d
}

func (d *Dispatcher) routeResolved(
	ctx context.Context, env map[string]string, engineName, workspaceID string,
	resolved secrets.ResolvedLLM, taskRun, mint bool,
) error {
	if resolved.Federation.Set() {
		return d.applyFederationEnv(ctx, env, engineName, workspaceID, resolved, mint)
	}
	return d.routeProfileCredential(env, engineName, resolved, taskRun)
}

// The provider SDK gives any API key or auth token, even an empty one, precedence over federation,
// so every credential var is cleared.
func (d *Dispatcher) applyFederationEnv(
	ctx context.Context, env map[string]string, engineName, workspaceID string, resolved secrets.ResolvedLLM, mint bool,
) error {
	eng, err := engine.Get(engineName)
	if err != nil {
		return apperr.Wrap(err, "task.errors.llm_profile", "profile", resolved.ProfileName)
	}
	caps := eng.Capabilities()
	if _, ok := caps.FederatedKeyKind(); !ok || engine.IsCloudProvider(resolved.Provider) || resolved.BaseURL != "" {
		return apperr.Precondition("task.errors.llm_federation_unsupported",
			"profile", resolved.ProfileName, "engine", engineName)
	}
	if d.workloadID == nil {
		return apperr.Precondition("task.errors.llm_federation_no_issuer", "profile", resolved.ProfileName)
	}
	if notReady := d.workloadID.Ready(); notReady != nil {
		return apperr.Wrap(notReady, "task.errors.llm_profile", "profile", resolved.ProfileName)
	}
	clearProviderAuthEnv(env, caps)
	if !mint {
		return nil
	}
	token, err := d.workloadID.Mint(ctx, workloadid.RunClaims{
		WorkspaceID: workspaceID,
		TaskID:      env["TASK_ID"],
		ChatID:      env["CHAT_ID"],
		RunID:       env["INSTANCE_ID"],
		Audience:    caps.FederationAudience,
		TTL:         d.federationTokenTTL(env, workspaceID),
	})
	if err != nil {
		return apperr.Wrap(err, "task.errors.llm_profile", "profile", resolved.ProfileName)
	}
	for k, v := range caps.FederationEnv(resolved.Federation, token) {
		env[k] = v
	}
	return nil
}

const chatFederationTTL = time.Hour

func (d *Dispatcher) federationTokenTTL(env map[string]string, workspaceID string) time.Duration {
	if env["CHAT_ID"] != "" {
		return chatFederationTTL
	}
	if d.runDeadline == nil {
		return 0
	}
	return d.runDeadline(workspaceID)
}
