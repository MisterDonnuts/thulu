import { AsyncLocalStorage } from "node:async_hooks";
import { timingSafeEqual } from "node:crypto";

import express from "express";
import { Octokit } from "@octokit/rest";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import {
  ListToolsRequestSchema,
  CallToolRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

import { t, tLog } from "./i18n.js";

const PORT = Number(process.env.PORT || 3000);
const OCTOPUS_API = process.env.OCTOPUS_API || "http://host.docker.internal:8585";
// Daemon calls use the caller's run key from the SSE handshake (set by
// images/snippets/mcp-json.js), never a server-wide key.
const callerKey = new AsyncLocalStorage();
const sessionKeys = new Map();

function keyFromRequest(req) {
  const direct = req.headers["x-api-key"];
  if (typeof direct === "string" && direct.trim()) return direct.trim();
  const bearer = req.headers.authorization;
  if (typeof bearer === "string" && bearer.startsWith("Bearer ")) {
    return bearer.slice("Bearer ".length).trim(); // i18n-ignore HTTP auth scheme
  }
  return "";
}

// A session id travels in a URL and is not a credential: every POST must carry the handshake's key.
// Only the keyless ?catalog=1 probe is accepted, and it can list tools but never call one.
function sameKey(a, b) {
  if (!a || !b) return false;
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && timingSafeEqual(x, y);
}

function octopusAuthHeaders() {
  const key = callerKey.getStore() || "";
  if (!key) {
    throw new Error(t("errors.no_credential"));
  }
  return { "X-API-Key": key };
}

// Never fall back to a server-wide GH_TOKEN/GITHUB_TOKEN: every workspace calling a shared server
// would act on GitHub with it.
const GITHUB_API_URL = process.env.GITHUB_API_URL || "";
const DIFF_CAP_BYTES = Number(process.env.DIFF_CAP_BYTES || 80 * 1024);
const LOG_CAP_BYTES = Number(process.env.LOG_CAP_BYTES || 80 * 1024);
const LIST_CAP = Number(process.env.LIST_CAP || 300);
const BODY_CAP_BYTES = Number(process.env.BODY_CAP_BYTES || 8 * 1024);
const HUNK_CAP_BYTES = Number(process.env.HUNK_CAP_BYTES || 2 * 1024);

async function callOctopusBranchPush(taskId) {
  if (!taskId) {
    throw new Error(t("errors.task_context_required", { tool: "branch_push" }));
  }
  const r = await fetch(
    `${OCTOPUS_API}/internal/tasks/${encodeURIComponent(taskId)}/branch-push`,
    { method: "POST", headers: octopusAuthHeaders() },
  );
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.octopus_call_failed", { op: "branch-push", status: r.status, body: text.trim() }));
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

async function callOctopusBranchStatus(taskId) {
  if (!taskId) {
    throw new Error(t("errors.task_context_required", { tool: "branch_status" }));
  }
  const r = await fetch(
    `${OCTOPUS_API}/internal/tasks/${encodeURIComponent(taskId)}/branch-status`,
    { headers: octopusAuthHeaders() },
  );
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.octopus_call_failed", { op: "branch-status", status: r.status, body: text.trim() }));
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

async function callOctopusCommit(taskId, message) {
  if (!taskId) {
    throw new Error(t("errors.task_context_required", { tool: "commit" }));
  }
  if (!message || typeof message !== "string") {
    throw new Error(t("errors.commit_message_required"));
  }
  const r = await fetch(
    `${OCTOPUS_API}/internal/tasks/${encodeURIComponent(taskId)}/commit`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json", ...octopusAuthHeaders() },
      body: JSON.stringify({ message }),
    },
  );
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.octopus_call_failed", { op: "commit", status: r.status, body: text.trim() }));
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

// Best-effort: the PR already exists, so a failure here must not fail pr_create (the agent would
// retry it).
// The daemon's Handoff fallback still adopts the PR later.
async function callOctopusRecordPR(taskId, repo, url, number) {
  if (!taskId) return { recorded: false, reason: t("errors.no_task_context") };
  try {
    const r = await fetch(`${OCTOPUS_API}/internal/tasks/${encodeURIComponent(taskId)}/pr`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...octopusAuthHeaders() },
      body: JSON.stringify({ repo, url, number }),
    });
    const text = await r.text();
    if (!r.ok) return { recorded: false, reason: `HTTP ${r.status}: ${text.trim()}` };
    return { recorded: true };
  } catch (err) {
    return { recorded: false, reason: err.message };
  }
}

// In a chat the daemon mints repo tokens without contents:write or deployments:write,
// so pr_merge and deployment_approve go to the daemon (callOctopusHeld) to wait for the person's
// confirmation.
function chatSession(ctx) {
  return !ctx.taskId && !ctx.projectId;
}

function heldPath(args, rest) {
  const { owner, repo } = parseRepo(args.repo);
  return `/internal/github/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/${rest}`;
}

async function callOctopusHeld(op, path, body) {
  const r = await fetch(`${OCTOPUS_API}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...octopusAuthHeaders() },
    body: JSON.stringify(body),
  });
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.octopus_call_failed", { op, status: r.status, body: text.trim() }));
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

const SCOPE_IMPLIES = {
  read: new Set(["read"]),
  write: new Set(["read", "write"]),
  admin: new Set(["read", "write", "admin"]),
};

// No scope grants DEPLOY_SCOPE, not even write: these tools must be named one by one
// (thulu-github@run_view+run_dispatch).
const DEPLOY_SCOPE = "deploy";

const DISPATCH_LOOKUP_MS = Number(process.env.DISPATCH_LOOKUP_MS || 20_000);
const DISPATCH_POLL_MS = Number(process.env.DISPATCH_POLL_MS || 2_000);
// Keep under the MCP client's tool timeout; long waits die there, not here.
const WATCH_MAX_SECONDS = Number(process.env.WATCH_MAX_SECONDS || 600);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const TOOLS = [
  {
    scope: "read",
    name: "pr_view",
    description: t("tools.pr_view.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: {
          type: "string",
          description: t("tools.pr_view.params.repo"),
        },
        number: { type: "integer", description: t("tools.pr_view.params.number") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_diff",
    description: t("tools.pr_diff.description", { cap_bytes: DIFF_CAP_BYTES }),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_diff.params.repo") },
        number: { type: "integer", description: t("tools.pr_diff.params.number") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_list",
    description: t("tools.pr_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_list.params.repo") },
        state: {
          type: "string",
          enum: ["open", "closed", "all"],
          description: t("tools.pr_list.params.state"),
        },
        head: {
          type: "string",
          description: t("tools.pr_list.params.head"),
        },
        base: { type: "string", description: t("tools.pr_list.params.base") },
        per_page: {
          type: "integer",
          description: t("tools.pr_list.params.per_page"),
        },
        page: { type: "integer", description: t("tools.pr_list.params.page") },
      },
      required: ["repo"],
    },
  },
  {
    scope: "read",
    name: "pr_files",
    description: t("tools.pr_files.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_files.params.repo") },
        number: { type: "integer", description: t("tools.pr_files.params.number") },
        per_page: { type: "integer", description: t("tools.pr_files.params.per_page") },
        page: { type: "integer", description: t("tools.pr_files.params.page") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_comments_list",
    description: t("tools.pr_comments_list.description", { cap: LIST_CAP }),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_comments_list.params.repo") },
        number: { type: "integer", description: t("tools.pr_comments_list.params.number") },
        since: { type: "string", description: t("tools.pr_comments_list.params.since") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_review_threads",
    description: t("tools.pr_review_threads.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_review_threads.params.repo") },
        number: { type: "integer", description: t("tools.pr_review_threads.params.number") },
        state: {
          type: "string",
          enum: ["all", "unresolved", "resolved"],
          description: t("tools.pr_review_threads.params.state"),
        },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_reviews_list",
    description: t("tools.pr_reviews_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_reviews_list.params.repo") },
        number: { type: "integer", description: t("tools.pr_reviews_list.params.number") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_reviewers",
    description: t("tools.pr_reviewers.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_reviewers.params.repo") },
        number: { type: "integer", description: t("tools.pr_reviewers.params.number") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "pr_checks",
    description: t("tools.pr_checks.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_checks.params.repo") },
        number: { type: "integer", description: t("tools.pr_checks.params.number") },
        only_failed: { type: "boolean", description: t("tools.pr_checks.params.only_failed") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "read",
    name: "labels_list",
    description: t("tools.labels_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.labels_list.params.repo") },
      },
      required: ["repo"],
    },
  },

  {
    scope: "write",
    name: "commit",
    description: t("tools.commit.description"),
    inputSchema: {
      type: "object",
      properties: {
        message: {
          type: "string",
          description: t("tools.commit.params.message"),
        },
      },
      required: ["message"],
    },
  },
  {
    scope: "write",
    name: "branch_push",
    description: t("tools.branch_push.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "branch_status",
    description: t("tools.branch_status.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "write",
    name: "pr_comment",
    description: t("tools.pr_comment.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_comment.params.repo") },
        number: { type: "integer", description: t("tools.pr_comment.params.number") },
        body: { type: "string", description: t("tools.pr_comment.params.body") },
      },
      required: ["repo", "number", "body"],
    },
  },
  {
    scope: "write",
    name: "pr_review_submit",
    description: t("tools.pr_review_submit.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_review_submit.params.repo") },
        number: { type: "integer", description: t("tools.pr_review_submit.params.number") },
        event: {
          type: "string",
          enum: ["APPROVE", "REQUEST_CHANGES", "COMMENT"],
          description: t("tools.pr_review_submit.params.event"),
        },
        body: {
          type: "string",
          description: t("tools.pr_review_submit.params.body"),
        },
        comments: {
          type: "array",
          description: t("tools.pr_review_submit.params.comments"),
          items: {
            type: "object",
            properties: {
              path: { type: "string" },
              line: {
                type: "integer",
                description: t("tools.pr_review_submit.params.comments_line"),
              },
              body: { type: "string" },
            },
            required: ["path", "line", "body"],
          },
        },
      },
      required: ["repo", "number", "event"],
    },
  },
  {
    scope: "write",
    name: "pr_review_comment_reply",
    description: t("tools.pr_review_comment_reply.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_review_comment_reply.params.repo") },
        number: { type: "integer", description: t("tools.pr_review_comment_reply.params.number") },
        comment_id: {
          type: "integer",
          description: t("tools.pr_review_comment_reply.params.comment_id"),
        },
        body: { type: "string", description: t("tools.pr_review_comment_reply.params.body") },
      },
      required: ["repo", "number", "comment_id", "body"],
    },
  },
  {
    scope: "write",
    name: "pr_review_thread_resolve",
    description: t("tools.pr_review_thread_resolve.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_review_thread_resolve.params.repo") },
        thread_id: {
          type: "string",
          description: t("tools.pr_review_thread_resolve.params.thread_id"),
        },
      },
      required: ["repo", "thread_id"],
    },
  },
  {
    scope: "write",
    name: "pr_review_thread_unresolve",
    description: t("tools.pr_review_thread_unresolve.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_review_thread_unresolve.params.repo") },
        thread_id: {
          type: "string",
          description: t("tools.pr_review_thread_unresolve.params.thread_id"),
        },
      },
      required: ["repo", "thread_id"],
    },
  },
  {
    scope: "write",
    name: "pr_reviewers_request",
    description: t("tools.pr_reviewers_request.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_reviewers_request.params.repo") },
        number: { type: "integer", description: t("tools.pr_reviewers_request.params.number") },
        reviewers: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_reviewers_request.params.reviewers"),
        },
        team_reviewers: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_reviewers_request.params.team_reviewers"),
        },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "write",
    name: "pr_reviewers_remove",
    description: t("tools.pr_reviewers_remove.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_reviewers_remove.params.repo") },
        number: { type: "integer", description: t("tools.pr_reviewers_remove.params.number") },
        reviewers: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_reviewers_remove.params.reviewers"),
        },
        team_reviewers: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_reviewers_remove.params.team_reviewers"),
        },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "write",
    name: "pr_labels_add",
    description: t("tools.pr_labels_add.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_labels_add.params.repo") },
        number: { type: "integer", description: t("tools.pr_labels_add.params.number") },
        labels: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_labels_add.params.labels"),
        },
      },
      required: ["repo", "number", "labels"],
    },
  },
  {
    scope: "write",
    name: "pr_labels_remove",
    description: t("tools.pr_labels_remove.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_labels_remove.params.repo") },
        number: { type: "integer", description: t("tools.pr_labels_remove.params.number") },
        labels: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_labels_remove.params.labels"),
        },
      },
      required: ["repo", "number", "labels"],
    },
  },
  {
    scope: "write",
    name: "pr_labels_set",
    description: t("tools.pr_labels_set.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_labels_set.params.repo") },
        number: { type: "integer", description: t("tools.pr_labels_set.params.number") },
        labels: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_labels_set.params.labels"),
        },
      },
      required: ["repo", "number", "labels"],
    },
  },
  {
    scope: "write",
    name: "pr_assignees_add",
    description: t("tools.pr_assignees_add.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_assignees_add.params.repo") },
        number: { type: "integer", description: t("tools.pr_assignees_add.params.number") },
        assignees: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_assignees_add.params.assignees"),
        },
      },
      required: ["repo", "number", "assignees"],
    },
  },
  {
    scope: "write",
    name: "pr_assignees_remove",
    description: t("tools.pr_assignees_remove.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_assignees_remove.params.repo") },
        number: { type: "integer", description: t("tools.pr_assignees_remove.params.number") },
        assignees: {
          type: "array",
          items: { type: "string" },
          description: t("tools.pr_assignees_remove.params.assignees"),
        },
      },
      required: ["repo", "number", "assignees"],
    },
  },
  {
    scope: "write",
    name: "pr_update",
    description: t("tools.pr_update.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_update.params.repo") },
        number: { type: "integer", description: t("tools.pr_update.params.number") },
        title: { type: "string", description: t("tools.pr_update.params.title") },
        body: { type: "string", description: t("tools.pr_update.params.body") },
        base: { type: "string", description: t("tools.pr_update.params.base") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "write",
    name: "pr_ready_for_review",
    description: t("tools.pr_ready_for_review.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_ready_for_review.params.repo") },
        number: { type: "integer", description: t("tools.pr_ready_for_review.params.number") },
        draft: { type: "boolean", description: t("tools.pr_ready_for_review.params.draft") },
      },
      required: ["repo", "number"],
    },
  },
  {
    scope: "write",
    name: "pr_create",
    description: t("tools.pr_create.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_create.params.repo") },
        head: {
          type: "string",
          description: t("tools.pr_create.params.head"),
        },
        base: { type: "string", description: t("tools.pr_create.params.base") },
        title: { type: "string", description: t("tools.pr_create.params.title") },
        body: { type: "string", description: t("tools.pr_create.params.body") },
        draft: {
          type: "boolean",
          description: t("tools.pr_create.params.draft"),
        },
      },
      required: ["repo", "head", "base", "title"],
    },
  },

  {
    scope: "read",
    name: "workflow_list",
    description: t("tools.workflow_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.workflow_list.params.repo") },
      },
      required: ["repo"],
    },
  },
  {
    scope: "read",
    name: "run_list",
    description: t("tools.run_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_list.params.repo") },
        workflow_id: {
          type: ["string", "integer"],
          description: t("tools.run_list.params.workflow_id"),
        },
        branch: { type: "string", description: t("tools.run_list.params.branch") },
        status: {
          type: "string",
          enum: [
            "queued",
            "in_progress",
            "completed",
            "success",
            "failure",
            "cancelled",
            "waiting",
          ],
          description: t("tools.run_list.params.status"),
        },
        per_page: { type: "integer", description: t("tools.run_list.params.per_page") },
        page: { type: "integer", description: t("tools.run_list.params.page") },
      },
      required: ["repo"],
    },
  },
  {
    scope: "read",
    name: "run_jobs",
    description: t("tools.run_jobs.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_jobs.params.repo") },
        run_id: { type: "integer", description: t("tools.run_jobs.params.run_id") },
        filter: {
          type: "string",
          enum: ["latest", "all"],
          description: t("tools.run_jobs.params.filter"),
        },
        failed_only: {
          type: "boolean",
          description: t("tools.run_jobs.params.failed_only"),
        },
        per_page: { type: "integer", description: t("tools.run_jobs.params.per_page") },
        page: { type: "integer", description: t("tools.run_jobs.params.page") },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "run_logs",
    description: t("tools.run_logs.description", { cap_bytes: LOG_CAP_BYTES }),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_logs.params.repo") },
        run_id: { type: "integer", description: t("tools.run_logs.params.run_id") },
        job_id: {
          type: "integer",
          description: t("tools.run_logs.params.job_id"),
        },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "run_view",
    description: t("tools.run_view.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_view.params.repo") },
        run_id: { type: "integer", description: t("tools.run_view.params.run_id") },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "run_watch",
    description: t("tools.run_watch.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_watch.params.repo") },
        run_id: { type: "integer", description: t("tools.run_watch.params.run_id") },
        timeout_seconds: {
          type: "integer",
          description: t("tools.run_watch.params.timeout_seconds", { max_seconds: WATCH_MAX_SECONDS }),
        },
        poll_seconds: {
          type: "integer",
          description: t("tools.run_watch.params.poll_seconds"),
        },
        stop_on_waiting: {
          type: "boolean",
          description: t("tools.run_watch.params.stop_on_waiting"),
        },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "run_pending_deployments",
    description: t("tools.run_pending_deployments.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_pending_deployments.params.repo") },
        run_id: { type: "integer", description: t("tools.run_pending_deployments.params.run_id") },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "run_artifacts",
    description: t("tools.run_artifacts.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_artifacts.params.repo") },
        run_id: { type: "integer", description: t("tools.run_artifacts.params.run_id") },
        per_page: { type: "integer", description: t("tools.run_artifacts.params.per_page") },
        page: { type: "integer", description: t("tools.run_artifacts.params.page") },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "read",
    name: "workflow_view",
    description: t("tools.workflow_view.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.workflow_view.params.repo") },
        workflow_id: {
          type: ["string", "integer"],
          description: t("tools.workflow_view.params.workflow_id"),
        },
        ref: {
          type: "string",
          description: t("tools.workflow_view.params.ref"),
        },
        include_source: {
          type: "boolean",
          description: t("tools.workflow_view.params.include_source"),
        },
      },
      required: ["repo", "workflow_id"],
    },
  },

  {
    scope: DEPLOY_SCOPE,
    name: "run_dispatch",
    description: t("tools.run_dispatch.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_dispatch.params.repo") },
        workflow_id: {
          type: ["string", "integer"],
          description: t("tools.run_dispatch.params.workflow_id"),
        },
        ref: {
          type: "string",
          description: t("tools.run_dispatch.params.ref"),
        },
        inputs: {
          type: "object",
          description: t("tools.run_dispatch.params.inputs"),
          additionalProperties: { type: "string" },
        },
      },
      required: ["repo", "workflow_id", "ref"],
    },
  },
  {
    scope: "write",
    name: "run_rerun",
    description: t("tools.run_rerun.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_rerun.params.repo") },
        run_id: { type: "integer", description: t("tools.run_rerun.params.run_id") },
        failed_only: {
          type: "boolean",
          description: t("tools.run_rerun.params.failed_only"),
        },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    scope: "write",
    name: "run_cancel",
    description: t("tools.run_cancel.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.run_cancel.params.repo") },
        run_id: { type: "integer", description: t("tools.run_cancel.params.run_id") },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    // Granting this to the agent that runs run_dispatch defeats the environment's review gate.
    scope: DEPLOY_SCOPE,
    name: "deployment_approve",
    description: t("tools.deployment_approve.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.deployment_approve.params.repo") },
        run_id: { type: "integer", description: t("tools.deployment_approve.params.run_id") },
        state: {
          type: "string",
          enum: ["approved", "rejected"],
          description: t("tools.deployment_approve.params.state"),
        },
        environment_ids: {
          type: "array",
          items: { type: "integer" },
          description: t("tools.deployment_approve.params.environment_ids"),
        },
        comment: {
          type: "string",
          description: t("tools.deployment_approve.params.comment"),
        },
      },
      required: ["repo", "run_id"],
    },
  },
  {
    // A merge onto a branch that deploys on push is a deployment, so pr_merge is named tool by tool
    // too.
    scope: DEPLOY_SCOPE,
    name: "pr_merge",
    description: t("tools.pr_merge.description"),
    inputSchema: {
      type: "object",
      properties: {
        repo: { type: "string", description: t("tools.pr_merge.params.repo") },
        number: { type: "integer", description: t("tools.pr_merge.params.number") },
        merge_method: {
          type: "string",
          enum: ["merge", "squash", "rebase"],
          description: t("tools.pr_merge.params.merge_method"),
        },
        commit_title: { type: "string", description: t("tools.pr_merge.params.commit_title") },
        commit_message: { type: "string", description: t("tools.pr_merge.params.commit_message") },
        sha: { type: "string", description: t("tools.pr_merge.params.sha") },
      },
      required: ["repo", "number"],
    },
  },
];

function toolsForScope(scope) {
  const allowed = SCOPE_IMPLIES[scope] || SCOPE_IMPLIES.read;
  return TOOLS.filter((t) => allowed.has(t.scope));
}

// Reviewing pending deployments needs Deployments: write, not an Actions permission.
// No workflows entry: commit/branch_push hold no token; the daemon adds workflows to its own push.
const TOOL_PERMISSIONS = {
  run_dispatch: "actions:write",
  run_rerun: "actions:write",
  run_cancel: "actions:write",
  deployment_approve: "deployments:write",
  pr_checks: "checks:read",
};

// An installation has checks:read only after an owner accepts it; when the mint is refused,
// pr_checks falls back to Actions runs,
// the same way githubapp.Resolver.GetPRStatusToken does.
const OPTIONAL_PERMISSIONS = new Set(["pr_checks"]);

async function resolveToolToken(req, ctx) {
  try {
    return await resolveToken(req);
  } catch (err) {
    if (!OPTIONAL_PERMISSIONS.has(req.tool)) throw err;
    ctx.permissionRefused = TOOL_PERMISSIONS[req.tool];
    return resolveToken({ ...req, permission: null });
  }
}

async function resolveToken({ taskId, projectId, repo, tool, permission = TOOL_PERMISSIONS[tool] }) {
  if (!taskId && !projectId && repo) return resolveRepoToken(repo, permission);
  const perms = permission ? `?permissions=${permission}` : "";
  if (taskId) {
    try {
      const r = await fetch(
        `${OCTOPUS_API}/internal/tasks/${encodeURIComponent(taskId)}/github-token${perms}`,
        { headers: octopusAuthHeaders() },
      );
      if (r.ok) {
        const j = await r.json();
        if (j && j.token) return j.token;
      }
    } catch (err) {
      console.warn(tLog("log.task_token_lookup_failed", { task: taskId, err: err.message }));
    }
  }
  if (projectId) {
    try {
      const r = await fetch(
        `${OCTOPUS_API}/internal/projects/${encodeURIComponent(projectId)}/github-token${perms}`,
        { headers: octopusAuthHeaders() },
      );
      if (r.ok) {
        const j = await r.json();
        if (j && j.token) return j.token;
      }
    } catch (err) {
      console.warn(tLog("log.project_token_lookup_failed", { project: projectId, err: err.message }));
    }
  }
  throw new Error(t("errors.no_github_token"));
}

async function resolveRepoToken(repo, permission) {
  const q = new URLSearchParams({ repo: String(repo) });
  if (permission) q.set("permissions", permission);
  const r = await fetch(`${OCTOPUS_API}/internal/github-token?${q}`, { headers: octopusAuthHeaders() });
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.repo_token_failed", { repo: String(repo), status: r.status, body: text.trim() }));
  }
  let j = null;
  try {
    j = JSON.parse(text);
  } catch {
    j = null;
  }
  if (j && j.token) return j.token;
  throw new Error(t("errors.no_github_token"));
}

function parseRepo(repo) {
  if (typeof repo !== "string" || !repo.includes("/")) {
    throw new Error(t("errors.repo_format", { repo: JSON.stringify(repo) }));
  }
  const [owner, name] = repo.split("/", 2);
  if (!owner || !name) {
    throw new Error(t("errors.repo_format", { repo: `'${repo}'` }));
  }
  return { owner, repo: name };
}

// Octokit returns binary bodies as ArrayBuffer or a typed array, never a Node Buffer; normalize
// before sniffing magic numbers.
function toBuffer(data) {
  if (Buffer.isBuffer(data)) return data;
  if (data instanceof ArrayBuffer) return Buffer.from(data);
  if (ArrayBuffer.isView(data)) {
    return Buffer.from(data.buffer, data.byteOffset, data.byteLength);
  }
  return null;
}

function clip(str, cap, label) {
  if (typeof str !== "string") return str;
  if (str.length <= cap) return str;
  return (
    str.slice(0, cap) +
    t("text.truncated", { label, bytes: str.length - cap })
  );
}

function ok(value) {
  return {
    content: [
      {
        type: "text",
        text: typeof value === "string" ? value : JSON.stringify(value, null, 2),
      },
    ],
  };
}

function runSummary(r) {
  return {
    id: r.id,
    name: r.name,
    workflow_id: r.workflow_id,
    head_branch: r.head_branch,
    head_sha: r.head_sha,
    event: r.event,
    status: r.status,
    conclusion: r.conclusion,
    run_number: r.run_number,
    run_attempt: r.run_attempt,
    actor: r.actor?.login,
    html_url: r.html_url,
    created_at: r.created_at,
    updated_at: r.updated_at,
    run_started_at: r.run_started_at,
  };
}

async function pendingDeployments(octokit, owner, repo, runID) {
  const { data } = await octokit.actions.getPendingDeploymentsForRun({
    owner,
    repo,
    run_id: runID,
  });
  return (data ?? []).map((d) => ({
    environment: d.environment?.name,
    environment_id: d.environment?.id,
    wait_timer: d.wait_timer,
    wait_timer_started_at: d.wait_timer_started_at,
    current_user_can_approve: d.current_user_can_approve,
    reviewers: (d.reviewers ?? []).map((rev) => ({
      type: rev.type,
      name: rev.reviewer?.login ?? rev.reviewer?.name,
    })),
  }));
}

async function newestDispatchRunID(octokit, owner, repo, workflowID) {
  try {
    const { data } = await octokit.actions.listWorkflowRuns({
      owner,
      repo,
      workflow_id: workflowID,
      event: "workflow_dispatch",
      per_page: 20,
    });
    return (data.workflow_runs ?? []).reduce((max, r) => Math.max(max, r.id), 0);
  } catch {
    return 0;
  }
}

function refName(ref) {
  const raw = String(ref ?? "");
  return raw.replace(/^refs\/(heads|tags)\//, "");
}

async function listCapped(octokit, method, params, cap = LIST_CAP) {
  const items = [];
  for await (const { data } of octokit.paginate.iterator(method, { ...params, per_page: 100 })) {
    for (const item of data) {
      if (items.length >= cap) return { items, capped: true };
      items.push(item);
    }
  }
  return { items, capped: false };
}

// Must match internal/github/prstatus.go (CIStatus.add) so pr_checks and the task view agree on a
// PR.
const CI_FAILED = new Set(["failure", "timed_out", "cancelled", "action_required", "stale", "startup_failure"]);

function ciOutcome(status, conclusion) {
  if (status && status !== "completed") return "pending";
  return CI_FAILED.has(conclusion) ? "failing" : "passing";
}

function statusOutcome(state) {
  if (state === "pending") return "pending";
  return state === "failure" || state === "error" ? "failing" : "passing";
}

function ciVerdict(outcomes) {
  if (outcomes.includes("failing")) return "failing";
  if (outcomes.includes("pending")) return "pending";
  return outcomes.length ? "passing" : "none";
}

// A COMMENTED review after an approval leaves the approval standing, as GitHub counts it on the PR
// page.
const DECISIVE_REVIEW = new Set(["APPROVED", "CHANGES_REQUESTED", "DISMISSED"]);

function latestReviewStates(reviews) {
  const byLogin = new Map();
  for (const r of reviews) {
    const login = r.user?.login;
    if (!login || r.state === "PENDING") continue;
    const prev = byLogin.get(login);
    if (prev && DECISIVE_REVIEW.has(prev.state) && !DECISIVE_REVIEW.has(r.state)) continue;
    byLogin.set(login, { login, state: r.state, submitted_at: r.submitted_at, commit_id: r.commit_id });
  }
  return byLogin;
}

// i18n-ignore GraphQL query
const REVIEW_THREADS_QUERY = `
  query ($owner: String!, $repo: String!, $number: Int!, $after: String) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $number) {
        reviewThreads(first: 100, after: $after) {
          pageInfo { hasNextPage endCursor }
          nodes {
            id isResolved isOutdated path line originalLine diffSide
            comments(first: 50) {
              totalCount
              nodes { databaseId author { login } body createdAt url diffHunk replyTo { databaseId } }
            }
          }
        }
      }
    }
  }`;

const HANDLERS = {
  commit: async (_octokit, args, ctx) => {
    const result = await callOctopusCommit(ctx.taskId, args.message);
    return ok(result);
  },

  branch_push: async (_octokit, _args, ctx) => {
    const result = await callOctopusBranchPush(ctx.taskId);
    return ok(result);
  },

  branch_status: async (_octokit, _args, ctx) => {
    const result = await callOctopusBranchStatus(ctx.taskId);
    return ok(result);
  },

  pr_view: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.pulls.get({
      owner,
      repo,
      pull_number: args.number,
    });
    return ok({
      number: data.number,
      title: data.title,
      body: data.body,
      state: data.state,
      draft: data.draft,
      merged: data.merged,
      mergeable: data.mergeable,
      head: { ref: data.head.ref, sha: data.head.sha, repo: data.head.repo?.full_name },
      base: { ref: data.base.ref, sha: data.base.sha, repo: data.base.repo?.full_name },
      author: data.user?.login,
      comments: data.comments,
      review_comments: data.review_comments,
      changed_files: data.changed_files,
      additions: data.additions,
      deletions: data.deletions,
      html_url: data.html_url,
      created_at: data.created_at,
      updated_at: data.updated_at,
      closed_at: data.closed_at,
      merged_at: data.merged_at,
    });
  },

  pr_diff: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const res = await octokit.pulls.get({
      owner,
      repo,
      pull_number: args.number,
      mediaType: { format: "diff" },
    });
    const diff = typeof res.data === "string" ? res.data : String(res.data ?? "");
    return ok(clip(diff, DIFF_CAP_BYTES, "diff"));
  },

  pr_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.pulls.list({
      owner,
      repo,
      state: args.state ?? "open",
      head: args.head,
      base: args.base,
      per_page: args.per_page ?? 30,
      page: args.page ?? 1,
    });
    return ok(
      data.map((pr) => ({
        number: pr.number,
        title: pr.title,
        state: pr.state,
        draft: pr.draft,
        head: pr.head.ref,
        base: pr.base.ref,
        author: pr.user?.login,
        html_url: pr.html_url,
        created_at: pr.created_at,
        updated_at: pr.updated_at,
      })),
    );
  },

  pr_files: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.pulls.listFiles({
      owner,
      repo,
      pull_number: args.number,
      per_page: args.per_page ?? 30,
      page: args.page ?? 1,
    });
    return ok(
      data.map((f) => ({
        filename: f.filename,
        previous_filename: f.previous_filename,
        status: f.status,
        additions: f.additions,
        deletions: f.deletions,
        changes: f.changes,
        patch: f.patch ? clip(f.patch, DIFF_CAP_BYTES, "patch") : undefined,
      })),
    );
  },

  pr_comments_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { items, capped } = await listCapped(octokit, octokit.issues.listComments, {
      owner,
      repo,
      issue_number: args.number,
      since: args.since,
    });
    return ok({
      returned: items.length,
      capped,
      ...(capped ? { note: t("tools.pr_comments_list.notes.capped", { cap: LIST_CAP }) } : {}),
      comments: items.map((c) => ({
        id: c.id,
        author: c.user?.login,
        author_association: c.author_association,
        body: clip(c.body, BODY_CAP_BYTES, "body"),
        created_at: c.created_at,
        updated_at: c.updated_at,
        html_url: c.html_url,
      })),
    });
  },

  pr_review_threads: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const state = args.state ?? "all";
    if (!["all", "unresolved", "resolved"].includes(state)) {
      throw new Error(t("tools.pr_review_threads.errors.invalid_state", { state }));
    }
    const threads = [];
    let capped = false;
    let after = null;
    for (;;) {
      const data = await octokit.graphql(REVIEW_THREADS_QUERY, { owner, repo, number: args.number, after });
      const page = data?.repository?.pullRequest?.reviewThreads;
      for (const node of page?.nodes ?? []) {
        if (threads.length >= LIST_CAP) {
          capped = true;
          break;
        }
        threads.push(node);
      }
      if (capped || !page?.pageInfo?.hasNextPage) break;
      after = page.pageInfo.endCursor;
    }
    const shaped = threads.map((th) => {
      const comments = th.comments?.nodes ?? [];
      return {
        id: th.id,
        resolved: th.isResolved,
        outdated: th.isOutdated,
        path: th.path,
        line: th.line,
        original_line: th.originalLine,
        diff_side: th.diffSide,
        diff_hunk: clip(comments[0]?.diffHunk, HUNK_CAP_BYTES, "diff_hunk"),
        comments_total: th.comments?.totalCount ?? comments.length,
        comments: comments.map((c) => ({
          id: c.databaseId,
          author: c.author?.login,
          body: clip(c.body, BODY_CAP_BYTES, "body"),
          created_at: c.createdAt,
          url: c.url,
          in_reply_to: c.replyTo?.databaseId ?? null,
        })),
      };
    });
    const unresolved = shaped.filter((th) => !th.resolved);
    const resolved = shaped.filter((th) => th.resolved);
    const picked = { all: [...unresolved, ...resolved], unresolved, resolved }[state];
    return ok({
      total: shaped.length,
      unresolved: unresolved.length,
      resolved: resolved.length,
      capped,
      ...(capped ? { note: t("tools.pr_review_threads.notes.capped", { cap: LIST_CAP }) } : {}),
      threads: picked,
    });
  },

  pr_reviews_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { items, capped } = await listCapped(octokit, octokit.pulls.listReviews, {
      owner,
      repo,
      pull_number: args.number,
    });
    return ok({
      returned: items.length,
      capped,
      ...(capped ? { note: t("tools.pr_reviews_list.notes.capped", { cap: LIST_CAP }) } : {}),
      reviews: items.map((r) => ({
        id: r.id,
        author: r.user?.login,
        state: r.state,
        body: clip(r.body, BODY_CAP_BYTES, "body"),
        submitted_at: r.submitted_at,
        commit_id: r.commit_id,
        html_url: r.html_url,
      })),
    });
  },

  pr_reviewers: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const [{ data: requested }, { items: reviews }] = await Promise.all([
      octokit.pulls.listRequestedReviewers({ owner, repo, pull_number: args.number }),
      listCapped(octokit, octokit.pulls.listReviews, { owner, repo, pull_number: args.number }),
    ]);
    const latest = latestReviewStates(reviews);
    const byState = (state) => [...latest.values()].filter((r) => r.state === state).map((r) => r.login);
    return ok({
      requested_users: (requested.users ?? []).map((u) => ({
        login: u.login,
        latest_state: latest.get(u.login)?.state ?? null,
      })),
      requested_teams: (requested.teams ?? []).map((tm) => ({ slug: tm.slug, name: tm.name })),
      reviewers: [...latest.values()],
      approved_by: byState("APPROVED"),
      changes_requested_by: byState("CHANGES_REQUESTED"),
    });
  },

  pr_checks: async (octokit, args, ctx = {}) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data: pr } = await octokit.pulls.get({ owner, repo, pull_number: args.number });
    const sha = pr.head.sha;
    const out = { head_sha: sha, head_ref: pr.head.ref };
    const outcomes = [];
    const keep = (outcome) => !args.only_failed || outcome === "failing";

    let fallback = ctx.permissionRefused
      ? t("tools.pr_checks.notes.checks_not_minted", { permission: ctx.permissionRefused })
      : null;
    if (!fallback) {
      try {
        const { items, capped } = await listCapped(octokit, octokit.checks.listForRef, {
          owner,
          repo,
          ref: sha,
          filter: "latest",
        });
        out.source = "checks";
        const runs = items.map((c) => ({
          name: c.name,
          app: c.app?.slug,
          status: c.status,
          conclusion: c.conclusion,
          outcome: ciOutcome(c.status, c.conclusion),
          started_at: c.started_at,
          completed_at: c.completed_at,
          html_url: c.html_url,
          title: c.output?.title ?? undefined,
          summary: c.output?.summary ? clip(c.output.summary, BODY_CAP_BYTES, "summary") : undefined,
        }));
        outcomes.push(...runs.map((r) => r.outcome));
        out.check_runs = runs.filter((r) => keep(r.outcome));
        if (capped) out.check_runs_capped = true;
      } catch (err) {
        if (err?.status !== 403 && err?.status !== 404) throw err;
        fallback = t("tools.pr_checks.notes.checks_refused", { status: err.status, message: err.message });
      }
    }
    if (fallback) {
      out.source = "actions";
      out.source_note = fallback;
      const { data } = await octokit.actions.listWorkflowRunsForRepo({ owner, repo, head_sha: sha, per_page: 100 });
      const runs = (data.workflow_runs ?? []).map((r) => ({
        id: r.id,
        name: r.name,
        event: r.event,
        status: r.status,
        conclusion: r.conclusion,
        outcome: ciOutcome(r.status, r.conclusion),
        run_attempt: r.run_attempt,
        html_url: r.html_url,
        created_at: r.created_at,
        updated_at: r.updated_at,
      }));
      outcomes.push(...runs.map((r) => r.outcome));
      out.workflow_runs = runs.filter((r) => keep(r.outcome));
    }

    try {
      const { data } = await octokit.repos.getCombinedStatusForRef({ owner, repo, ref: sha, per_page: 100 });
      const statuses = (data.statuses ?? []).map((s) => ({
        context: s.context,
        state: s.state,
        outcome: statusOutcome(s.state),
        description: s.description,
        target_url: s.target_url,
      }));
      outcomes.push(...statuses.map((s) => s.outcome));
      // GitHub answers state "pending" even when a commit has no statuses.
      out.statuses = {
        state: statuses.length ? data.state : null,
        statuses: statuses.filter((s) => keep(s.outcome)),
      };
    } catch (err) {
      if (err?.status !== 403 && err?.status !== 404) throw err;
      out.statuses_unavailable = { status: err.status, message: err.message };
    }

    out.verdict = ciVerdict(outcomes);
    out.counts = {
      total: outcomes.length,
      failing: outcomes.filter((o) => o === "failing").length,
      pending: outcomes.filter((o) => o === "pending").length,
      passing: outcomes.filter((o) => o === "passing").length,
    };
    return ok(out);
  },

  labels_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { items, capped } = await listCapped(octokit, octokit.issues.listLabelsForRepo, { owner, repo });
    return ok({
      returned: items.length,
      capped,
      ...(capped ? { note: t("tools.labels_list.notes.capped", { cap: LIST_CAP }) } : {}),
      labels: items.map((l) => ({ name: l.name, color: l.color, description: l.description })),
    });
  },

  pr_comment: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.issues.createComment({
      owner,
      repo,
      issue_number: args.number,
      body: args.body,
    });
    return ok({ id: data.id, html_url: data.html_url });
  },

  pr_review_submit: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.pulls.createReview({
      owner,
      repo,
      pull_number: args.number,
      event: args.event,
      body: args.body,
      comments: args.comments,
    });
    return ok({
      id: data.id,
      state: data.state,
      html_url: data.html_url,
    });
  },

  pr_review_comment_reply: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    // GitHub threads replies only under the thread's first comment; reading the comment also
    // rejects an id from another PR before posting.
    const { data: comment } = await octokit.pulls.getReviewComment({
      owner,
      repo,
      comment_id: args.comment_id,
    });
    const actual = prNumberFromURL(comment?.pull_request_url);
    if (actual !== null && actual !== Number(args.number)) {
      throw new Error(
        t("errors.comment_other_pr", { comment_id: args.comment_id, number: args.number, actual }),
      );
    }
    const { data } = await octokit.pulls.createReplyForReviewComment({
      owner,
      repo,
      pull_number: args.number,
      comment_id: comment?.in_reply_to_id ?? args.comment_id,
      body: args.body,
    });
    return ok({ id: data.id, in_reply_to_id: data.in_reply_to_id, html_url: data.html_url });
  },

  pr_review_thread_resolve: async (octokit, args) => setThreadResolved(octokit, args, true),

  pr_review_thread_unresolve: async (octokit, args) => setThreadResolved(octokit, args, false),

  pr_reviewers_request: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const lists = reviewerLists(args);
    const { data } = await octokit.pulls.requestReviewers({
      owner,
      repo,
      pull_number: args.number,
      ...lists,
    });
    return ok(requestedReviewers(data));
  },

  pr_reviewers_remove: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const lists = reviewerLists(args);
    const { data } = await octokit.pulls.removeRequestedReviewers({
      owner,
      repo,
      pull_number: args.number,
      ...lists,
    });
    return ok(requestedReviewers(data));
  },

  pr_labels_add: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const wanted = nameList(args.labels);
    if (wanted.length === 0) throw new Error(t("errors.list_required", { param: "labels" }));
    const labels = await knownLabels(octokit, owner, repo, args.repo, wanted);
    const { data } = await octokit.issues.addLabels({
      owner,
      repo,
      issue_number: args.number,
      labels,
    });
    return ok({ number: args.number, labels: labelNames(data) });
  },

  pr_labels_remove: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const wanted = nameList(args.labels);
    if (wanted.length === 0) throw new Error(t("errors.list_required", { param: "labels" }));
    // GitHub removes one label per call and answers 404 for a label the PR lacks; report it and
    // keep removing the others.
    const notOnPR = [];
    let current = null;
    for (const name of wanted) {
      try {
        const { data } = await octokit.issues.removeLabel({
          owner,
          repo,
          issue_number: args.number,
          name,
        });
        current = data;
      } catch (err) {
        if (err?.status !== 404) throw err;
        notOnPR.push(name);
      }
    }
    if (current === null) {
      const { data } = await octokit.issues.listLabelsOnIssue({
        owner,
        repo,
        issue_number: args.number,
        per_page: 100,
      });
      current = data;
    }
    return ok({
      number: args.number,
      labels: labelNames(current),
      ...(notOnPR.length ? { not_on_pr: notOnPR } : {}),
    });
  },

  pr_labels_set: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const wanted = nameList(args.labels);
    const labels = wanted.length ? await knownLabels(octokit, owner, repo, args.repo, wanted) : [];
    const { data } = await octokit.issues.setLabels({
      owner,
      repo,
      issue_number: args.number,
      labels,
    });
    return ok({ number: args.number, labels: labelNames(data) });
  },

  pr_assignees_add: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const wanted = nameList(args.assignees);
    if (wanted.length === 0) throw new Error(t("errors.list_required", { param: "assignees" }));
    const { data } = await octokit.issues.addAssignees({
      owner,
      repo,
      issue_number: args.number,
      assignees: wanted,
    });
    // GitHub can answer 201 while leaving out a login it could not assign; comparing is the only
    // way to tell.
    const now = (data.assignees ?? []).map((u) => u.login);
    const missing = wanted.filter((l) => !now.some((a) => a.toLowerCase() === l.toLowerCase()));
    return ok({
      number: args.number,
      assignees: now,
      ...(missing.length ? { not_assigned: missing } : {}),
    });
  },

  pr_assignees_remove: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const wanted = nameList(args.assignees);
    if (wanted.length === 0) throw new Error(t("errors.list_required", { param: "assignees" }));
    const { data } = await octokit.issues.removeAssignees({
      owner,
      repo,
      issue_number: args.number,
      assignees: wanted,
    });
    return ok({ number: args.number, assignees: (data.assignees ?? []).map((u) => u.login) });
  },

  pr_update: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    // `state` is left out on purpose: closing a PR is a decision for a person.
    const changes = {};
    for (const field of ["title", "body", "base"]) {
      if (args[field] !== undefined && args[field] !== null) changes[field] = args[field];
    }
    if (Object.keys(changes).length === 0) throw new Error(t("errors.update_nothing"));
    const { data } = await octokit.pulls.update({
      owner,
      repo,
      pull_number: args.number,
      ...changes,
    });
    return ok({
      number: data.number,
      title: data.title,
      base: data.base?.ref,
      draft: data.draft,
      html_url: data.html_url,
    });
  },

  pr_ready_for_review: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const draft = args.draft === true;
    // REST cannot change a PR's draft state; GraphQL can, by node id.
    const { data: pr } = await octokit.pulls.get({ owner, repo, pull_number: args.number });
    if (pr.draft === draft) {
      return ok({ number: pr.number, draft, unchanged: true, html_url: pr.html_url });
    }
    const res = await octokit.graphql(draft ? GQL_CONVERT_TO_DRAFT : GQL_MARK_READY, { id: pr.node_id });
    const after = (draft ? res?.convertPullRequestToDraft : res?.markPullRequestReadyForReview)?.pullRequest;
    return ok({ number: pr.number, draft: after?.isDraft ?? draft, html_url: pr.html_url });
  },

  pr_create: async (octokit, args, ctx = {}) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.pulls.create({
      owner,
      repo,
      head: args.head,
      base: args.base,
      title: args.title,
      body: args.body,
      draft: args.draft ?? false,
    });
    const link = await callOctopusRecordPR(ctx.taskId, args.repo, data.html_url, data.number);
    return ok({
      number: data.number,
      html_url: data.html_url,
      head: data.head.ref,
      base: data.base.ref,
      draft: data.draft,
      linked_to_task: link.recorded,
      ...(link.recorded ? {} : { link_warning: link.reason }),
    });
  },

  workflow_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.actions.listRepoWorkflows({ owner, repo });
    return ok(
      data.workflows.map((w) => ({
        id: w.id,
        name: w.name,
        path: w.path,
        state: w.state,
      })),
    );
  },

  run_list: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const params = {
      owner,
      repo,
      branch: args.branch,
      per_page: args.per_page ?? 30,
      page: args.page ?? 1,
    };
    // GitHub's `status` param takes both lifecycle and conclusion values and silently ignores a
    // `conclusion` param;
    // translating status:"failure" to completed+conclusion returned green runs. Pass it through.
    if (args.status) params.status = args.status;
    const list =
      args.workflow_id != null
        ? await octokit.actions.listWorkflowRuns({ ...params, workflow_id: args.workflow_id })
        : await octokit.actions.listWorkflowRunsForRepo(params);
    return ok(
      list.data.workflow_runs.map((r) => ({
        id: r.id,
        name: r.name,
        workflow_id: r.workflow_id,
        head_branch: r.head_branch,
        head_sha: r.head_sha,
        event: r.event,
        status: r.status,
        conclusion: r.conclusion,
        run_number: r.run_number,
        actor: r.actor?.login,
        html_url: r.html_url,
        created_at: r.created_at,
        updated_at: r.updated_at,
      })),
    );
  },

  run_jobs: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.actions.listJobsForWorkflowRun({
      owner,
      repo,
      run_id: args.run_id,
      filter: args.filter ?? "latest",
      per_page: args.per_page ?? 30,
      page: args.page ?? 1,
    });
    const FAILED = new Set(["failure", "timed_out", "cancelled"]);
    let jobs = data.jobs ?? [];
    if (args.failed_only) jobs = jobs.filter((j) => FAILED.has(j.conclusion));
    return ok({
      total_count: data.total_count,
      returned: jobs.length,
      jobs: jobs.map((j) => ({
        id: j.id,
        name: j.name,
        status: j.status,
        conclusion: j.conclusion,
        run_attempt: j.run_attempt,
        started_at: j.started_at,
        completed_at: j.completed_at,
        html_url: j.html_url,
        steps: (j.steps ?? []).map((s) => ({
          number: s.number,
          name: s.name,
          status: s.status,
          conclusion: s.conclusion,
        })),
      })),
    });
  },

  run_logs: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    let zipBuffer;
    if (args.job_id != null) {
      const r = await octokit.actions.downloadJobLogsForWorkflowRun({
        owner,
        repo,
        job_id: args.job_id,
      });
      return ok(clip(String(r.data ?? ""), LOG_CAP_BYTES, "logs"));
    }
    const r = await octokit.actions.downloadWorkflowRunLogs({
      owner,
      repo,
      run_id: args.run_id,
    });
    // A full run's logs come back as a zip and the image has no zip library; refuse it and point
    // the agent to job_id.
    zipBuffer = toBuffer(r.data);
    const sample = zipBuffer ? zipBuffer.subarray(0, 4).toString("hex") : "";
    if (sample.startsWith("504b")) {
      throw new Error(t("errors.logs_zip"));
    }
    return ok(
      clip(
        zipBuffer ? zipBuffer.toString("utf8") : String(r.data ?? ""),
        LOG_CAP_BYTES,
        "logs",
      ),
    );
  },

  run_dispatch: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    // Baseline before dispatching: run ids grow monotonically per repo, while created_at has second
    // resolution and uses GitHub's clock.
    const baseline = await newestDispatchRunID(octokit, owner, repo, args.workflow_id);

    await octokit.actions.createWorkflowDispatch({
      owner,
      repo,
      workflow_id: args.workflow_id,
      ref: args.ref,
      inputs: args.inputs,
    });

    const wanted = refName(args.ref);
    const deadline = Date.now() + DISPATCH_LOOKUP_MS;
    while (Date.now() < deadline) {
      await sleep(DISPATCH_POLL_MS);
      const { data } = await octokit.actions.listWorkflowRuns({
        owner,
        repo,
        workflow_id: args.workflow_id,
        event: "workflow_dispatch",
        per_page: 20,
      });
      const fresh = (data.workflow_runs ?? [])
        .filter((r) => r.id > baseline)
        .sort((a, b) => a.id - b.id);
      const run = fresh.find((r) => r.head_branch === wanted) ?? fresh[0];
      if (run) {
        return ok({
          dispatched: true,
          run_id: run.id,
          status: run.status,
          head_branch: run.head_branch,
          head_sha: run.head_sha,
          html_url: run.html_url,
          note: t("text.dispatch_watch"),
        });
      }
    }
    return ok({
      dispatched: true,
      run_id: null,
      note: t("text.dispatch_no_run", { seconds: Math.round(DISPATCH_LOOKUP_MS / 1000) }),
    });
  },

  run_view: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.actions.getWorkflowRun({
      owner,
      repo,
      run_id: args.run_id,
      exclude_pull_requests: true,
    });
    return ok(runSummary(data));
  },

  run_watch: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const budgetMs =
      Math.min(Math.max(Number(args.timeout_seconds ?? 120), 10), WATCH_MAX_SECONDS) * 1000;
    const everyMs = Math.min(Math.max(Number(args.poll_seconds ?? 10), 5), 60) * 1000;
    const stopOnWaiting = args.stop_on_waiting !== false;
    const started = Date.now();

    let run;
    for (;;) {
      ({ data: run } = await octokit.actions.getWorkflowRun({
        owner,
        repo,
        run_id: args.run_id,
        exclude_pull_requests: true,
      }));
      if (run.status === "completed") break;
      if (stopOnWaiting && run.status === "waiting") break;
      if (Date.now() - started + everyMs >= budgetMs) break;
      await sleep(everyMs);
    }

    const out = runSummary(run);
    out.waited_seconds = Math.round((Date.now() - started) / 1000);
    out.timed_out = run.status !== "completed" && !(stopOnWaiting && run.status === "waiting");
    if (run.status === "waiting") {
      out.pending_deployments = await pendingDeployments(octokit, owner, repo, args.run_id);
      out.note = t("text.run_waiting_review");
    }
    if (run.status === "completed" && run.conclusion !== "success") {
      const { data } = await octokit.actions.listJobsForWorkflowRun({
        owner,
        repo,
        run_id: args.run_id,
        filter: "latest",
        per_page: 100,
      });
      const FAILED = new Set(["failure", "timed_out", "cancelled"]);
      out.failed_jobs = (data.jobs ?? [])
        .filter((j) => FAILED.has(j.conclusion))
        .map((j) => ({
          id: j.id,
          name: j.name,
          conclusion: j.conclusion,
          failed_step: (j.steps ?? []).find((st) => FAILED.has(st.conclusion))?.name,
        }));
      if (out.failed_jobs.length) {
        out.note = t("text.run_read_failure");
      }
    }
    return ok(out);
  },

  run_cancel: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    await octokit.actions.cancelWorkflowRun({ owner, repo, run_id: args.run_id });
    return ok({
      ok: true,
      note: t("text.cancel_requested"),
    });
  },

  run_pending_deployments: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    return ok(await pendingDeployments(octokit, owner, repo, args.run_id));
  },

  run_artifacts: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data } = await octokit.actions.listWorkflowRunArtifacts({
      owner,
      repo,
      run_id: args.run_id,
      per_page: args.per_page ?? 30,
      page: args.page ?? 1,
    });
    return ok({
      total_count: data.total_count,
      artifacts: (data.artifacts ?? []).map((a) => ({
        id: a.id,
        name: a.name,
        size_in_bytes: a.size_in_bytes,
        expired: a.expired,
        created_at: a.created_at,
        expires_at: a.expires_at,
        archive_download_url: a.archive_download_url,
      })),
    });
  },

  workflow_view: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    const { data: wf } = await octokit.actions.getWorkflow({
      owner,
      repo,
      workflow_id: args.workflow_id,
    });
    const out = {
      id: wf.id,
      name: wf.name,
      path: wf.path,
      state: wf.state,
      html_url: wf.html_url,
    };
    if (args.include_source !== false) {
      try {
        const { data: file } = await octokit.repos.getContent({
          owner,
          repo,
          path: wf.path,
          ref: args.ref,
        });
        if (file && typeof file.content === "string") {
          out.source = clip(
            Buffer.from(file.content, "base64").toString("utf8"),
            DIFF_CAP_BYTES,
            "workflow",
          );
        }
      } catch (err) {
        out.source_error = err?.message ?? String(err);
      }
    }
    return ok(out);
  },

  deployment_approve: async (octokit, args, ctx = {}) => {
    const { owner, repo } = parseRepo(args.repo);
    const state = args.state ?? "approved";
    if (state !== "approved" && state !== "rejected") {
      throw new Error(t("errors.invalid_state", { state }));
    }
    if (chatSession(ctx)) {
      const path = heldPath(args, `runs/${encodeURIComponent(args.run_id)}/deployment-review`);
      const body = { state, environment_ids: args.environment_ids, comment: args.comment };
      return ok(await callOctopusHeld("deployment-review", path, body));
    }
    let environmentIDs = Array.isArray(args.environment_ids) ? args.environment_ids : [];
    if (environmentIDs.length === 0) {
      const pending = await pendingDeployments(octokit, owner, repo, args.run_id);
      environmentIDs = pending
        .filter((d) => d.current_user_can_approve && d.environment_id != null)
        .map((d) => d.environment_id);
      if (environmentIDs.length === 0) {
        throw new Error(t("errors.no_reviewable_environment"));
      }
    }
    const { data } = await octokit.actions.reviewPendingDeploymentsForRun({
      owner,
      repo,
      run_id: args.run_id,
      environment_ids: environmentIDs,
      state,
      comment: args.comment ?? "",
    });
    return ok({
      state,
      environment_ids: environmentIDs,
      deployments: (Array.isArray(data) ? data : []).map((d) => ({
        id: d.id,
        environment: d.environment?.name,
        created_at: d.created_at,
      })),
    });
  },

  pr_merge: async (octokit, args, ctx = {}) => {
    const { owner, repo } = parseRepo(args.repo);
    if (chatSession(ctx)) {
      const path = heldPath(args, `pulls/${encodeURIComponent(args.number)}/merge`);
      const body = {
        merge_method: args.merge_method,
        commit_title: args.commit_title,
        commit_message: args.commit_message,
        sha: args.sha,
      };
      return ok(await callOctopusHeld("pr-merge", path, body));
    }
    const { data } = await octokit.pulls.merge({
      owner,
      repo,
      pull_number: args.number,
      merge_method: args.merge_method,
      commit_title: args.commit_title,
      commit_message: args.commit_message,
      sha: args.sha,
    });
    return ok({ merged: data.merged, sha: data.sha, message: data.message });
  },

  run_rerun: async (octokit, args) => {
    const { owner, repo } = parseRepo(args.repo);
    if (args.failed_only) {
      await octokit.actions.reRunWorkflowFailedJobs({
        owner,
        repo,
        run_id: args.run_id,
      });
    } else {
      await octokit.actions.reRunWorkflow({
        owner,
        repo,
        run_id: args.run_id,
      });
    }
    return ok({ ok: true });
  },
};

// i18n-ignore GraphQL document
const GQL_THREAD = `query($id: ID!) { node(id: $id) { ... on PullRequestReviewThread { id isResolved pullRequest { number repository { nameWithOwner } } } } }`;
// i18n-ignore GraphQL document
const GQL_RESOLVE_THREAD = `mutation($id: ID!) { resolveReviewThread(input: { threadId: $id }) { thread { id isResolved } } }`;
// i18n-ignore GraphQL document
const GQL_UNRESOLVE_THREAD = `mutation($id: ID!) { unresolveReviewThread(input: { threadId: $id }) { thread { id isResolved } } }`;
// i18n-ignore GraphQL document
const GQL_MARK_READY = `mutation($id: ID!) { markPullRequestReadyForReview(input: { pullRequestId: $id }) { pullRequest { number isDraft } } }`;
// i18n-ignore GraphQL document
const GQL_CONVERT_TO_DRAFT = `mutation($id: ID!) { convertPullRequestToDraft(input: { pullRequestId: $id }) { pullRequest { number isDraft } } }`;

// A thread id names no repository, and an installation token reaches every repo the App is
// installed on:
// check the thread belongs to args.repo before acting.
async function setThreadResolved(octokit, args, resolved) {
  const { owner, repo } = parseRepo(args.repo);
  const threadId = String(args.thread_id ?? "").trim();
  if (!threadId) throw new Error(t("errors.thread_id_required"));
  const found = await octokit.graphql(GQL_THREAD, { id: threadId });
  const thread = found?.node;
  const actual = thread?.pullRequest?.repository?.nameWithOwner;
  if (!actual) throw new Error(t("errors.thread_not_found", { thread_id: threadId }));
  if (actual.toLowerCase() !== `${owner}/${repo}`.toLowerCase()) {
    throw new Error(t("errors.thread_other_repo", { thread_id: threadId, repo: `${owner}/${repo}`, actual }));
  }
  const number = thread.pullRequest.number;
  if (thread.isResolved === resolved) {
    return ok({ thread_id: threadId, number, resolved, unchanged: true });
  }
  const res = await octokit.graphql(resolved ? GQL_RESOLVE_THREAD : GQL_UNRESOLVE_THREAD, { id: threadId });
  const after = (resolved ? res?.resolveReviewThread : res?.unresolveReviewThread)?.thread;
  return ok({ thread_id: threadId, number, resolved: after?.isResolved ?? resolved });
}

function prNumberFromURL(url) {
  const m = /\/pulls\/(\d+)$/.exec(String(url ?? ""));
  return m ? Number(m[1]) : null;
}

function nameList(value) {
  if (value === undefined || value === null) return [];
  const raw = Array.isArray(value) ? value : [value];
  return [...new Set(raw.map((v) => String(v).trim()).filter(Boolean))];
}

function reviewerLists(args) {
  const reviewers = nameList(args.reviewers);
  const teams = nameList(args.team_reviewers);
  if (reviewers.length === 0 && teams.length === 0) throw new Error(t("errors.reviewers_required"));
  return { reviewers, team_reviewers: teams };
}

function requestedReviewers(pr) {
  return {
    number: pr.number,
    requested_reviewers: (pr.requested_reviewers ?? []).map((u) => u.login),
    requested_teams: (pr.requested_teams ?? []).map((team) => team.slug),
    html_url: pr.html_url,
  };
}

function labelNames(labels) {
  return (Array.isArray(labels) ? labels : []).map((l) => l.name);
}

// GitHub silently creates any label addLabels/setLabels names that the repo lacks, so validate
// names first (case-insensitive, as GitHub matches).
async function knownLabels(octokit, owner, repo, repoRef, wanted) {
  const all = await octokit.paginate(octokit.issues.listLabelsForRepo, { owner, repo, per_page: 100 });
  const byLower = new Map(all.map((l) => [String(l.name).toLowerCase(), l.name]));
  const unknown = wanted.filter((n) => !byLower.has(n.toLowerCase()));
  if (unknown.length) {
    const names = [...byLower.values()];
    const detail = unknown
      .map((label) => {
        const close = closeMatches(label, names);
        return close.length
          ? t("text.label_close_matches", { label, matches: close.join(", ") })
          : t("text.label_no_match", { label });
      })
      .join("; ");
    throw new Error(t("errors.unknown_labels", { repo: repoRef, labels: detail }));
  }
  return [...new Set(wanted.map((n) => byLower.get(n.toLowerCase())))];
}

function closeMatches(name, candidates) {
  const want = name.toLowerCase();
  return candidates
    .map((c) => {
      const have = c.toLowerCase();
      const d = have.includes(want) || want.includes(have) ? 0 : editDistance(want, have);
      return { c, d };
    })
    .filter(({ d }) => d <= 2)
    .sort((a, b) => a.d - b.d || a.c.localeCompare(b.c))
    .slice(0, 5)
    .map(({ c }) => c);
}

function editDistance(a, b) {
  let prev = Array.from({ length: b.length + 1 }, (_, j) => j);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) {
      cur[j] = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1));
    }
    prev = cur;
  }
  return prev[b.length];
}

const TASK_ONLY = new Set(["commit", "branch_push", "branch_status"]);
const HELD_IN_CHAT = new Set(["pr_merge", "deployment_approve"]);

function toolsForExplicit(names) {
  const allowed = new Set();
  for (const raw of names) {
    const name = String(raw).trim();
    if (!name) continue;
    if (SCOPE_IMPLIES[name]) {
      for (const tool of toolsForScope(name)) allowed.add(tool.name);
      continue;
    }
    allowed.add(name);
  }
  return TOOLS.filter((t) => allowed.has(t.name));
}

function buildServer({ taskId, projectId, scope, allowedTools, catalog = false, listOnly = false }) {
  const exposed = allowedTools ? toolsForExplicit(allowedTools) : toolsForScope(scope);
  const exposedNames = new Set(exposed.map((t) => t.name));
  // catalog=1 (internal/mcp/probe.go) lists every tool so deploy tools can be ticked; CallTool
  // still answers from exposedNames. Listing is not granting.
  const listed = catalog ? TOOLS : exposed;

  const server = new Server(
    { name: "thulu-github", version: "0.1.0" },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: listed.map(({ scope: _s, ...rest }) => rest),
  }));

  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    if (listOnly) {
      return { isError: true, content: [{ type: "text", text: t("errors.list_only_session") }] };
    }
    const name = req.params.name;
    if (!exposedNames.has(name)) {
      const known = TOOLS.find((t) => t.name === name);
      let reason;
      if (!known) {
        reason = t("errors.unknown_tool", { name });
      } else if (allowedTools) {
        reason = t("errors.tool_not_allowed", { name });
      } else if (known.scope === DEPLOY_SCOPE) {
        reason = t("errors.deploy_tool_not_granted", { name });
      } else {
        reason = t("errors.scope_required", { name, required: known.scope, scope });
      }
      return {
        isError: true,
        content: [{ type: "text", text: reason }],
      };
    }
    const handler = HANDLERS[name];
    if (!handler) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.no_handler", { name }) }],
      };
    }
    try {
      const args = req.params.arguments ?? {};
      const held = HELD_IN_CHAT.has(name) && chatSession({ taskId, projectId });
      let octokit = null;
      const ctx = { taskId, projectId, scope };
      if (!TASK_ONLY.has(name) && !held) {
        const token = await resolveToolToken({ taskId, projectId, repo: args.repo, tool: name }, ctx);
        octokit = new Octokit(
          GITHUB_API_URL ? { auth: token, baseUrl: GITHUB_API_URL } : { auth: token },
        );
      }
      return await handler(octokit, args, ctx);
    } catch (err) {
      const msg = err?.message ?? String(err);
      const text = err?.status
        ? t("errors.tool_failed_http", { tool: name, status: err.status, message: msg })
        : t("errors.tool_failed", { tool: name, message: msg });
      return {
        isError: true,
        content: [{ type: "text", text }],
      };
    }
  });

  return server;
}

// The dashboard stores the list "+"-separated (commas are re-split by the MCP_TOOLS/label
// encoding), mcp-config.sh sends commas,
// and a stale agent image may send a raw "+" that decodes as a space.
const TOOL_LIST_SEP = /[\s,+]+/;

const app = express();
// Do NOT use express.json() globally: SSEServerTransport.handlePostMessage reads the raw request
// stream and a body parser would consume it first.
const transports = new Map();

app.get("/health", (_req, res) => res.json({ ok: true }));

app.get("/sse", async (req, res) => {
  const taskId = String(req.query.task ?? "") || null;
  const projectId = String(req.query.project ?? "") || null;
  const toolsRaw = String(req.query.tools ?? "");
  const scopeRaw = "scope" in req.query ? String(req.query.scope) : "write";
  const catalog = String(req.query.catalog ?? "") === "1";

  let scope = "write";
  let allowedTools = null;
  if (toolsRaw) {
    allowedTools = toolsRaw.split(TOOL_LIST_SEP).map((t) => t.trim()).filter(Boolean);
  } else {
    scope = SCOPE_IMPLIES[scopeRaw] ? scopeRaw : "write";
    if (scope !== scopeRaw) {
      console.warn(tLog("log.unknown_scope", { scope: scopeRaw }));
    }
  }

  const key = keyFromRequest(req);
  if (!key && String(req.query.catalog ?? "") !== "1") {
    res.status(401).json({ error: t("errors.handshake_no_key") });
    return;
  }
  const listOnly = !key;
  const transport = new SSEServerTransport("/messages", res);
  transports.set(transport.sessionId, transport);
  sessionKeys.set(transport.sessionId, key);
  res.on("close", () => {
    transports.delete(transport.sessionId);
    sessionKeys.delete(transport.sessionId);
  });
  const server = buildServer({ taskId, projectId, scope, allowedTools, catalog, listOnly });
  await server.connect(transport);
});

app.post("/messages", async (req, res) => {
  const sessionId = String(req.query.sessionId ?? "");
  const transport = transports.get(sessionId);
  if (!transport) {
    res.status(404).send(t("errors.session_not_found"));
    return;
  }
  // The POST must carry the handshake's key again: knowing a session id is not owning it.
  const key = sessionKeys.get(sessionId) || "";
  if (key && !sameKey(key, keyFromRequest(req))) {
    res.status(401).json({ error: t("errors.message_key_mismatch") });
    return;
  }
  await callerKey.run(key, () => transport.handlePostMessage(req, res));
});

app.listen(PORT, () => {
  console.log(tLog("log.listening", { port: PORT }));
  console.log(tLog("log.octopus_api", { url: OCTOPUS_API }));
  console.log(tLog("log.sse_endpoint"));
  console.log(tLog("log.health_endpoint"));
});

const shutdown = (sig) => {
  console.log(tLog("log.shutting_down", { signal: sig }));
  process.exit(0);
};
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));
