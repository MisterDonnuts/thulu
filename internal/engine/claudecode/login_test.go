package claudecode

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestParseAuthStatus(t *testing.T) {
	in := []byte("update available\n" + `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty",` +
		`"configDirectory":"/home/node/.claude-login","email":"ada@example.com","orgId":"o-1",` +
		`"orgName":"Ada's Org","subscriptionType":"max"}`)
	got, err := parseAuthStatus(in)
	if err != nil {
		t.Fatal(err)
	}
	want := engine.LoginStatus{LoggedIn: true, Email: "ada@example.com", Org: "Ada's Org", Method: "claude.ai"}
	if got != want {
		t.Errorf("status = %+v, want %+v", got, want)
	}

	got, err = parseAuthStatus([]byte(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`))
	if err != nil || got.LoggedIn || got.Email != "" {
		t.Errorf("signed out = %+v, %v", got, err)
	}
	if _, err := parseAuthStatus([]byte("command not found")); err == nil {
		t.Error("a status with no JSON must be an error, not a signed-out answer")
	}
}

func TestPersonalLoginClearsWhatOutranksTheLogin(t *testing.T) {
	caps := New(Config{}).Capabilities()
	pl := caps.PersonalLogin
	if pl == nil {
		t.Fatal("claude-code declares no personal login")
	}
	if pl.ConfigDirEnv != "CLAUDE_CONFIG_DIR" || pl.ConfigDir == "" {
		t.Errorf("config dir = %s=%q", pl.ConfigDirEnv, pl.ConfigDir)
	}
	clears := engine.PersonalLoginClears(caps)
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "LLM_API_KEY",
		engine.LLMGatewayEnv, engine.LLMProfileEnv,
	} {
		if !slices.Contains(clears, v) {
			t.Errorf("personal login does not clear %s", v)
		}
	}
	if slices.Contains(clears, pl.ConfigDirEnv) {
		t.Error("the clear list must not drop the config dir the login lives in")
	}
}
