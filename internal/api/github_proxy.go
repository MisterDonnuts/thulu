package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/tools"
)

// The GitHub proxy serves run keys on projects whose token is a stored credential (a PAT): the
// token endpoints refuse them the PAT (refuseStoredGithubToken) and thulu-github sends its calls
// here instead. The daemon forwards a call only when it is one thulu-github makes
// (tools/thulu-github/github-proxy.json), the run's stage grants a tool that makes it, the
// approval that tool needs is approved, and the repository belongs to a project the run reaches.
// The PAT never leaves the daemon.
const githubProxyPrefix = "/internal/github-proxy"

const (
	githubProxyMaxRequest  = 1 << 20
	githubProxyMaxResponse = 16 << 20
	// Under the daemon's request timeout (middleware.DefaultRequestTimeout).
	githubProxyTimeout = 40 * time.Second
)

// thulu-github names the repository a GraphQL document acts on, because a node id names none.
const githubProxyRepoHeader = "X-Thulu-Repo"

// pr_merge and deployment_approve wait in a delegated chat for the person's confirmation
// (github_held.go); the proxy never runs them there.
var githubHeldInChat = map[string]bool{"pr_merge": true, "deployment_approve": true}

type githubProxyRoute struct {
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Tools    []string `json:"tools"`
	Download bool     `json:"download"`
	segments []string
}

type githubProxyDoc struct {
	Tools         []string `json:"tools"`
	Variables     []string `json:"variables"`
	RepoVariables *struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repo_variables"`
	NodeVariable string `json:"node_variable"`
	Query        string `json:"query"`
}

type githubProxyTable struct {
	REST    []githubProxyRoute        `json:"rest"`
	GraphQL map[string]githubProxyDoc `json:"graphql"`
	byQuery map[string]string
}

var githubProxy = mustGithubProxyTable(tools.GithubProxyManifest())

func mustGithubProxyTable(raw []byte) *githubProxyTable {
	t, err := parseGithubProxyTable(raw)
	if err != nil {
		panic(err)
	}
	return t
}

var githubProxyMethods = []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}

const githubProxyRepoPrefix = "/repos/{owner}/{repo}/" //i18n:ignore REST route template

// A route acts under githubProxyRepoPrefix with a supported method and names its tools; a GraphQL
// document names its tools and exactly one of node_variable and repo_variables, each among its
// variables.
func parseGithubProxyTable(raw []byte) (*githubProxyTable, error) {
	var t githubProxyTable
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, apperr.Wrap(err, "api.github.errors.proxy_manifest_unreadable")
	}
	for i := range t.REST {
		r := &t.REST[i]
		if !slices.Contains(githubProxyMethods, r.Method) || !strings.HasPrefix(r.Path, githubProxyRepoPrefix) || len(r.Tools) == 0 {
			return nil, apperr.New("api.github.errors.proxy_manifest_entry", "entry", r.Method+" "+r.Path)
		}
		r.segments = strings.Split(strings.TrimPrefix(r.Path, "/"), "/")
	}
	t.byQuery = map[string]string{}
	for name, d := range t.GraphQL {
		ok := len(d.Tools) > 0 && (d.NodeVariable == "") != (d.RepoVariables == nil)
		if v := d.NodeVariable; v != "" && !slices.Contains(d.Variables, v) {
			ok = false
		}
		if rv := d.RepoVariables; rv != nil && (!slices.Contains(d.Variables, rv.Owner) || !slices.Contains(d.Variables, rv.Name)) {
			ok = false
		}
		if !ok {
			return nil, apperr.New("api.github.errors.proxy_manifest_entry", "entry", name)
		}
		t.byQuery[normalizeGraphQL(d.Query)] = name
	}
	return &t, nil
}

func normalizeGraphQL(q string) string { return strings.Join(strings.Fields(q), " ") }

