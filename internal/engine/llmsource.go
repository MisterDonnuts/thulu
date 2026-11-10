package engine

// Launchers re-read the host credential on every launch and chat turn; overlaying it after a
// profile set a key
// put the host's OAuth token back into the run, and the Claude CLI preferred it.
const LLMSourceEnv = "OCTOPUS_LLM_SOURCE"

const LLMProfileEnv = "OCTOPUS_LLM_PROFILE"

func HostAuthApplies(env map[string]string) bool {
	src := env[LLMSourceEnv]
	return src == "" || src == "host"
}

const LLMGatewayEnv = "OCTOPUS_LLM_GATEWAY"

func ViaGateway(env map[string]string) bool {
	return env[LLMGatewayEnv] == "1"
}
