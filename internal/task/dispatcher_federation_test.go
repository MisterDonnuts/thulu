package task

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

type fakeIssuer struct {
	notReady error
	minted   []workloadid.RunClaims
}

func (f *fakeIssuer) Ready() error { return f.notReady }

func (f *fakeIssuer) Mint(_ context.Context, c workloadid.RunClaims) (string, error) {
	f.minted = append(f.minted, c)
	return "signed.jwt.token", nil
}

var federated = secrets.ResolvedLLM{
	Source: "binding", Engine: "claude-code", Provider: "anthropic", ProfileName: "wif",
	Federation: engine.FederationConfig{
		RuleID: "fdrl_1", OrganizationID: "123e4567-e89b-12d3-a456-426614174000", ServiceAccountID: "svac_1",
	},
}

func TestApplyLLMAuth_Federation(t *testing.T) {
	iss := &fakeIssuer{}
	d := (&Dispatcher{llmResolver: resolverReturning(federated, nil)}).
		WithWorkloadIdentity(iss).
		WithRunDeadline(func(ws string) time.Duration {
			if ws == "ws-1" {
				return 90 * time.Minute
			}
			return time.Hour
		})
	env := map[string]string{
		"ENGINE": "claude-code", "TASK_ID": "t1", "INSTANCE_ID": "dev-t1-1",
		"ANTHROPIC_API_KEY": "sk-ant-api03-host", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-host",
		"ANTHROPIC_AUTH_TOKEN": "x", "ANTHROPIC_BASE_URL": "http://gw",
	}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", &Task{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if _, ok := env[v]; ok {
			t.Errorf("%s survived into a federation run", v)
		}
	}
	want := map[string]string{
		claudecode.IdentityJWTEnv:      "signed.jwt.token",
		"ANTHROPIC_FEDERATION_RULE_ID": "fdrl_1",
		"ANTHROPIC_ORGANIZATION_ID":    "123e4567-e89b-12d3-a456-426614174000",
		"ANTHROPIC_SERVICE_ACCOUNT_ID": "svac_1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(iss.minted) != 1 {
		t.Fatalf("minted %d tokens, want 1", len(iss.minted))
	}
	c := iss.minted[0]
	if c.WorkspaceID != "ws-1" || c.TaskID != "t1" || c.RunID != "dev-t1-1" ||
		c.Audience != claudecode.FederationAudience || c.TTL != 90*time.Minute {
		t.Errorf("claims = %+v", c)
	}

	if err := d.LLMCredentialReady(t.Context(), "ws-1", claudeAgent, "dev"); err != nil || len(iss.minted) != 1 {
		t.Errorf("LLMCredentialReady = %v, minted %d", err, len(iss.minted))
	}

	chatEnv := map[string]string{"ENGINE": "claude-code", "CHAT_ID": "chat-1", "INSTANCE_ID": "chat-1"}
	if err := d.ApplyLLMAuth(t.Context(), chatEnv, "ws-1", claudeAgent, "dev"); err != nil {
		t.Fatal(err)
	}
	if c := iss.minted[len(iss.minted)-1]; c.ChatID != "chat-1" || c.TaskID != "" || c.TTL != chatFederationTTL {
		t.Errorf("chat claims = %+v", c)
	}
}

func TestApplyLLMAuth_FederationNeedsIssuer(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(federated, nil)}
	err := d.applyLLMAuth(t.Context(), map[string]string{"ENGINE": "claude-code"}, "ws-1", claudeAgent, "dev", nil)
	if err == nil || !strings.Contains(err.Error(), "wif") {
		t.Fatalf("err = %v, want a refusal naming the profile", err)
	}
	d.WithWorkloadIdentity(&fakeIssuer{notReady: apperr.Precondition("workloadid.errors.not_configured", "env", workloadid.IssuerEnv)})
	err = d.applyLLMAuth(t.Context(), map[string]string{"ENGINE": "claude-code"}, "ws-1", claudeAgent, "dev", nil)
	if err == nil || !strings.Contains(err.Error(), workloadid.IssuerEnv) {
		t.Fatalf("err = %v, want one naming %s", err, workloadid.IssuerEnv)
	}
}