func (t *githubProxyTable) match(method string, segs []string) (*githubProxyRoute, map[string]string) {
	for i := range t.REST {
		r := &t.REST[i]
		if r.Method != method || len(r.segments) != len(segs) {
			continue
		}
		if params, ok := matchGithubRoute(r.segments, segs); ok {
			return r, params
		}
	}
	return nil, nil
}

func matchGithubRoute(tmpl, segs []string) (map[string]string, bool) {
	params := map[string]string{}
	for i, part := range tmpl {
		name, isParam := strings.CutPrefix(part, "{")
		if !isParam {
			if segs[i] != part {
				return nil, false
			}
			continue
		}
		name = strings.TrimSuffix(name, "}")
		value, err := url.PathUnescape(segs[i])
		if err != nil || !validGithubParam(name, value) {
			return nil, false
		}
		params[name] = value
	}
	return params, true
}

// A route parameter must not reach another GitHub route: ids are digits (so "pulls/comments" never
// matches "pulls/{pull_number}"), owner and repo hold no slash, and a ref or a file path holds no
// dot segment.
func validGithubParam(name, value string) bool {
	if value == "" || len(value) > 1024 || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	switch {
	case name == "owner" || name == "repo":
		return !strings.Contains(value, "/") && value != "." && value != ".."
	case name == "workflow_id":
		return !strings.Contains(value, "/") && value != "." && value != ".."
	case strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_number"):
		return strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Server) handleGithubProxy(w http.ResponseWriter, r *http.Request) {
	scope := auth.APIKeyRunScope(r.Context())
	if !auth.IsAPIKeyAuthed(r.Context()) || !scope.IsRun() {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_run_key_only")
		return
	}
	if s.gh.tokens == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.no_credential_source")
		return
	}
	if s.projectStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.project_store_unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), githubProxyPrefix)
	if path == "/graphql" && r.Method == http.MethodPost {
		s.githubProxyGraphQL(w, r, scope)
		return
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	route, params := githubProxy.match(r.Method, segs)
	if route == nil {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_route_unknown", "method", r.Method, "path", path)
		return
	}
	proj, tool, ok := s.githubProxyGate(w, r, scope, params["owner"]+"/"+params["repo"], route.Tools)
	if !ok {
		return
	}
	body, ok := readGithubProxyBody(w, r)
	if !ok {
		return
	}
	token, ok := s.githubProxyToken(w, r, scope, proj.ID, tool)
	if !ok {
		return
	}
	upstream := githubcfg.APIURL() + "/" + strings.Join(segs, "/")
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	s.forwardGithub(w, r, githubForward{
		projectID: proj.ID, token: token, method: r.Method, url: upstream, body: body,
		download: route.Download, linkPath: "/" + strings.Join(segs, "/"),
	})
}

type githubProxyGraphQLBody struct {
	Query     string                     `json:"query"`
	Variables map[string]json.RawMessage `json:"variables"`
}

func (s *Server) githubProxyGraphQL(w http.ResponseWriter, r *http.Request, scope auth.RunScope) {
	body, ok := readGithubProxyBody(w, r)
	if !ok {
		return
	}
	var req githubProxyGraphQLBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.server.errors.invalid_body")
		return
	}
	name, known := githubProxy.byQuery[normalizeGraphQL(req.Query)]
	if !known {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_document_unknown")
		return
	}
	doc := githubProxy.GraphQL[name]
	vars := map[string]string{}
	for k, raw := range req.Variables {
		if !slices.Contains(doc.Variables, k) {
			writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_variable_refused", "name", k, "document", name)
			return
		}
		if !scalarJSON(raw) {
			writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_variable_not_scalar", "name", k, "document", name)
			return
		}
		var v string
		if json.Unmarshal(raw, &v) == nil {
			vars[k] = v
		}
	}
	repo, ok := githubProxyDocRepo(w, r, doc, vars)
	if !ok {
		return
	}
	proj, tool, ok := s.githubProxyGate(w, r, scope, repo, doc.Tools)
	if !ok {
		return
	}
	token, ok := s.githubProxyToken(w, r, scope, proj.ID, tool)
	if !ok {
		return
	}
	if doc.NodeVariable != "" {
		want, _ := repoRef(repo)
		got, err := githubNodeRepository(r.Context(), token, vars[doc.NodeVariable])
		if err != nil {
			logx.Warn(err, "api.github.log.proxy_upstream_failed", "project", proj.ID, "path", "/graphql")
			writeErrKey(w, r, http.StatusBadGateway, "api.github.errors.proxy_upstream_failed")
			return
		}
		if got == "" || !strings.EqualFold(got, want) {
			writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_node_other_repo",
				"id", vars[doc.NodeVariable], "repo", want)
			return
		}
	}
	checked, err := json.Marshal(githubProxyGraphQLBody{Query: doc.Query, Variables: req.Variables})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.forwardGithub(w, r, githubForward{
		projectID: proj.ID, token: token, method: http.MethodPost, url: githubcfg.GraphQLURL(), body: checked,
	})
}

