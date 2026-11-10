package daemon

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// OCTOPUS_IMAGE comes from deploy/.env in Docker (digest-pinned by `thulu apply`) and from the
// Helm chart's Deployment.
func recordVersion(ctx context.Context, store *audit.Store) {
	if store == nil {
		return
	}
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	change, err := store.RecordVersion(ctx, buildinfo.Version, strings.TrimSpace(os.Getenv("OCTOPUS_IMAGE")), host)
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.version_audit_failed", "err", err))
		return
	}
	if change == nil {
		return
	}
	if change.From == "" {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.version_recorded", "version", buildinfo.Version))
		return
	}
	log.Printf("[daemon] %s", i18n.Log("daemon.log.version_changed", "version", buildinfo.Version))
}
