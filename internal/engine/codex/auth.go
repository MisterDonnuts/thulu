package codex

import "github.com/MisterDonnuts/thulu/internal/engine"

var authKeys = []string{"OPENAI_API_KEY", "CODEX_API_KEY"}

var codexAliases = map[string]string{"CODEX_API_KEY": "OPENAI_API_KEY"}

func readAuthFromEnvFile(envFile string) []string {
	return engine.AuthFromEnvFile(envFile, authKeys, codexAliases)
}
