package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
)

type Event struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	Result     Result    `json:"result"`
	Mode       string    `json:"mode"`
	Operator   string    `json:"operator"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Detail     string    `json:"detail"`
}

func attachAuth(req *http.Request, devToken, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey) //i18n:ignore HTTP auth scheme
		return
	}
	auth.AttachDevSession(req, devToken)
}

func (u *updateRun) reportEvent(ctx context.Context, ev Event) {
	if u.in.DaemonURL == "" {
		return
	}
	if u.in.APIKey == "" && u.in.DevToken == "" {
		u.say("updater.cli.event_no_key")
		return
	}
	if err := postEvent(ctx, u.deps.http(), u.in.DaemonURL, u.in.DevToken, u.in.APIKey, ev); err != nil {
		u.say("updater.cli.event_not_sent", "err", apperr.Text(err), "file", StateFile)
		if strings.Contains(err.Error(), "403") {
			u.say("updater.cli.event_key_needs_admin")
		}
		return
	}
	u.say("updater.cli.event_recorded")
}

func postEvent(ctx context.Context, client *http.Client, baseURL, devToken, apiKey string, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/updates/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	attachAuth(req, devToken, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return apperr.WrapKind(apperr.KindUnavailable, err, "updater.errors.daemon_unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); err != nil {
		return apperr.Wrap(err, "updater.errors.read_daemon_answer")
	}
	if resp.StatusCode/100 != 2 {
		return apperr.Unavailable("updater.errors.daemon_answered", "status", resp.Status)
	}
	return nil
}