// Every listed document takes scalar variables. An object could repeat a key, and the copy
// encoding/json reads need not be the one GitHub reads.
func scalarJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '['
}

// The repository comes from the document's own variables when it has them; a document acting on a
// node id takes it from X-Thulu-Repo and the daemon checks the node against it.
func githubProxyDocRepo(w http.ResponseWriter, r *http.Request, doc githubProxyDoc, vars map[string]string) (string, bool) {
	header := strings.TrimSpace(r.Header.Get(githubProxyRepoHeader))
	if rv := doc.RepoVariables; rv != nil {
		repo := vars[rv.Owner] + "/" + vars[rv.Name]
		if header != "" {
			a, _ := repoRef(header)
			b, _ := repoRef(repo)
			if a != b {
				writeErrKey(w, r, http.StatusForbidden, "api.github.errors.proxy_repo_mismatch", "header", header, "repo", repo)
				return "", false
			}
		}
		return repo, true
	}
	if header == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.proxy_repo_required", "header", githubProxyRepoHeader)
		return "", false
	}
	return header, true
}

// githubProxyGate finds the run's project for repo and the first of the call's tools that the run
// may use: granted in its stage and, outside a delegated chat, with its approval approved.
func (s *Server) githubProxyGate(
	w http.ResponseWriter, r *http.Request, scope auth.RunScope, repo string, callTools []string,
) (*project.Project, string, bool) {
	proj, err := s.githubProxyProject(r, repo)
	if err != nil {
		writeError(w, r, 0, err)
		return nil, "", false
	}
	tool, err := s.githubProxyTool(r, scope, callTools)
	if err != nil {
		writeError(w, r, 0, err)
		return nil, "", false
	}
	return proj, tool, true
}

func (s *Server) githubProxyProject(r *http.Request, raw string) (*project.Project, error) {
	ref, ok := repoRef(raw)
	if !ok {
		return nil, apperr.Invalid("api.github.errors.repo_invalid", "repo", raw)
	}
	candidates, err := s.projectsForRepo(r, ref)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, apperr.NotFound("api.github.errors.repo_not_in_workspace", "repo", ref)
	}
	for i := range candidates {
		if s.runReachesGithubProject(r, candidates[i].ID) {
			return &candidates[i], nil
		}
	}
	return nil, apperr.Forbidden("api.github.errors.proxy_repo_not_in_task", "repo", ref)
}

func (s *Server) githubProxyTool(r *http.Request, scope auth.RunScope, callTools []string) (string, error) {
	var t *task.Task
	var entries []string
	if scope.Delegated() {
		entries = s.chatStageTools(r.Context(), scope)
	} else if t = s.runTask(r); t != nil {
		entries = s.credentialStageTools(scope, t)
	}
	var refused error
	for _, tool := range callTools {
		if !githubToolGranted(entries, tool) {
			continue
		}
		if scope.Delegated() {
			if githubHeldInChat[tool] {
				refused = apperr.Forbidden("api.github.errors.proxy_held_in_chat", "tool", tool)
				continue
			}
			return tool, nil
		}
		if err := s.requireGithubToolApproval(r.Context(), t, tool); err != nil {
			refused = err
			continue
		}
		return tool, nil
	}
	if refused != nil {
		return "", refused
	}
	return "", apperr.Forbidden("api.github.errors.proxy_tool_not_granted",
		"tools", strings.Join(callTools, ", "), "server", githubMCPServer)
}

