package daemon

import (
	"context"
	"log"
	"os"

	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/updatecheck"
)

func startUpdateChecker(ctx context.Context, srv *api.Server) {
	c, err := updatecheck.FromEnv(os.Getenv, buildinfo.Version)
	if err != nil {
		log.Printf("[updates] %s", i18n.Log("daemon.log.update_checker_disabled", "err", err))
		return
	}
	srv.SetUpdateChecker(c)
	st := c.State()
	switch {
	case st.ConfigError != "":
		log.Printf("[updates] %s", i18n.Log("daemon.log.release_source_misconfigured", "error", st.ConfigError))
	case st.Off:
		log.Printf("[updates] %s", i18n.Log("daemon.log.release_lookups_off"))
	default:
		log.Printf("[updates] %s", i18n.Log("daemon.log.checking_releases", "source", st.Source, "interval", st.Interval, "channels", st.Channels))
		go c.Run(ctx)
	}
}
