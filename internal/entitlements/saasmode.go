package entitlements

import (
	"os"
	"strings"
)

const SaaSModeEnv = "OCTOPUS_SAAS_MODE"

// In SaaS mode the "unlimited" zeros of agent.max_containers and agent.max_run_minutes become
// host-derived limits: on a shared host one tenant's unlimited is everyone else's outage.
func SaaSMode() bool {
	v := strings.TrimSpace(os.Getenv(SaaSModeEnv))
	return v == "1" || strings.EqualFold(v, "true")
}