// The token carries what the tool needs, as the token endpoints would mint it: on an App project
// the proxy works the same, narrowed.
func githubToolPermissions(scope auth.RunScope, tool string) githubapp.Permissions {
	perms := githubapp.PermsRunTools
	if scope.Delegated() {
		perms = githubapp.PermsDelegatedChat
	}
	for name, tools := range githubPermissionTools {
		if slices.Contains(tools, tool) {
			perms = perms.With(tokenPermissionExtras[name])
		}
	}
	return perms
}

func (s *Server) githubProxyToken(w http.ResponseWriter, r *http.Request, scope auth.RunScope, projectID, tool string) (string, bool) {
	rec, err := s.githubTokenForProjectWith(r.Context(), projectID, githubToolPermissions(scope, tool))
	if errors.Is(err, githubapp.ErrNoCredential) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.no_token")
		return "", false
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return "", false
	}
	return rec.Token, true
}

func readGithubProxyBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, githubProxyMaxRequest))
	if err != nil {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.github.errors.proxy_body_too_large", "limit", githubProxyMaxRequest)
		return nil, false
	}
	return body, true
}

// Following no redirect keeps the token on GitHub's API host. A log download is the exception:
// GitHub answers it with a signed URL on its storage host, which the proxy fetches with no token.
var githubProxyClient = &http.Client{
	Timeout: githubProxyTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Only what a GitHub client needs crosses in either direction: the caller's Authorization,
// X-API-Key and cookies never reach GitHub, and GitHub's cookies never reach the caller.
var (
	githubProxyRequestHeaders  = []string{"Accept", "Content-Type", "X-GitHub-Api-Version"} //i18n:ignore HTTP header names
	githubProxyResponseHeaders = []string{
		"Content-Type", "ETag", "Last-Modified", "Retry-After", "X-GitHub-Media-Type", "X-GitHub-Request-Id",
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "X-RateLimit-Resource", "X-RateLimit-Used",
	}
)

type githubForward struct {
	projectID string
	token     string
	method    string
	url       string
	body      []byte
	download  bool
	// The path Link headers are rewritten onto, so pagination comes back through the proxy.
	linkPath string
}

func (s *Server) forwardGithub(w http.ResponseWriter, r *http.Request, f githubForward) {
	var body io.Reader = http.NoBody
	if len(f.body) > 0 {
		body = bytes.NewReader(f.body)
	}
	req, err := http.NewRequestWithContext(r.Context(), f.method, f.url, body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	for _, h := range githubProxyRequestHeaders {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("Authorization", "token "+f.token)
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := githubProxyClient.Do(req)
	if err != nil {
		s.githubProxyFailed(w, r, f, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		next, ferr := followGithubDownload(r.Context(), f, resp)
		if ferr != nil {
			s.githubProxyFailed(w, r, f, ferr)
			return
		}
		if next == nil {
			writeErrKey(w, r, http.StatusBadGateway, "api.github.errors.proxy_redirect_refused", "status", resp.StatusCode)
			return
		}
		defer next.Body.Close()
		resp = next
	}
	s.writeGithubResponse(w, r, f, resp)
}

func (s *Server) writeGithubResponse(w http.ResponseWriter, r *http.Request, f githubForward, resp *http.Response) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, githubProxyMaxResponse+1))
	if err != nil {
		s.githubProxyFailed(w, r, f, err)
		return
	}
	if len(raw) > githubProxyMaxResponse {
		if !f.download {
			writeErrKey(w, r, http.StatusBadGateway, "api.github.errors.proxy_response_too_large", "limit", githubProxyMaxResponse)
			return
		}
		raw = raw[:githubProxyMaxResponse]
	}
	for _, h := range githubProxyResponseHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if link := resp.Header.Get("Link"); link != "" && f.linkPath != "" {
		w.Header().Set("Link", rewriteGithubLink(link, f.linkPath))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

// followGithubDownload returns nil when the redirect is not one to follow.
func followGithubDownload(ctx context.Context, f githubForward, resp *http.Response) (*http.Response, error) {
	loc := githubDownloadLocation(f, resp)
	if loc == nil {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	next, err := githubProxyClient.Do(req)
	if err != nil {
		return nil, err
	}
	if next.StatusCode >= 300 && next.StatusCode < 400 {
		next.Body.Close()
		return nil, nil
	}
	return next, nil
}

// A redirect without a usable Location, or to a plain-HTTP host, is refused like any other.
func githubDownloadLocation(f githubForward, resp *http.Response) *url.URL {
	if !f.download || f.method != http.MethodGet {
		return nil
	}
	loc, err := resp.Location()
	if err != nil {
		return nil
	}
	upstream, err := url.Parse(f.url)
	if err != nil || loc.Host == "" || (loc.Scheme != "https" && loc.Scheme != upstream.Scheme) {
		return nil
	}
	return loc
}

// Error text from the HTTP client names the URL, never the token, which travels in a header.
func (s *Server) githubProxyFailed(w http.ResponseWriter, r *http.Request, f githubForward, err error) {
	path := f.url
	if u, perr := url.Parse(f.url); perr == nil {
		path = u.Path
	}
	logx.Warn(err, "api.github.log.proxy_upstream_failed", "project", f.projectID, "path", path)
	writeErrKey(w, r, http.StatusBadGateway, "api.github.errors.proxy_upstream_failed")
}

// GitHub's next/last links can name the repository by id (/repositories/123/...), which the
// proxy's table does not know. A page link changes only the query, so it is put back on the path
// the caller asked for; octokit prefixes a relative link with its base URL.
func rewriteGithubLink(link, path string) string {
	parts := strings.Split(link, ",")
	out := parts[:0]
	for _, p := range parts {
		start, end := strings.Index(p, "<"), strings.Index(p, ">")
		if start < 0 || end < start {
			continue
		}
		u, err := url.Parse(p[start+1 : end])
		if err != nil {
			continue
		}
		target := path
		if u.RawQuery != "" {
			target += "?" + u.RawQuery
		}
		out = append(out, p[:start+1]+target+p[end:])
	}
	return strings.Join(out, ",")
}

//i18n:ignore GraphQL query, not text
const githubNodeRepoQuery = `query($id: ID!) { node(id: $id) {
  ... on PullRequestReviewThread { pullRequest { repository { nameWithOwner } } }
  ... on PullRequest { repository { nameWithOwner } }
} }`

// githubNodeRepository answers the owner/name a review thread or pull request belongs to, or ""
// for any other node: a node id names no repository, and a PAT reaches every repository its owner
// does.
func githubNodeRepository(ctx context.Context, token, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	payload, err := json.Marshal(map[string]any{"query": githubNodeRepoQuery, "variables": map[string]string{"id": id}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubcfg.GraphQLURL(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := githubProxyClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", apperr.New("api.github.errors.proxy_node_lookup_failed", "status", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Node *struct {
				Repository  *struct{ NameWithOwner string } `json:"repository"`
				PullRequest *struct {
					Repository *struct{ NameWithOwner string } `json:"repository"`
				} `json:"pullRequest"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	n := out.Data.Node
	switch {
	case n == nil:
		return "", nil
	case n.Repository != nil:
		return n.Repository.NameWithOwner, nil
	case n.PullRequest != nil && n.PullRequest.Repository != nil:
		return n.PullRequest.Repository.NameWithOwner, nil
	}
	return "", nil
}
