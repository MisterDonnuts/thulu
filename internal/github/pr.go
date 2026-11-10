package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type OpenPRReq struct {
	Owner string
	Repo  string
	Head  string
	Base  string
	Title string
	Body  string
	Draft bool
}

type OpenPRResp struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

func OpenPR(ctx context.Context, token string, req OpenPRReq) (*OpenPRResp, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	if req.Owner == "" || req.Repo == "" || req.Head == "" || req.Base == "" || req.Title == "" {
		return nil, apperr.Invalid("github.errors.open_pr_fields_required")
	}
	body, err := json.Marshal(map[string]any{
		"title": req.Title,
		"body":  req.Body,
		"head":  req.Head,
		"base":  req.Base,
		"draft": req.Draft,
	})
	if err != nil {
		return nil, err
	}
	endpoint := reposURL(req.Owner, req.Repo) + "/pulls"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "token "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", buildinfo.UserAgent())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, apperr.Wrap(err, "github.errors.read_pulls_response")
	}

	if resp.StatusCode == http.StatusCreated {
		var out OpenPRResp
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, apperr.Wrap(err, "github.errors.decode_pulls_response")
		}
		return &out, nil
	}
	return nil, parsePRError(resp.StatusCode, respBody)
}

// GitHub answers 422 both when the PR exists and when head has no commits beyond base; both map to
// ErrAlreadyExists.
func parsePRError(status int, body []byte) error {
	var apiErr struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	logx.Warn(json.Unmarshal(body, &apiErr), "github.log.parse_pulls_error_failed", "status", status)
	combined := apiErr.Message
	for _, e := range apiErr.Errors {
		if e.Message != "" {
			combined += "; " + e.Message
		}
	}
	if status == http.StatusUnprocessableEntity &&
		//i18n:ignore GitHub's own error wording, matched
		(strings.Contains(combined, "already exists") ||
			//i18n:ignore GitHub's own error wording, matched
			strings.Contains(combined, "No commits between")) {
		return ErrAlreadyExists
	}
	if combined == "" {
		combined = fmt.Sprintf("HTTP %d", status)
	}
	return apperr.New("github.errors.pulls_api", "message", combined)
}

var ErrAlreadyExists error = apperr.Conflict("github.errors.pr_already_exists")

// GitHub's 422 never says which PR exists. The head filter wants owner:branch; only same-repo
// heads, since
// Thulu pushes task branches to the project's own remote.
func FindPRByHead(ctx context.Context, token, owner, repo, head string) (*OpenPRResp, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	if owner == "" || repo == "" || head == "" {
		return nil, apperr.Invalid("github.errors.find_pr_fields_required")
	}
	endpoint := reposURL(owner, repo) + "/pulls?state=open&per_page=1&head=" + //i18n:ignore API path and query
		url.QueryEscape(owner+":"+head)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "token "+token)
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("User-Agent", buildinfo.UserAgent())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, apperr.Wrap(err, "github.errors.read_pulls_list")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parsePRError(resp.StatusCode, body)
	}
	var out []OpenPRResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, apperr.Wrap(err, "github.errors.decode_pulls_list")
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// Owner and repo come from a project's repo_url; path-escape so a '/' or '?' cannot retarget the
// API path.
func reposURL(owner, repo string) string {
	return githubcfg.APIURL() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func ParseRemote(remote string) (owner, repo string, ok bool) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, ".git")
	if remote == "" {
		return "", "", false
	}

	if !strings.HasPrefix(remote, "http://") && !strings.HasPrefix(remote, "https://") {
		if at := strings.Index(remote, "@"); at >= 0 {
			rest := remote[at+1:]
			if colon := strings.Index(rest, ":"); colon > 0 {
				path := rest[colon+1:]
				return splitOwnerRepo(path)
			}
		}
	}
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	return splitOwnerRepo(strings.Trim(u.Path, "/"))
}

// Strict on purpose: only an exact PR URL on the configured GitHub host is trusted.
func ParsePRURL(raw string) (owner, repo string, number int, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !githubcfg.IsGitHubHost(u.Host) {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}

func splitOwnerRepo(path string) (string, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
