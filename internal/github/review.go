package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type MergeReq struct {
	Owner         string
	Repo          string
	Number        int
	Method        string
	CommitTitle   string
	CommitMessage string
	// SHA makes GitHub refuse the merge if the head moved since the person confirmed.
	SHA string
}

type MergeResp struct {
	Merged  bool   `json:"merged"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

var MergeMethods = map[string]bool{"": true, "merge": true, "squash": true, "rebase": true}

func MergePR(ctx context.Context, token string, req *MergeReq) (*MergeResp, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	if req.Owner == "" || req.Repo == "" || req.Number <= 0 || !MergeMethods[req.Method] {
		return nil, apperr.Invalid("github.errors.merge_fields_required")
	}
	payload := map[string]any{}
	for k, v := range map[string]string{
		"merge_method": req.Method, "commit_title": req.CommitTitle,
		"commit_message": req.CommitMessage, "sha": req.SHA,
	} {
		if v != "" {
			payload[k] = v
		}
	}
	endpoint := reposURL(req.Owner, req.Repo) + "/pulls/" + strconv.Itoa(req.Number) + "/merge"
	var out MergeResp
	if err := call(ctx, token, http.MethodPut, endpoint, payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type PendingDeployment struct {
	Environment struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"environment"`
	CurrentUserCanApprove bool `json:"current_user_can_approve"`
}

func PendingDeployments(ctx context.Context, token, owner, repo string, runID int64) ([]PendingDeployment, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	var out []PendingDeployment
	endpoint := reposURL(owner, repo) + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/pending_deployments"
	if err := call(ctx, token, http.MethodGet, endpoint, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type DeploymentReview struct {
	Owner          string
	Repo           string
	RunID          int64
	EnvironmentIDs []int64
	State          string
	Comment        string
}

func ReviewPendingDeployments(ctx context.Context, token string, req *DeploymentReview) ([]int64, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	if req.Owner == "" || req.Repo == "" || req.RunID <= 0 || len(req.EnvironmentIDs) == 0 ||
		(req.State != "approved" && req.State != "rejected") {
		return nil, apperr.Invalid("github.errors.review_fields_required")
	}
	endpoint := reposURL(req.Owner, req.Repo) + "/actions/runs/" + strconv.FormatInt(req.RunID, 10) + "/pending_deployments"
	var out []struct {
		ID int64 `json:"id"`
	}
	payload := map[string]any{"environment_ids": req.EnvironmentIDs, "state": req.State, "comment": req.Comment}
	if err := call(ctx, token, http.MethodPost, endpoint, payload, &out); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(out))
	for _, d := range out {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

func call(ctx context.Context, token, method, endpoint string, payload, out any) error {
	return callLimit(ctx, token, method, endpoint, payload, out, 256*1024)
}

func callLimit(ctx context.Context, token, method, endpoint string, payload, out any, limit int64) error {
	var body io.Reader = http.NoBody
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return apperr.Wrap(err, "github.errors.read_response")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return apperr.Wrap(err, "github.errors.decode_response")
	}
	return nil
}

// GitHub answers 405 for not mergeable and 409 for a moved head; both are conflicts.
func apiError(status int, body []byte) error {
	var apiErr struct {
		Message string `json:"message"`
	}
	logx.Warn(json.Unmarshal(body, &apiErr), "github.log.parse_error_failed", "status", status)
	msg := apiErr.Message
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	e := apperr.New("github.errors.api", "status", status, "message", msg)
	switch status {
	case http.StatusMethodNotAllowed, http.StatusConflict:
		return e.WithKind(apperr.KindConflict)
	case http.StatusNotFound:
		return e.WithKind(apperr.KindNotFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return e.WithKind(apperr.KindForbidden)
	case http.StatusUnprocessableEntity:
		return e.WithKind(apperr.KindUnprocessable)
	}
	return e
}
