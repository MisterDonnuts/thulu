package agentscripts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fakeClaudeEnv = `#!/bin/bash
printf '%s\n' "$@" >"$FAKE_LOG/args"
for v in ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_WORKSPACE_ID \
  ANTHROPIC_FEDERATION_RULE_ID ANTHROPIC_IDENTITY_TOKEN_FILE OCTOPUS_ANTHROPIC_IDENTITY_JWT; do
  if [ -n "${!v+x}" ]; then echo "$v=${!v}"; else echo "$v unset"; fi
done >"$FAKE_LOG/env"
[ -n "${ANTHROPIC_IDENTITY_TOKEN_FILE:-}" ] && cat "$ANTHROPIC_IDENTITY_TOKEN_FILE" >"$FAKE_LOG/token"
cat >/dev/null
echo '{"type":"system","subtype":"init","session_id":"S"}'
echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1}'
`

func TestClaudeAdapter_FederationEnv(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaudeEnv)
	r.run(t, "claude-code.sh", "engine_auth_check && engine_run", "hello",
		"OCTOPUS_ANTHROPIC_IDENTITY_JWT=hdr.claims.sig",
		"ANTHROPIC_FEDERATION_RULE_ID=fdrl_1",
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_WORKSPACE_ID=",
	)
	env, err := os.ReadFile(filepath.Join(r.log, "env"))
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(r.home, ".config", "octopus", "anthropic-identity.jwt")
	for _, want := range []string{
		"ANTHROPIC_API_KEY unset", "ANTHROPIC_AUTH_TOKEN unset", "CLAUDE_CODE_OAUTH_TOKEN unset",
		"ANTHROPIC_WORKSPACE_ID unset", "OCTOPUS_ANTHROPIC_IDENTITY_JWT unset",
		"ANTHROPIC_FEDERATION_RULE_ID=fdrl_1", "ANTHROPIC_IDENTITY_TOKEN_FILE=" + tokenFile,
	} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("CLI env lacks %q:\n%s", want, env)
		}
	}
	if tok, _ := os.ReadFile(filepath.Join(r.log, "token")); string(tok) != "hdr.claims.sig" {
		t.Errorf("token file = %q", tok)
	}
	if fi, err := os.Stat(tokenFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	args, _ := os.ReadFile(filepath.Join(r.log, "args"))
	if !slices.Contains(strings.Split(string(args), "\n"), "--strict-mcp-config") {
		t.Errorf("claude args lack --strict-mcp-config:\n%s", args)
	}
}

func TestClaudeAdapter_KeyEnvKept(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaudeEnv)
	r.run(t, "claude-code.sh", "engine_run", "hello", "ANTHROPIC_API_KEY=sk-ant-api03-x", "CLAUDE_CODE_OAUTH_TOKEN=")
	env, err := os.ReadFile(filepath.Join(r.log, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "ANTHROPIC_API_KEY=sk-ant-api03-x\n") ||
		!strings.Contains(string(env), "ANTHROPIC_IDENTITY_TOKEN_FILE unset\n") {
		t.Errorf("CLI env:\n%s", env)
	}
}
