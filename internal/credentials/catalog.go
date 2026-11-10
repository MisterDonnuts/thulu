package credentials

import (
	"context"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Must agree with `requires` in tools/*/tool.yaml (checked by catalog_test.go); lives in Go because
// Kubernetes mode has no tools/ on disk.
// Requires lists only permissions without which a tool cannot work at all; provider IAM is the real
// boundary.

type ToolSpec struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
	// The key set is the set of supported providers: a credential whose provider is absent cannot
	// drive this tool.
	Requires   map[string][]string `json:"requires"`
	Summary    string              `json:"summary,omitempty"`
	summaryKey string
}

func (t ToolSpec) SummaryIn(ctx context.Context) string {
	if t.summaryKey == "" {
		return t.Summary
	}
	return i18n.TCtx(ctx, t.summaryKey)
}

func (t ToolSpec) Providers() []string {
	out := make([]string, 0, len(t.Requires))
	for p := range t.Requires {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

type Catalog struct {
	byKey map[string]ToolSpec
}

func NewCatalog() *Catalog {
	c := &Catalog{byKey: map[string]ToolSpec{}}
	for _, t := range builtinTools {
		c.byKey[PolicyKey(t.Server, t.Tool)] = t
	}
	return c
}

func (c *Catalog) Lookup(server, tool string) (ToolSpec, bool) {
	t, ok := c.byKey[PolicyKey(server, tool)]
	return t, ok
}

func (c *Catalog) All() []ToolSpec { return c.AllIn(context.Background()) }

func (c *Catalog) AllIn(ctx context.Context) []ToolSpec {
	out := make([]ToolSpec, 0, len(c.byKey))
	for _, t := range c.byKey {
		t.Summary = t.SummaryIn(ctx)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

func (c *Catalog) ForProvider(provider string) []ToolSpec {
	var out []ToolSpec
	for _, t := range c.All() {
		if _, ok := t.Requires[provider]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (c *Catalog) Permissions() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range c.byKey {
		for _, perms := range t.Requires {
			for _, p := range perms {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

const (
	permGitHubRepoRead     = "github:repo:read"               //i18n:ignore provider permission identifier
	permGitHubRepoWrite    = "github:repo:write"              //i18n:ignore provider permission identifier
	permGitHubActionsRead  = "github:actions:read"            //i18n:ignore provider permission identifier
	permGitHubActionsWrite = "github:actions:write"           //i18n:ignore provider permission identifier
	permAWSGetMetricData   = "aws:cloudwatch:GetMetricData"   //i18n:ignore provider permission identifier
	permAWSStartQuery      = "aws:logs:StartQuery"            //i18n:ignore provider permission identifier
	permAWSGetQueryResults = "aws:logs:GetQueryResults"       //i18n:ignore provider permission identifier
	permGCPTimeSeriesList  = "gcp:monitoring.timeSeries.list" //i18n:ignore provider permission identifier
	permGCPLogEntriesList  = "gcp:logging.logEntries.list"    //i18n:ignore provider permission identifier
	permGCPRunServicesList = "gcp:run.services.list"          //i18n:ignore provider permission identifier
	// Figma does not report a token's scopes, so these are never checked; they only tell the
	// operator which scope to mint.
	permFigmaFileContentRead  = "figma:file_content:read"  //i18n:ignore provider permission identifier
	permFigmaFileCommentsRead = "figma:file_comments:read" //i18n:ignore provider permission identifier
	permFigmaProjectsRead     = "figma:projects:read"      //i18n:ignore provider permission identifier
)

var builtinTools = []ToolSpec{
	{Server: "thulu-github", Tool: "pr_view", summaryKey: "credentials.labels.tool_pr_view",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_diff", summaryKey: "credentials.labels.tool_pr_diff",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_list", summaryKey: "credentials.labels.tool_pr_list",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_files", summaryKey: "credentials.labels.tool_pr_files",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_comments_list", summaryKey: "credentials.labels.tool_pr_comments_list",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_review_threads", summaryKey: "credentials.labels.tool_pr_review_threads",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_reviews_list", summaryKey: "credentials.labels.tool_pr_reviews_list",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_reviewers", summaryKey: "credentials.labels.tool_pr_reviewers",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "pr_checks", summaryKey: "credentials.labels.tool_pr_checks",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "labels_list", summaryKey: "credentials.labels.tool_labels_list",
		Requires: map[string][]string{"github": {permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "branch_push", summaryKey: "credentials.labels.tool_branch_push",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_comment", summaryKey: "credentials.labels.tool_pr_comment",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_review_submit", summaryKey: "credentials.labels.tool_pr_review_submit",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_review_comment_reply", summaryKey: "credentials.labels.tool_pr_review_comment_reply",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_review_thread_resolve", summaryKey: "credentials.labels.tool_pr_review_thread_resolve",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_review_thread_unresolve", summaryKey: "credentials.labels.tool_pr_review_thread_unresolve",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_reviewers_request", summaryKey: "credentials.labels.tool_pr_reviewers_request",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_reviewers_remove", summaryKey: "credentials.labels.tool_pr_reviewers_remove",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_labels_add", summaryKey: "credentials.labels.tool_pr_labels_add",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_labels_remove", summaryKey: "credentials.labels.tool_pr_labels_remove",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_labels_set", summaryKey: "credentials.labels.tool_pr_labels_set",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_assignees_add", summaryKey: "credentials.labels.tool_pr_assignees_add",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_assignees_remove", summaryKey: "credentials.labels.tool_pr_assignees_remove",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_update", summaryKey: "credentials.labels.tool_pr_update",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_ready_for_review", summaryKey: "credentials.labels.tool_pr_ready_for_review",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "pr_create", summaryKey: "credentials.labels.tool_pr_create",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},
	{Server: "thulu-github", Tool: "workflow_list", summaryKey: "credentials.labels.tool_workflow_list",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_list", summaryKey: "credentials.labels.tool_run_list",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_jobs", summaryKey: "credentials.labels.tool_run_jobs",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_logs", summaryKey: "credentials.labels.tool_run_logs",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_view", summaryKey: "credentials.labels.tool_run_view",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_watch", summaryKey: "credentials.labels.tool_run_watch",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_pending_deployments", summaryKey: "credentials.labels.tool_run_pending_deployments",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	{Server: "thulu-github", Tool: "run_artifacts", summaryKey: "credentials.labels.tool_run_artifacts",
		Requires: map[string][]string{"github": {permGitHubActionsRead}}},
	// workflow_view reads the workflow file, so it needs repo contents too.
	{Server: "thulu-github", Tool: "workflow_view", summaryKey: "credentials.labels.tool_workflow_view",
		Requires: map[string][]string{"github": {permGitHubActionsRead, permGitHubRepoRead}}},
	{Server: "thulu-github", Tool: "run_dispatch", summaryKey: "credentials.labels.tool_run_dispatch",
		Requires: map[string][]string{"github": {permGitHubActionsWrite}}},
	{Server: "thulu-github", Tool: "run_rerun", summaryKey: "credentials.labels.tool_run_rerun",
		Requires: map[string][]string{"github": {permGitHubActionsWrite}}},
	{Server: "thulu-github", Tool: "run_cancel", summaryKey: "credentials.labels.tool_run_cancel",
		Requires: map[string][]string{"github": {permGitHubActionsWrite}}},
	{Server: "thulu-github", Tool: "deployment_approve", summaryKey: "credentials.labels.tool_deployment_approve",
		Requires: map[string][]string{"github": {permGitHubActionsWrite}}},
	{Server: "thulu-github", Tool: "pr_merge", summaryKey: "credentials.labels.tool_pr_merge",
		Requires: map[string][]string{"github": {permGitHubRepoWrite}}},

	// environments_list is deliberately absent: it reads Thulu's own rows, and gating it on a
	// cloud credential would hide the list an agent needs to pick one.
	{
		Server: "thulu-cloud", Tool: "discover_resources",
		summaryKey: "credentials.labels.tool_discover_resources",
		// Empty on purpose: the tool inventories six services independently and reports unreadable
		// ones as gaps.
		Requires: map[string][]string{"aws": {}, "gcp": {}},
	},
	{
		Server: "thulu-cloud", Tool: "metrics_query", summaryKey: "credentials.labels.tool_metrics_query",
		Requires: map[string][]string{
			"aws": {permAWSGetMetricData},
			"gcp": {permGCPTimeSeriesList},
		},
	},
	{
		Server: "thulu-cloud", Tool: "logs_search", summaryKey: "credentials.labels.tool_logs_search",
		Requires: map[string][]string{
			"aws": {permAWSStartQuery, permAWSGetQueryResults},
			"gcp": {permGCPLogEntriesList},
		},
	},
	{
		Server: "thulu-cloud", Tool: "error_groups", summaryKey: "credentials.labels.tool_error_groups",
		Requires: map[string][]string{
			"aws": {permAWSStartQuery, permAWSGetQueryResults},
			// The tool falls back to plain Cloud Logging, so only the fallback's permission is
			// required.
			"gcp": {permGCPLogEntriesList},
		},
	},
	{
		Server: "thulu-cloud", Tool: "deploys", summaryKey: "credentials.labels.tool_deploys",
		// AWS absent on purpose: the tool reports itself unavailable there.
		Requires: map[string][]string{"gcp": {permGCPRunServicesList}},
	},

	{Server: "thulu-figma", Tool: "file_get", summaryKey: "credentials.labels.tool_figma_file_get",
		Requires: map[string][]string{"figma": {permFigmaFileContentRead}}},
	{Server: "thulu-figma", Tool: "nodes_get", summaryKey: "credentials.labels.tool_figma_nodes_get",
		Requires: map[string][]string{"figma": {permFigmaFileContentRead}}},
	{Server: "thulu-figma", Tool: "images_render", summaryKey: "credentials.labels.tool_figma_images_render",
		Requires: map[string][]string{"figma": {permFigmaFileContentRead}}},
	{Server: "thulu-figma", Tool: "comments_list", summaryKey: "credentials.labels.tool_figma_comments_list",
		Requires: map[string][]string{"figma": {permFigmaFileCommentsRead}}},
	{Server: "thulu-figma", Tool: "team_projects_list", summaryKey: "credentials.labels.tool_figma_team_projects_list",
		Requires: map[string][]string{"figma": {permFigmaProjectsRead}}},
	{Server: "thulu-figma", Tool: "project_files_list", summaryKey: "credentials.labels.tool_figma_project_files_list",
		Requires: map[string][]string{"figma": {permFigmaProjectsRead}}},
}
