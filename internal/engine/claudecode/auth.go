package claudecode

import "github.com/MisterDonnuts/thulu/internal/engine"

var authKeys = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

func readAuthFromEnvFile(envFile string) []string {
	return engine.AuthFromEnvFile(envFile, authKeys, nil)
}
