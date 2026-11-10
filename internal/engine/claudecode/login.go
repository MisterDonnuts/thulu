package claudecode

import (
	"bytes"
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
)

// Kept apart from ~/.claude so the adapter can clear a run's leftovers from the login volume
// without touching ~/.claude.
const LoginConfigDir = "/home/node/.claude-login"

var personalLogin = &engine.PersonalLogin{
	ConfigDirEnv: "CLAUDE_CONFIG_DIR",
	ConfigDir:    LoginConfigDir,
	Login:        []string{"claude", "auth", "login", "--claudeai"},
	Status:       []string{"claude", "auth", "status", "--json"},
	Logout:       []string{"claude", "auth", "logout"},
	ParseStatus:  parseAuthStatus,
	// Any of these makes Claude Code bypass the stored login: env credentials outrank it and the
	// cloud/endpoint switches route calls elsewhere.
	ClearEnv: []string{
		"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH", "CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", VertexKeyFileEnv,
	},
}

type authStatus struct {
	LoggedIn   bool   `json:"loggedIn"`
	AuthMethod string `json:"authMethod"`
	Email      string `json:"email"`
	OrgName    string `json:"orgName"`
}

// The CLI may print lines (an update notice) before the JSON, so decode from the first '{'.
func parseAuthStatus(out []byte) (engine.LoginStatus, error) {
	i := bytes.IndexByte(out, '{')
	if i < 0 {
		return engine.LoginStatus{}, apperr.Invalid("engine.errors.login_status_unreadable")
	}
	var st authStatus
	if err := json.NewDecoder(bytes.NewReader(out[i:])).Decode(&st); err != nil {
		return engine.LoginStatus{}, apperr.Wrap(err, "engine.errors.login_status_unreadable")
	}
	return engine.LoginStatus{LoggedIn: st.LoggedIn, Email: st.Email, Org: st.OrgName, Method: st.AuthMethod}, nil
}
