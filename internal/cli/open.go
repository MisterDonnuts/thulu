package cli

import (
	"context"
	"fmt"
	neturl "net/url"
	"os/exec"
	"runtime"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

func Open(root string) error {
	url := baseURL()
	resolved, err := resolveRoot(root)
	if err != nil && root != "" {
		return err
	}
	if tok := initwizard.InstallDevToken(resolved); tok != "" {
		url = auth.DevDashboardURL(url, tok)
	}
	fmt.Println(url)
	if err := openBrowser(url); err != nil {
		fmt.Println(i18n.Text("cli.cli.browser_open_failed"))
	}
	return nil
}

// Only http(s) reaches the opener: anything else could be read as an option or open something other
// than a page.
func openBrowser(url string) error {
	if u, err := neturl.Parse(url); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return apperr.Invalid("cli.errors.not_dashboard_url", "url", url)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		//nolint:gosec // G204: fixed opener binary; url was checked to be http(s) above
		cmd = exec.CommandContext(context.Background(), "open", url)
	case "linux":
		//nolint:gosec // G204: fixed opener binary; url was checked to be http(s) above
		cmd = exec.CommandContext(context.Background(), "xdg-open", url)
	case "windows":
		//nolint:gosec // G204: fixed opener binary; url was checked to be http(s) above. i18n:ignore Windows opener argument, not a message
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return apperr.NotImplemented("cli.errors.no_browser_opener", "os", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return apperr.Wrap(err, "cli.errors.open_browser")
	}
	return nil
}
