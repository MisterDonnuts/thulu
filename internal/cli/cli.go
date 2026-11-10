package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

const defaultAddr = "http://localhost:8585"

func baseURL() string {
	if v := os.Getenv("OCTOPUS_URL"); v != "" {
		return v
	}
	return defaultAddr
}

func daemonRequest(method, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, url, http.NoBody)
	if err != nil {
		return nil, apperr.Wrap(err, "cli.errors.build_request")
	}
	req.Header.Set("Accept-Language", i18n.DaemonLocale())
	if key := strings.TrimSpace(os.Getenv("OCTOPUS_API_KEY")); key != "" {
		//i18n:ignore HTTP authorization scheme, not a message
		req.Header.Set("Authorization", "Bearer "+key)
		return req, nil
	}
	root, rootErr := resolveRoot("")
	if rootErr != nil {
		root = ""
	}
	auth.AttachDevSession(req, initwizard.InstallDevToken(root))
	return req, nil
}

func Status() error {
	req, err := daemonRequest(http.MethodGet, baseURL()+"/agents")
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return daemonUnreachable(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return daemonError(resp)
	}

	var agents []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Project  string `json:"project"`
		Instance string `json:"instance"`
		Status   string `json:"status"`
		Uptime   string `json:"uptime"`
		ExitCode *int   `json:"exit_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return err
	}

	if len(agents) == 0 {
		fmt.Println(i18n.Text("cli.cli.no_agents_running"))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, i18n.Text("cli.cli.status_header"))
	for _, a := range agents {
		status := a.Status
		if a.ExitCode != nil {
			status = fmt.Sprintf("%s(%d)", status, *a.ExitCode)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.Name, a.Type, a.Project, status, a.Uptime, a.ID)
	}
	return w.Flush()
}

func Logs(idOrName string, follow bool) error {
	resp, err := streamLogsRequest(idOrName, follow)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return dockerx.Demux(os.Stdout, resp.Body)
}

func streamLogsRequest(idOrName string, follow bool) (*http.Response, error) {
	//i18n:ignore URL path, not a message
	url := fmt.Sprintf("%s/agents/%s/logs?tail=100", baseURL(), idOrName)
	if follow {
		url += "&follow=true"
	}
	req, err := daemonRequest(http.MethodGet, url)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, daemonUnreachable(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, daemonError(resp)
	}
	return resp, nil
}

func daemonUnreachable(err error) error {
	addr := baseURL()
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return apperr.Unavailable("cli.errors.daemon_refused", "url", addr)
	case errors.As(err, &dnsErr):
		return apperr.Unavailable("cli.errors.daemon_unknown_host", "url", addr)
	case errors.As(err, &netErr) && netErr.Timeout():
		return apperr.Unavailable("cli.errors.daemon_timeout", "url", addr)
	}
	return apperr.WrapKind(apperr.KindUnavailable, err, "cli.errors.daemon_unreachable")
}

func daemonError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return apperr.New("cli.errors.request_failed", "status", resp.StatusCode)
	}
	return daemonErrorFrom(resp.StatusCode, body)
}

func daemonErrorFrom(status int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && strings.TrimSpace(e.Error) != "" {
		return apperr.New("cli.errors.daemon_error", "message", strings.TrimSpace(e.Error))
	}
	text := strings.TrimSpace(string(body))
	if text == "" || len(text) > 300 || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "{") {
		return apperr.New("cli.errors.request_failed", "status", status)
	}
	return apperr.New("cli.errors.daemon_error", "message", text)
}

func Stop(idOrName string) error {
	url := fmt.Sprintf("%s/agents/%s", baseURL(), idOrName)
	req, err := daemonRequest(http.MethodDelete, url)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return daemonUnreachable(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return daemonError(resp)
	}
	var result map[string]string
	if decErr := json.NewDecoder(resp.Body).Decode(&result); decErr != nil {
		return apperr.Wrap(decErr, "cli.errors.decode_response")
	}

	fmt.Println(i18n.Text("cli.cli.stopped_agent", "agent", result["agent"]))
	return nil
}
