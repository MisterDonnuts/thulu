package daemon

import (
	"log"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// SaaS on Docker puts every tenant on one kernel and bridge network, so agents can reach each
// other's sandboxes and the daemon.
// OCTOPUS_ALLOW_DOCKER_SAAS=1 is for demo boxes only.
func checkDockerRuntimeSaaS(getenv func(string) string) error {
	if !envOn(getenv("OCTOPUS_SAAS_MODE")) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(getenv("OCTOPUS_AGENT_RUNTIME")), "kubernetes") {
		return nil
	}
	if envOn(getenv("OCTOPUS_ALLOW_DOCKER_SAAS")) {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.docker_saas_allowed"))
		return nil
	}
	return apperr.Forbidden("daemon.errors.docker_saas_refused")
}

func envOn(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true")
}
