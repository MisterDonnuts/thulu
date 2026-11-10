import { t } from "../i18n";

const KEYS: Partial<Record<string, Partial<Record<string, string>>>> = {
  "thulu-github": {
    pr_view: "lib.mcp_tool_text.thulu_github.pr_view",
    pr_diff: "lib.mcp_tool_text.thulu_github.pr_diff",
    pr_list: "lib.mcp_tool_text.thulu_github.pr_list",
    pr_files: "lib.mcp_tool_text.thulu_github.pr_files",
    pr_comments_list: "lib.mcp_tool_text.thulu_github.pr_comments_list",
    pr_review_threads: "lib.mcp_tool_text.thulu_github.pr_review_threads",
    pr_reviews_list: "lib.mcp_tool_text.thulu_github.pr_reviews_list",
    pr_reviewers: "lib.mcp_tool_text.thulu_github.pr_reviewers",
    pr_checks: "lib.mcp_tool_text.thulu_github.pr_checks",
    labels_list: "lib.mcp_tool_text.thulu_github.labels_list",
    commit: "lib.mcp_tool_text.thulu_github.commit",
    branch_status: "lib.mcp_tool_text.thulu_github.branch_status",
    branch_push: "lib.mcp_tool_text.thulu_github.branch_push",
    pr_comment: "lib.mcp_tool_text.thulu_github.pr_comment",
    pr_review_submit: "lib.mcp_tool_text.thulu_github.pr_review_submit",
    pr_review_comment_reply: "lib.mcp_tool_text.thulu_github.pr_review_comment_reply",
    pr_review_thread_resolve: "lib.mcp_tool_text.thulu_github.pr_review_thread_resolve",
    pr_review_thread_unresolve: "lib.mcp_tool_text.thulu_github.pr_review_thread_unresolve",
    pr_reviewers_request: "lib.mcp_tool_text.thulu_github.pr_reviewers_request",
    pr_reviewers_remove: "lib.mcp_tool_text.thulu_github.pr_reviewers_remove",
    pr_labels_add: "lib.mcp_tool_text.thulu_github.pr_labels_add",
    pr_labels_remove: "lib.mcp_tool_text.thulu_github.pr_labels_remove",
    pr_labels_set: "lib.mcp_tool_text.thulu_github.pr_labels_set",
    pr_assignees_add: "lib.mcp_tool_text.thulu_github.pr_assignees_add",
    pr_assignees_remove: "lib.mcp_tool_text.thulu_github.pr_assignees_remove",
    pr_update: "lib.mcp_tool_text.thulu_github.pr_update",
    pr_ready_for_review: "lib.mcp_tool_text.thulu_github.pr_ready_for_review",
    pr_create: "lib.mcp_tool_text.thulu_github.pr_create",
    workflow_list: "lib.mcp_tool_text.thulu_github.workflow_list",
    run_list: "lib.mcp_tool_text.thulu_github.run_list",
    run_jobs: "lib.mcp_tool_text.thulu_github.run_jobs",
    run_logs: "lib.mcp_tool_text.thulu_github.run_logs",
    run_view: "lib.mcp_tool_text.thulu_github.run_view",
    run_watch: "lib.mcp_tool_text.thulu_github.run_watch",
    run_pending_deployments: "lib.mcp_tool_text.thulu_github.run_pending_deployments",
    run_artifacts: "lib.mcp_tool_text.thulu_github.run_artifacts",
    workflow_view: "lib.mcp_tool_text.thulu_github.workflow_view",
    run_rerun: "lib.mcp_tool_text.thulu_github.run_rerun",
    run_cancel: "lib.mcp_tool_text.thulu_github.run_cancel",
    run_dispatch: "lib.mcp_tool_text.thulu_github.run_dispatch",
    deployment_approve: "lib.mcp_tool_text.thulu_github.deployment_approve",
    pr_merge: "lib.mcp_tool_text.thulu_github.pr_merge",
  },
};

export function toolDescription(server: string, tool: string, fallback: string): string {
  const key = KEYS[server]?.[tool];
  return key ? t(key) : fallback;
}

const SERVER_KEYS: Partial<Record<string, string>> = {
  "thulu-browser": "lib.mcp_tool_text.server.thulu_browser",
  "thulu-cloud": "lib.mcp_tool_text.server.thulu_cloud",
  "thulu-figma": "lib.mcp_tool_text.server.thulu_figma",
  "thulu-github": "lib.mcp_tool_text.server.thulu_github",
  "thulu-sandbox": "lib.mcp_tool_text.server.thulu_sandbox",
  "octopus-api": "lib.mcp_tool_text.server.octopus_api",
};

export function firstSentence(text: string): string {
  const flat = text.replace(/\s+/g, " ").trim();
  const end = flat.search(/[.!?](\s|$)/);
  return end === -1 ? flat : flat.slice(0, end + 1);
}

export function serverDescription(server: string, fallback: string): string {
  const key = SERVER_KEYS[server];
  return key ? t(key) : firstSentence(fallback);
}
