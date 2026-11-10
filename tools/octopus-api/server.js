import { AsyncLocalStorage } from "node:async_hooks";
import { timingSafeEqual } from "node:crypto";

import express from "express";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import {
  ListToolsRequestSchema,
  CallToolRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";

import { t, tLog } from "./i18n.js";

const PORT = Number(process.env.PORT || 3000);
const OCTOPUS_API = process.env.OCTOPUS_API || "http://host.docker.internal:8585";

// Calls the daemon with the caller's run-scoped key from the SSE handshake
// (images/snippets/mcp-json.js),
// never a server credential, so a leaked key is one run's.
const callerKey = new AsyncLocalStorage();
const sessionKeys = new Map();

function keyFromRequest(req) {
  const direct = req.headers["x-api-key"];
  if (typeof direct === "string" && direct.trim()) return direct.trim();
  const bearer = req.headers.authorization;
  if (typeof bearer === "string" && bearer.startsWith("Bearer ")) {
    // i18n-ignore HTTP auth scheme, not a message
    return bearer.slice("Bearer ".length).trim();
  }
  return "";
}

// The session id travels in a URL and is not a credential: every POST /messages must repeat the
// handshake's key.
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

// "board" is deliberately outside the hierarchy: cross-board tools are granted only by naming them,
// never by a scope,
// because writing to the wrong board fails silently.
const SCOPE_IMPLIES = {
  read: new Set(["read"]),
  write: new Set(["read", "write"]),
};

const CROSS_BOARD_SCOPE = "board";

async function callApi(method, path, body, { rawBody = false, rawResponse = false } = {}) {
  const opts = { method, headers: { ...octopusAuthHeaders() } };
  if (rawBody && typeof body === "string") {
    opts.headers["Content-Type"] = "text/plain";
    opts.body = body;
  } else if (body !== undefined && body !== null) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(`${OCTOPUS_API}${path}`, opts);
  const text = await r.text();
  let parsed = text;
  if (!rawResponse) {
    try { parsed = JSON.parse(text); } catch { /* leave as string */ }
  }
  if (!r.ok) {
    const msg = typeof parsed === "string" ? parsed : (parsed?.error || JSON.stringify(parsed));
    throw new Error(t("errors.daemon_http", { method, path, status: r.status, msg }));
  }
  return parsed;
}

const enc = encodeURIComponent;

function envBase(projectID, envID) {
  return `/projects/${enc(projectID)}/environments/${enc(envID)}`;
}

// A run's key holds tasks:write but never projects:write, so proposals with a task must use the
// /tasks/... route;
// the project route is only for operator keys with no task.
function proposalTask(a, ctx) {
  return a.task_id || ctx?.taskId || "";
}

function testCasePath(taskID, caseRef) {
  return `/tasks/${enc(taskID)}/test-cases/${enc(caseRef)}`;
}

// Same limits the daemon enforces on POST /tasks/{id}/questions; change both together.

const ASK_LIMITS = {
  minQuestions: 1,
  maxQuestions: 4,
  maxOptions: 6,
  // The header renders as a chip over the question; past 12 it wraps.
  headerChars: 12,
};

// Counts code points, not UTF-16 units, to match what the person sees on the chip.
const chars = (s) => [...s].length;

const isText = (v) => typeof v === "string" && v.trim() !== "";

function askBody(a) {
  if (a.kind !== "question" && a.kind !== "blocker") {
    throw new Error(t("errors.ask_kind", { kind: String(a.kind) }));
  }
  if (a.blocking !== undefined && typeof a.blocking !== "boolean") {
    throw new Error(t("errors.ask_bool", { field: "blocking" }));
  }
  const blocking = a.blocking ?? true;
  if (!isText(a.context)) throw new Error(t("errors.ask_required", { field: "context" }));
  if (a.default !== undefined && typeof a.default !== "string") {
    throw new Error(t("errors.ask_string", { field: "default" }));
  }
  if (!blocking && !isText(a.default)) throw new Error(t("errors.ask_default_required"));
  if (a.needs_credential !== undefined && typeof a.needs_credential !== "boolean") {
    throw new Error(t("errors.ask_bool", { field: "needs_credential" }));
  }

  const qs = a.questions;
  if (!Array.isArray(qs) || qs.length < ASK_LIMITS.minQuestions || qs.length > ASK_LIMITS.maxQuestions) {
    throw new Error(
      t("errors.ask_questions_count", {
        min: ASK_LIMITS.minQuestions,
        max: ASK_LIMITS.maxQuestions,
        count: Array.isArray(qs) ? qs.length : 0,
      }),
    );
  }
  const questions = qs.map((q, i) => {
    const at = `questions[${i}]`;
    if (!q || typeof q !== "object") throw new Error(t("errors.ask_required", { field: at }));
    if (!isText(q.header)) throw new Error(t("errors.ask_required", { field: `${at}.header` }));
    if (chars(q.header) > ASK_LIMITS.headerChars) {
      throw new Error(
        t("errors.ask_header_long", { field: `${at}.header`, max: ASK_LIMITS.headerChars, header: q.header }),
      );
    }
    if (!isText(q.question)) throw new Error(t("errors.ask_required", { field: `${at}.question` }));
    for (const flag of ["multi_select", "allow_other"]) {
      if (q[flag] !== undefined && typeof q[flag] !== "boolean") {
        throw new Error(t("errors.ask_bool", { field: `${at}.${flag}` }));
      }
    }
    const opts = q.options ?? [];
    if (!Array.isArray(opts) || opts.length > ASK_LIMITS.maxOptions) {
      throw new Error(
        t("errors.ask_options_count", {
          field: `${at}.options`,
          max: ASK_LIMITS.maxOptions,
          count: Array.isArray(opts) ? opts.length : 0,
        }),
      );
    }
    const seen = new Set();
    const options = opts.map((o, j) => {
      const oat = `${at}.options[${j}]`;
      if (!o || typeof o !== "object" || !isText(o.label)) {
        throw new Error(t("errors.ask_required", { field: `${oat}.label` }));
      }
      if (o.description !== undefined && typeof o.description !== "string") {
        throw new Error(t("errors.ask_string", { field: `${oat}.description` }));
      }
      const label = o.label.trim();
      if (seen.has(label)) throw new Error(t("errors.ask_option_duplicate", { field: `${at}.options`, label }));
      seen.add(label);
      return { label, description: o.description ?? "" };
    });
    return {
      header: q.header.trim(),
      question: q.question,
      options,
      multi_select: q.multi_select ?? false,
      allow_other: q.allow_other ?? true,
    };
  });

  return {
    kind: a.kind,
    blocking,
    context: a.context,
    questions,
    default: a.default ?? "",
    needs_credential: a.needs_credential ?? false,
  };
}

function sessionTaskID(ctx, tool) {
  if (!ctx?.taskId) throw new Error(t("errors.run_tool_without_task", { tool }));
  return ctx.taskId;
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

async function currentTask(ctx) {
  const taskId = ctx?.taskId;
  if (!taskId) {
    throw new Error(t("errors.no_task_context"));
  }
  return callApi("GET", `/tasks/${enc(taskId)}`);
}

async function currentBoardID(ctx) {
  const task = await currentTask(ctx);
  if (!task?.board_id) {
    throw new Error(t("errors.task_without_board", { task: ctx.taskId }));
  }
  return task.board_id;
}

async function boardInfo(boardID) {
  const res = await callApi("GET", "/boards");
  const boards = res?.boards ?? [];
  const found = boards.find((b) => b.id === boardID);
  if (!found) {
    throw new Error(
      t("errors.unknown_board", {
        board: boardID,
        boards: boards.map((b) => b.id).join(", ") || t("labels.none"),
      }),
    );
  }
  return found;
}

async function boardStages(boardID) {
  const stages = await callApi("GET", `/boards/${enc(boardID)}/stages`);
  return Array.isArray(stages) ? stages : [];
}

// A task placed in a stage its board doesn't render becomes invisible.
async function assertStageOnBoard(boardID, stage) {
  if (!stage) return;
  const stages = await boardStages(boardID);
  if (!stages.some((s) => s.id === stage)) {
    throw new Error(
      t("errors.stage_not_on_board", {
        stage,
        board: boardID,
        columns: stages.map((s) => s.id).join(" → ") || t("labels.none"),
      }),
    );
  }
}

async function createTaskOnBoard(boardID, args) {
  const { title, body, project_ids, stage, stage_assignments, tags } = args;
  if (boardID) await assertStageOnBoard(boardID, stage);
  return callApi("POST", "/tasks", {
    title,
    body,
    project_ids,
    stage,
    stage_assignments,
    tags,
    ...(boardID ? { board_id: boardID } : {}),
  });
}

const TOOLS = [
  {
    scope: "read",
    name: "task_list",
    description: t("tools.task_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "task_get",
    description: t("tools.task_get.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.task_get.params.id") } },
      required: ["id"],
    },
  },

  {
    scope: "read",
    name: "board_current",
    description: t("tools.board_current.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: CROSS_BOARD_SCOPE,
    name: "boards_list",
    description: t("tools.boards_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        include_stages: {
          type: "boolean",
          description: t("tools.boards_list.params.include_stages"),
        },
      },
    },
  },
  {
    scope: CROSS_BOARD_SCOPE,
    name: "board_tasks_list",
    description: t("tools.board_tasks_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        board_id: { type: "string", description: t("tools.board_tasks_list.params.board_id") },
      },
      required: ["board_id"],
    },
  },

  {
    scope: "write",
    name: "task_create",
    description: t("tools.task_create.description"),
    inputSchema: {
      type: "object",
      properties: {
        title: { type: "string", description: t("tools.task_create.params.title") },
        body: { type: "string", description: t("tools.task_create.params.body") },
        project_ids: {
          type: "array",
          items: { type: "string" },
          description: t("tools.task_create.params.project_ids"),
        },
        stage: {
          type: "string",
          description: t("tools.task_create.params.stage"),
        },
        stage_assignments: {
          type: "object",
          additionalProperties: { type: "string" },
          description: t("tools.task_create.params.stage_assignments"),
        },
        tags: { type: "array", items: { type: "string" } },
      },
      required: ["title"],
    },
  },
  {
    scope: CROSS_BOARD_SCOPE,
    name: "task_create_on_board",
    description: t("tools.task_create_on_board.description"),
    inputSchema: {
      type: "object",
      properties: {
        board_id: { type: "string", description: t("tools.task_create_on_board.params.board_id") },
        title: { type: "string", description: t("tools.task_create_on_board.params.title") },
        body: { type: "string", description: t("tools.task_create_on_board.params.body") },
        project_ids: {
          type: "array",
          items: { type: "string" },
          description: t("tools.task_create_on_board.params.project_ids"),
        },
        stage: {
          type: "string",
          description: t("tools.task_create_on_board.params.stage"),
        },
        stage_assignments: {
          type: "object",
          additionalProperties: { type: "string" },
          description: t("tools.task_create_on_board.params.stage_assignments"),
        },
        tags: { type: "array", items: { type: "string" } },
      },
      required: ["board_id", "title"],
    },
  },
  {
    scope: CROSS_BOARD_SCOPE,
    name: "task_move_to_board",
    description: t("tools.task_move_to_board.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_move_to_board.params.id") },
        board_id: { type: "string", description: t("tools.task_move_to_board.params.board_id") },
        stage: {
          type: "string",
          description: t("tools.task_move_to_board.params.stage"),
        },
      },
      required: ["id", "board_id"],
    },
  },
  {
    scope: "write",
    name: "subtask_create",
    description: t("tools.subtask_create.description"),
    inputSchema: {
      type: "object",
      properties: {
        parent_id: { type: "string", description: t("tools.subtask_create.params.parent_id") },
        title: { type: "string", description: t("tools.subtask_create.params.title") },
        body: { type: "string", description: t("tools.subtask_create.params.body") },
        stage: {
          type: "string",
          description: t("tools.subtask_create.params.stage"),
        },
        stage_assignments: {
          type: "object",
          additionalProperties: { type: "string" },
          description: t("tools.subtask_create.params.stage_assignments"),
        },
        tags: { type: "array", items: { type: "string" } },
        priority: { type: "string" },
        depends_on: {
          type: "array",
          items: { type: "string" },
          description: t("tools.subtask_create.params.depends_on"),
        },
      },
      required: ["parent_id", "title"],
    },
  },
  {
    scope: "read",
    name: "task_sections_get",
    description: t("tools.task_sections_get.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.task_sections_get.params.id") } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_section_set",
    description: t("tools.task_section_set.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_section_set.params.id") },
        slug: {
          type: "string",
          description: t("tools.task_section_set.params.slug"),
        },
        content: {
          type: "string",
          description: t("tools.task_section_set.params.content"),
        },
        title: {
          type: "string",
          description: t("tools.task_section_set.params.title"),
        },
      },
      required: ["id", "slug", "content"],
    },
  },
  {
    scope: "write",
    name: "task_checklist_check",
    description: t("tools.task_checklist_check.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_checklist_check.params.id") },
        slug: {
          type: "string",
          description: t("tools.task_checklist_check.params.slug"),
        },
        index: {
          type: "integer",
          description: t("tools.task_checklist_check.params.index"),
        },
        checked: { type: "boolean" },
        note: { type: "string", description: t("tools.task_checklist_check.params.note") },
      },
      required: ["id", "slug", "index", "checked"],
    },
  },
  {
    scope: "write",
    name: "task_attach",
    description: t("tools.task_attach.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_attach.params.id") },
        filename: {
          type: "string",
          description: t("tools.task_attach.params.filename"),
        },
        content_base64: { type: "string", description: t("tools.task_attach.params.content_base64") },
        mime_type: {
          type: "string",
          description: t("tools.task_attach.params.mime_type"),
        },
        comment_id: {
          type: "string",
          description: t("tools.task_attach.params.comment_id"),
        },
      },
      required: ["id", "filename", "content_base64"],
    },
  },
  {
    scope: "read",
    name: "task_attachments",
    description: t("tools.task_attachments.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.task_attachments.params.id") } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_attachment_delete",
    description: t("tools.task_attachment_delete.description"),
    inputSchema: {
      type: "object",
      properties: { attachment_id: { type: "string", description: t("tools.task_attachment_delete.params.attachment_id") } },
      required: ["attachment_id"],
    },
  },
  {
    scope: "write",
    name: "task_update",
    description: t("tools.task_update.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        title: { type: "string" },
        body: { type: "string" },
        stage: { type: "string" },
        stage_assignments: {
          type: "object",
          additionalProperties: { type: "string" },
        },
        project_ids: { type: "array", items: { type: "string" } },
        tags: { type: "array", items: { type: "string" } },
        status: {
          type: "string",
          enum: ["idle", "running", "succeeded", "failed"],
        },
      },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_delete",
    description: t("tools.task_delete.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_clone",
    description: t("tools.task_clone.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_relaunch",
    description: t("tools.task_relaunch.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        turns: { type: "integer", description: t("tools.task_relaunch.params.turns") },
      },
      required: ["id"],
    },
  },
  {
    scope: "read",
    name: "task_list_dependencies",
    description: t("tools.task_list_dependencies.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.task_list_dependencies.params.id") } },
      required: ["id"],
    },
  },

  {
    scope: "write",
    name: "task_add_dependency",
    description: t("tools.task_add_dependency.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_add_dependency.params.id") },
        depends_on_task_id: { type: "string", description: t("tools.task_add_dependency.params.depends_on_task_id") },
        kind: { type: "string", enum: ["hard", "soft"], description: t("tools.task_add_dependency.params.kind") },
      },
      required: ["id", "depends_on_task_id"],
    },
  },
  {
    scope: "write",
    name: "task_remove_dependency",
    description: t("tools.task_remove_dependency.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        depends_on_task_id: { type: "string" },
      },
      required: ["id", "depends_on_task_id"],
    },
  },

  {
    scope: "write",
    name: "task_add_comment",
    description: t("tools.task_add_comment.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        body: { type: "string", description: t("tools.task_add_comment.params.body") },
        parent_id: {
          type: "string",
          description: t("tools.task_add_comment.params.parent_id"),
        },
        attachment_ids: {
          type: "array",
          items: { type: "string" },
          description: t("tools.task_add_comment.params.attachment_ids"),
        },
      },
      required: ["id"],
    },
  },

  {
    scope: "write",
    name: "task_add_report",
    description: t("tools.task_add_report.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_add_report.params.id") },
        title: { type: "string", description: t("tools.task_add_report.params.title") },
        body: { type: "string", description: t("tools.task_add_report.params.body") },
      },
      required: ["id", "body"],
    },
  },

  // Both act on the handshake's ?task=, never a model-supplied id: the daemon only takes a question
  // from the asking run.
  {
    scope: "write",
    name: "task_ask",
    description: t("tools.task_ask.description"),
    inputSchema: {
      type: "object",
      properties: {
        kind: {
          type: "string",
          enum: ["question", "blocker"],
          description: t("tools.task_ask.params.kind"),
        },
        blocking: { type: "boolean", default: true, description: t("tools.task_ask.params.blocking") },
        context: { type: "string", description: t("tools.task_ask.params.context") },
        questions: {
          type: "array",
          minItems: ASK_LIMITS.minQuestions,
          maxItems: ASK_LIMITS.maxQuestions,
          description: t("tools.task_ask.params.questions"),
          items: {
            type: "object",
            properties: {
              header: {
                type: "string",
                maxLength: ASK_LIMITS.headerChars,
                description: t("tools.task_ask.params.questions_header"),
              },
              question: { type: "string", description: t("tools.task_ask.params.questions_question") },
              options: {
                type: "array",
                maxItems: ASK_LIMITS.maxOptions,
                description: t("tools.task_ask.params.questions_options"),
                items: {
                  type: "object",
                  properties: {
                    label: { type: "string", description: t("tools.task_ask.params.options_label") },
                    description: { type: "string", description: t("tools.task_ask.params.options_description") },
                  },
                  required: ["label"],
                },
              },
              multi_select: {
                type: "boolean",
                default: false,
                description: t("tools.task_ask.params.questions_multi_select"),
              },
              allow_other: {
                type: "boolean",
                default: true,
                description: t("tools.task_ask.params.questions_allow_other"),
              },
            },
            required: ["header", "question"],
          },
        },
        default: { type: "string", description: t("tools.task_ask.params.default") },
        needs_credential: {
          type: "boolean",
          default: false,
          description: t("tools.task_ask.params.needs_credential"),
        },
      },
      required: ["kind", "context", "questions"],
    },
  },
  {
    scope: "write",
    name: "task_request_approval",
    description: t("tools.task_request_approval.description"),
    inputSchema: {
      type: "object",
      properties: {
        label: { type: "string", description: t("tools.task_request_approval.params.label") },
        reason: { type: "string", description: t("tools.task_request_approval.params.reason") },
        key: { type: "string", description: t("tools.task_request_approval.params.key") },
      },
      required: ["label", "reason"],
    },
  },

  {
    scope: "read",
    name: "task_test_cases",
    description: t("tools.task_test_cases.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.task_test_cases.params.id") } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "task_test_case_add",
    description: t("tools.task_test_case_add.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_test_case_add.params.id") },
        cases: {
          type: "array",
          minItems: 1,
          items: {
            type: "object",
            properties: {
              title: { type: "string", description: t("tools.task_test_case_add.params.cases_title") },
              key: {
                type: "string",
                description: t("tools.task_test_case_add.params.cases_key"),
              },
              description: { type: "string", description: t("tools.task_test_case_add.params.cases_description") },
              steps: { type: "string", description: t("tools.task_test_case_add.params.cases_steps") },
              expected: { type: "string", description: t("tools.task_test_case_add.params.cases_expected") },
              priority: { type: "string", enum: ["low", "medium", "high", "critical"] },
            },
            required: ["title"],
          },
        },
      },
      required: ["id", "cases"],
    },
  },
  {
    scope: "write",
    name: "task_test_case_update",
    description: t("tools.task_test_case_update.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_test_case_update.params.id") },
        case: { type: "string", description: t("tools.task_test_case_update.params.case") },
        title: { type: "string" },
        description: { type: "string" },
        steps: { type: "string" },
        expected: { type: "string" },
        priority: { type: "string", enum: ["low", "medium", "high", "critical"] },
        status: { type: "string", enum: ["pending", "pass", "fail", "blocked", "skipped"] },
        covered: { type: "boolean" },
        position: { type: "integer", description: t("tools.task_test_case_update.params.position") },
      },
      required: ["id", "case"],
    },
  },
  {
    scope: "write",
    name: "task_test_case_delete",
    description: t("tools.task_test_case_delete.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_test_case_delete.params.id") },
        case: { type: "string", description: t("tools.task_test_case_delete.params.case") },
      },
      required: ["id", "case"],
    },
  },
  {
    scope: "write",
    name: "task_test_case_run",
    description: t("tools.task_test_case_run.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string", description: t("tools.task_test_case_run.params.id") },
        case: { type: "string", description: t("tools.task_test_case_run.params.case") },
        verdict: { type: "string", enum: ["pass", "fail", "blocked"] },
        observed: { type: "string", description: t("tools.task_test_case_run.params.observed") },
        note: { type: "string", description: t("tools.task_test_case_run.params.note") },
        video_attachment_id: { type: "string", description: t("tools.task_test_case_run.params.video_attachment_id") },
        take_attachment_id: { type: "string", description: t("tools.task_test_case_run.params.take_attachment_id") },
        report_id: {
          type: "string",
          description: t("tools.task_test_case_run.params.report_id"),
        },
        comment_id: {
          type: "string",
          description: t("tools.task_test_case_run.params.comment_id"),
        },
        capture_attachment_ids: {
          type: "array",
          items: { type: "string" },
          description: t("tools.task_test_case_run.params.capture_attachment_ids"),
        },
      },
      required: ["id", "case", "verdict"],
    },
  },

  {
    scope: "read",
    name: "agent_list",
    description: t("tools.agent_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "agent_get",
    description: t("tools.agent_get.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.agent_get.params.id") } },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "agent_stop",
    description: t("tools.agent_stop.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string", description: t("tools.agent_stop.params.id") } },
      required: ["id"],
    },
  },

  {
    scope: "read",
    name: "agent_type_list",
    description: t("tools.agent_type_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "agent_type_get_settings",
    description: t("tools.agent_type_get_settings.description"),
    inputSchema: {
      type: "object",
      properties: { name: { type: "string", description: t("tools.agent_type_get_settings.params.name") } },
      required: ["name"],
    },
  },
  {
    scope: "read",
    name: "agent_type_list_prompts",
    description: t("tools.agent_type_list_prompts.description"),
    inputSchema: {
      type: "object",
      properties: { name: { type: "string" } },
      required: ["name"],
    },
  },
  {
    scope: "read",
    name: "agent_type_list_files",
    description: t("tools.agent_type_list_files.description"),
    inputSchema: {
      type: "object",
      properties: { name: { type: "string" } },
      required: ["name"],
    },
  },
  {
    scope: "read",
    name: "agent_type_read_file",
    description: t("tools.agent_type_read_file.description"),
    inputSchema: {
      type: "object",
      properties: {
        name: { type: "string" },
        path: { type: "string", description: t("tools.agent_type_read_file.params.path") },
      },
      required: ["name", "path"],
    },
  },

  {
    scope: "write",
    name: "agent_type_update_settings",
    description: t("tools.agent_type_update_settings.description"),
    inputSchema: {
      type: "object",
      properties: {
        name: { type: "string" },
        description: { type: "string" },
        model: { type: "string", description: t("tools.agent_type_update_settings.params.model") },
        effort: { type: "string", description: t("tools.agent_type_update_settings.params.effort") },
        valid_stages: { type: "array", items: { type: "string" } },
        works_without_repo: { type: "boolean" },
        turns_by_stage: { type: "object", additionalProperties: { type: "string" } },
        skills_by_stage: {
          type: "object",
          additionalProperties: { type: "array", items: { type: "string" } },
        },
        mcp_by_stage: {
          type: "object",
          additionalProperties: { type: "array", items: { type: "string" } },
        },
        auditor_stages: { type: "array", items: { type: "string" } },
        auditor_turns_by_stage: { type: "object", additionalProperties: { type: "string" } },
        auditor_model_by_stage: { type: "object", additionalProperties: { type: "string" } },
        auditor_engine_by_stage: { type: "object", additionalProperties: { type: "string" } },
        auditor_skills_by_stage: {
          type: "object",
          additionalProperties: { type: "array", items: { type: "string" } },
        },
        system_skills_off: {
          type: "object",
          additionalProperties: { type: "array", items: { type: "string" } },
          description: t("tools.agent_type_update_settings.params.system_skills_off"),
        },
      },
      required: ["name"],
    },
  },
  {
    scope: "write",
    name: "agent_type_write_file",
    description: t("tools.agent_type_write_file.description"),
    inputSchema: {
      type: "object",
      properties: {
        name: { type: "string" },
        path: { type: "string" },
        content: { type: "string", description: t("tools.agent_type_write_file.params.content") },
      },
      required: ["name", "path", "content"],
    },
  },
  {
    scope: "write",
    name: "agent_type_create",
    description: t("tools.agent_type_create.description"),
    inputSchema: {
      type: "object",
      properties: {
        name: { type: "string", description: t("tools.agent_type_create.params.name") },
        template: { type: "string", description: t("tools.agent_type_create.params.template") },
      },
      required: ["name"],
    },
  },
  {
    scope: "write",
    name: "agent_type_delete",
    description: t("tools.agent_type_delete.description"),
    inputSchema: {
      type: "object",
      properties: { name: { type: "string" } },
      required: ["name"],
    },
  },

  {
    scope: "read",
    name: "project_list",
    description: t("tools.project_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "project_get",
    description: t("tools.project_get.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" } },
      required: ["id"],
    },
  },

  {
    scope: "write",
    name: "project_create",
    description: t("tools.project_create.description"),
    inputSchema: {
      type: "object",
      properties: {
        name: { type: "string" },
        repo_url: { type: "string", description: t("tools.project_create.params.repo_url") },
        description: { type: "string" },
        default_branch: { type: "string", description: t("tools.project_create.params.default_branch") },
        stage_template: { type: "object", additionalProperties: { type: "string" } },
      },
      required: ["name", "repo_url"],
    },
  },
  {
    scope: "write",
    name: "project_update",
    description: t("tools.project_update.description"),
    inputSchema: {
      type: "object",
      properties: {
        id: { type: "string" },
        name: { type: "string" },
        default_branch: { type: "string" },
        description: { type: "string" },
        stage_template: { type: "object", additionalProperties: { type: "string" } },
      },
      required: ["id"],
    },
  },
  {
    scope: "write",
    name: "project_delete",
    description: t("tools.project_delete.description"),
    inputSchema: {
      type: "object",
      properties: { id: { type: "string" } },
      required: ["id"],
    },
  },

  {
    scope: "read",
    name: "skills_list",
    description: t("tools.skills_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "tools_list",
    description: t("tools.tools_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "stages_list",
    description: t("tools.stages_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "environment_list",
    description: t("tools.environment_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string" } },
      required: ["project_id"],
    },
  },
  {
    scope: "read",
    name: "environment_mapping_list",
    description: t("tools.environment_mapping_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string" }, environment_id: { type: "string" } },
      required: ["project_id", "environment_id"],
    },
  },
  {
    scope: "write",
    name: "environment_mapping_propose",
    description: t("tools.environment_mapping_propose.description"),
    inputSchema: {
      type: "object",
      properties: {
        project_id: { type: "string" },
        environment_id: { type: "string" },
        mappings: {
          type: "array",
          description: t("tools.environment_mapping_propose.params.mappings"),
          items: { type: "object" },
        },
        task_id: { type: "string", description: t("tools.environment_mapping_propose.params.task_id") },
        flag_id: { type: "string", description: t("tools.environment_mapping_propose.params.flag_id") },
      },
      required: ["project_id", "environment_id", "mappings"],
    },
  },

  {
    scope: "read",
    name: "custom_metric_list",
    description: t("tools.custom_metric_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string" }, environment_id: { type: "string" } },
      required: ["project_id", "environment_id"],
    },
  },
  {
    scope: "read",
    name: "custom_metric_series",
    description: t("tools.custom_metric_series.description"),
    inputSchema: {
      type: "object",
      properties: {
        project_id: { type: "string" },
        environment_id: { type: "string" },
        metric_id: { type: "string" },
        from: { type: "string", description: t("tools.custom_metric_series.params.from") },
        to: { type: "string", description: t("tools.custom_metric_series.params.to") },
      },
      required: ["project_id", "environment_id", "metric_id"],
    },
  },
  {
    scope: "read",
    name: "chart_types_list",
    description: t("tools.chart_types_list.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    scope: "read",
    name: "dashboard_list",
    description: t("tools.dashboard_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string" }, environment_id: { type: "string" } },
      required: ["project_id", "environment_id"],
    },
  },
  {
    scope: "read",
    name: "dashboard_get",
    description: t("tools.dashboard_get.description"),
    inputSchema: {
      type: "object",
      properties: {
        project_id: { type: "string" },
        environment_id: { type: "string" },
        dashboard_id: { type: "string" },
      },
      required: ["project_id", "environment_id", "dashboard_id"],
    },
  },
  {
    scope: "read",
    name: "dashboard_data",
    description: t("tools.dashboard_data.description"),
    inputSchema: {
      type: "object",
      properties: {
        project_id: { type: "string" },
        environment_id: { type: "string" },
        dashboard_id: { type: "string" },
        range_hours: { type: "integer", description: t("tools.dashboard_data.params.range_hours") },
      },
      required: ["project_id", "environment_id", "dashboard_id"],
    },
  },
  {
    scope: "write",
    name: "dashboard_propose",
    description: t("tools.dashboard_propose.description"),
    inputSchema: {
      type: "object",
      properties: {
        project_id: { type: "string" },
        environment_id: { type: "string" },
        name: { type: "string" },
        description: { type: "string" },
        panels: {
          type: "array",
          items: {
            type: "object",
            properties: {
              type: { type: "string", description: t("tools.dashboard_propose.params.panels_type") },
              title: { type: "string" },
              series: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    metric: { type: "string", description: t("tools.dashboard_propose.params.panels_series_metric") },
                    service: { type: "string" },
                    agg: { type: "string", description: t("tools.dashboard_propose.params.panels_series_agg") },
                    where: {
                      type: "object",
                      additionalProperties: { type: "string" },
                      description: t("tools.dashboard_propose.params.panels_series_where"),
                    },
                    label: { type: "string", description: t("tools.dashboard_propose.params.panels_series_label") },
                  },
                  required: ["metric", "service", "agg"],
                },
              },
              group_by: {
                type: "string",
                description: t("tools.dashboard_propose.params.panels_group_by"),
              },
              range_hours: { type: "integer", description: t("tools.dashboard_propose.params.panels_range_hours") },
              unit: { type: "string" },
              width: { type: "integer", description: t("tools.dashboard_propose.params.panels_width") },
            },
            required: ["type", "title", "series"],
          },
        },
        task_id: { type: "string", description: t("tools.dashboard_propose.params.task_id") },
      },
      required: ["project_id", "environment_id", "name", "panels"],
    },
  },
];

function toolsForScope(scope) {
  const allowed = SCOPE_IMPLIES[scope] || SCOPE_IMPLIES.read;
  return TOOLS.filter((tool) => allowed.has(tool.scope));
}

const HANDLERS = {
  environment_list: (a) => callApi("GET", `/projects/${enc(a.project_id)}/environments`).then(ok),

  environment_mapping_list: (a) =>
    callApi("GET", `/projects/${enc(a.project_id)}/environments/${enc(a.environment_id)}/mappings`).then(ok),

  environment_mapping_propose: async (a, ctx) => {
    if (!Array.isArray(a.mappings) || a.mappings.length === 0) {
      throw new Error(t("errors.mappings_empty"));
    }
    const taskId = proposalTask(a, ctx);
    const res = taskId
      ? await callApi(
          "POST",
          `/tasks/${enc(taskId)}/environments/${enc(a.environment_id)}/mapping-proposals`,
          { mappings: a.mappings, flag_id: a.flag_id ?? "" },
        )
      : await callApi("POST", `${envBase(a.project_id, a.environment_id)}/mappings`, {
          mappings: a.mappings,
          flag_id: a.flag_id ?? "",
        });
    return ok({
      proposed: Array.isArray(res) ? res.length : 0,
      mappings: res,
      note: t("text.mappings_proposed_note"),
    });
  },

  custom_metric_list: (a) =>
    callApi("GET", `${envBase(a.project_id, a.environment_id)}/custom-metrics`).then(ok),

  custom_metric_series: (a) => {
    const q = new URLSearchParams();
    if (a.from) q.set("from", a.from);
    if (a.to) q.set("to", a.to);
    const suffix = q.toString() ? `?${q}` : "";
    return callApi(
      "GET",
      `${envBase(a.project_id, a.environment_id)}/custom-metrics/${enc(a.metric_id)}/series${suffix}`,
    ).then(ok);
  },

  chart_types_list: () => callApi("GET", "/chart-types").then(ok),

  dashboard_list: (a) =>
    callApi("GET", `${envBase(a.project_id, a.environment_id)}/dashboards`).then(ok),

  dashboard_get: (a) =>
    callApi(
      "GET",
      `${envBase(a.project_id, a.environment_id)}/dashboards/${enc(a.dashboard_id)}`,
    ).then(ok),

  dashboard_data: (a) => {
    const suffix = a.range_hours ? `?range_hours=${enc(String(a.range_hours))}` : "";
    return callApi(
      "GET",
      `${envBase(a.project_id, a.environment_id)}/dashboards/${enc(a.dashboard_id)}/data${suffix}`,
    ).then(ok);
  },

  dashboard_propose: async (a, ctx) => {
    if (!Array.isArray(a.panels) || a.panels.length === 0) {
      throw new Error(t("errors.panels_empty"));
    }
    const body = { name: a.name, description: a.description ?? "", panels: a.panels };
    const taskId = proposalTask(a, ctx);
    const res = taskId
      ? await callApi(
          "POST",
          `/tasks/${enc(taskId)}/environments/${enc(a.environment_id)}/dashboard-proposals`,
          body,
        )
      : await callApi("POST", `${envBase(a.project_id, a.environment_id)}/dashboards`, body);
    return ok({
      dashboard: res,
      note: t("text.dashboard_proposed_note"),
    });
  },

  board_current: async (_args, ctx) => {
    const task = await currentTask(ctx);
    if (!task?.board_id) {
      throw new Error(t("errors.task_without_board", { task: ctx.taskId }));
    }
    const [info, stages] = await Promise.all([boardInfo(task.board_id), boardStages(task.board_id)]);
    return ok({
      board: {
        id: info.id,
        label: info.label,
        description: info.description || null,
        is_instance_default: !!info.default,
      },
      stages: stages.map((s) => ({ id: s.id, label: s.label })),
      task: { id: task.id, title: task.title, stage: task.stage },
      note: t("text.board_current_note"),
    });
  },

  boards_list: async ({ include_stages } = {}) => {
    const res = await callApi("GET", "/boards");
    if (!include_stages) return ok(res);
    const boards = res?.boards ?? [];
    // Unbounded Promise.all is fine: the daemon caps an instance at 64 boards.
    const withStages = await Promise.all(
      boards.map(async (b) => ({
        ...b,
        stages: (await boardStages(b.id)).map((s) => ({ id: s.id, label: s.label })),
      })),
    );
    return ok({ boards: withStages, default: res?.default ?? null });
  },

  board_tasks_list: async ({ board_id }) => {
    // GET /tasks?board=<unknown> answers [], indistinguishable from an empty board, so validate
    // first.
    const board = await boardInfo(board_id);
    return ok(await callApi("GET", `/tasks?board=${enc(board.id)}`));
  },

  task_list: async (_args, ctx) => {
    // Scoping to the run's board is what makes board_tasks_list a meaningful separate grant.
    if (!ctx?.taskId) return ok(await callApi("GET", "/tasks"));
    const boardID = await currentBoardID(ctx);
    return ok(await callApi("GET", `/tasks?board=${enc(boardID)}`));
  },
  task_get: async ({ id }) => ok(await callApi("GET", `/tasks/${enc(id)}`)),
  task_create: async (args, ctx) => {
    if (args.board_id !== undefined) {
      throw new Error(t("errors.task_create_board_id"));
    }
    const boardID = ctx?.taskId ? await currentBoardID(ctx) : "";
    return ok(await createTaskOnBoard(boardID, args));
  },
  task_create_on_board: async (args) => {
    const board = await boardInfo(args.board_id);
    return ok(await createTaskOnBoard(board.id, args));
  },
  task_move_to_board: async ({ id, board_id, stage }) => {
    const board = await boardInfo(board_id);
    const stages = await boardStages(board.id);
    if (!stages.length) throw new Error(t("errors.board_without_columns", { board: board.id }));
    const target = stage || stages[0].id;
    if (!stages.some((s) => s.id === target)) {
      throw new Error(
        t("errors.stage_not_on_board", {
          stage: target,
          board: board.id,
          columns: stages.map((s) => s.id).join(" → "),
        }),
      );
    }
    // Board and stage must change together, or the task lands in a column the destination doesn't
    // render.
    return ok(await callApi("PATCH", `/tasks/${enc(id)}`, { board_id: board.id, stage: target }));
  },
  subtask_create: async (args) => {
    const { parent_id, board_id, ...fields } = args;
    if (board_id !== undefined) {
      throw new Error(t("errors.subtask_create_board_id"));
    }
    return ok(await callApi("POST", `/tasks/${enc(parent_id)}/subtasks`, fields));
  },
  task_update: async (args) => {
    const { id, board_id, ...patch } = args;
    if (board_id !== undefined) {
      throw new Error(t("errors.task_update_board_id"));
    }
    return ok(await callApi("PATCH", `/tasks/${enc(id)}`, patch));
  },
  task_sections_get: async ({ id }) =>
    ok(await callApi("GET", `/tasks/${enc(id)}/sections`)),
  task_section_set: async ({ id, slug, content, title }) =>
    ok(await callApi("PUT", `/tasks/${enc(id)}/sections/${enc(slug)}`, { content, title })),
  task_checklist_check: async ({ id, slug, index, checked, note }) =>
    ok(await callApi("PATCH", `/tasks/${enc(id)}/sections/${enc(slug)}/items/${enc(index)}`, {
      checked,
      note,
    })),
  task_attach: async ({ id, filename, content_base64, mime_type, comment_id }) => {
    const bytes = Buffer.from(content_base64 || "", "base64");
    if (bytes.length === 0) throw new Error(t("errors.content_base64_invalid"));
    return ok(await callApi("POST", `/tasks/${enc(id)}/attachments`, {
      filename,
      mime_type: mime_type || "image/png",
      comment_id: comment_id || "",
      content_base64,
    }));
  },
  task_attachments: async ({ id }) => ok(await callApi("GET", `/tasks/${enc(id)}/attachments`)),
  task_attachment_delete: async ({ attachment_id }) =>
    ok(await callApi("DELETE", `/task-attachments/${enc(attachment_id)}`)),
  task_delete: async ({ id }) => ok(await callApi("DELETE", `/tasks/${enc(id)}`)),
  task_clone: async ({ id }) => ok(await callApi("POST", `/tasks/${enc(id)}/clone`)),
  task_relaunch: async ({ id, turns }) =>
    ok(await callApi("POST", `/tasks/${enc(id)}/relaunch`, turns ? { turns } : {})),
  task_add_comment: async ({ id, body, parent_id, attachment_ids }) =>
    ok(await callApi("POST", `/tasks/${enc(id)}/comments`, {
      attachment_ids: attachment_ids || [],
      body,
      parent_id: parent_id || undefined,
    })),
  task_add_report: async ({ id, title, body }) =>
    ok(await callApi("POST", `/tasks/${enc(id)}/reports`, { kind: "evidence", title: title || "", body })),

  // The daemon's instruction (end the turn for a blocking question) is what parks the run; it must
  // come first as plain text.
  task_ask: async (args, ctx) => {
    const taskID = sessionTaskID(ctx, "task_ask");
    const body = askBody(args);
    const res = await callApi("POST", `/tasks/${enc(taskID)}/questions`, body);
    const fallback = body.blocking ? t("text.ask_blocking_fallback") : t("text.ask_nonblocking_fallback");
    const instruction = res?.instruction || fallback;
    return ok(
      `${instruction}\n\n${t("text.ask_reference", {
        question: res?.question?.id ?? "?",
        comment: res?.comment_id ?? res?.question?.comment_id ?? "?",
      })}`,
    );
  },
  task_request_approval: async ({ label, reason, key }, ctx) => {
    const taskID = sessionTaskID(ctx, "task_request_approval");
    const res = await callApi("POST", `/tasks/${enc(taskID)}/approvals`, {
      label,
      reason,
      ...(key ? { key } : {}),
    });
    return ok({ approval: res, note: t("text.approval_requested_note") });
  },

  task_test_cases: async ({ id }) => ok(await callApi("GET", `/tasks/${enc(id)}/test-cases`)),
  task_test_case_add: async ({ id, cases }) => {
    if (!Array.isArray(cases) || cases.length === 0) {
      throw new Error(t("errors.cases_empty"));
    }
    return ok(await callApi("POST", `/tasks/${enc(id)}/test-cases`, { cases }));
  },
  task_test_case_update: async (args) => {
    const { id, case: key, ...patch } = args;
    return ok(await callApi("PATCH", testCasePath(id, key), patch));
  },
  task_test_case_delete: async ({ id, case: key }) =>
    ok(await callApi("DELETE", testCasePath(id, key))),
  task_test_case_run: async (args) => {
    const { id, case: key, ...run } = args;
    return ok(await callApi("POST", `${testCasePath(id, key)}/runs`, run));
  },

  task_list_dependencies: async ({ id }) =>
    ok(await callApi("GET", `/tasks/${enc(id)}/dependencies`)),
  task_add_dependency: async ({ id, depends_on_task_id, kind }) =>
    ok(await callApi("POST", `/tasks/${enc(id)}/dependencies`, {
      depends_on_task_id,
      kind: kind || "hard",
    })),
  task_remove_dependency: async ({ id, depends_on_task_id }) =>
    ok(await callApi("DELETE", `/tasks/${enc(id)}/dependencies/${enc(depends_on_task_id)}`)),

  agent_list: async () => ok(await callApi("GET", "/agents")),
  agent_get: async ({ id }) => ok(await callApi("GET", `/agents/${enc(id)}`)),
  agent_stop: async ({ id }) => ok(await callApi("POST", `/agents/${enc(id)}/stop`)),

  agent_type_list: async () => ok(await callApi("GET", "/types")),
  agent_type_get_settings: async ({ name }) =>
    ok(await callApi("GET", `/types/${enc(name)}/settings`)),
  agent_type_list_prompts: async ({ name }) =>
    ok(await callApi("GET", `/types/${enc(name)}/prompts`)),
  agent_type_list_files: async ({ name }) =>
    ok(await callApi("GET", `/types/${enc(name)}/files`)),
  agent_type_read_file: async ({ name, path }) =>
    ok(await callApi("GET", `/types/${enc(name)}/files/${path}`, undefined, { rawResponse: true })),
  agent_type_update_settings: async (args) => {
    const { name, ...patch } = args;
    return ok(await callApi("PUT", `/types/${enc(name)}/settings`, patch));
  },
  agent_type_write_file: async ({ name, path, content }) =>
    ok(await callApi("PUT", `/types/${enc(name)}/files/${path}`, content, { rawBody: true })),
  agent_type_create: async ({ name, template }) =>
    ok(await callApi("POST", "/types", template ? { name, template } : { name })),
  agent_type_delete: async ({ name }) =>
    ok(await callApi("DELETE", `/types/${enc(name)}`)),

  project_list: async () => ok(await callApi("GET", "/projects")),
  project_get: async ({ id }) => ok(await callApi("GET", `/projects/${enc(id)}`)),
  project_create: async (args) => ok(await callApi("POST", "/projects", args)),
  project_update: async (args) => {
    const { id, ...patch } = args;
    return ok(await callApi("PATCH", `/projects/${enc(id)}`, patch));
  },
  project_delete: async ({ id }) =>
    ok(await callApi("DELETE", `/projects/${enc(id)}`)),

  skills_list: async () => ok(await callApi("GET", "/skills")),
  tools_list: async () => ok(await callApi("GET", "/tools")),
  stages_list: async () => ok(await callApi("GET", "/stages")),
};

// Explicit lists may contain scope keywords ("octopus-api@write+boards_list"); thulu-github and
// thulu-cloud parse them the same way.
function toolsForExplicit(names) {
  const allowed = new Set();
  for (const name of names) {
    if (SCOPE_IMPLIES[name]) {
      for (const tool of toolsForScope(name)) allowed.add(tool.name);
      continue;
    }
    allowed.add(name);
  }
  return TOOLS.filter((tool) => allowed.has(tool.name));
}

// An explicit ?tools= list selects from the full surface and overrides scope, matching thulu-github
// and thulu-browser.
function buildServer({ scope, allowedTools = null, taskId = "", catalog = false, listOnly = false }) {
  const exposed = allowedTools ? toolsForExplicit(allowedTools) : toolsForScope(scope);
  const exposedNames = new Set(exposed.map((tool) => tool.name));
  // catalog=1 (internal/mcp/probe.go) widens only tools/list; CallTool still checks exposedNames.
  // Listing is not granting.
  const listed = catalog ? TOOLS : exposed;

  const server = new Server(
    { name: "octopus-api", version: "0.1.0" },
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
      const known = TOOLS.find((tool) => tool.name === name);
      let reason;
      if (!known) {
        reason = t("errors.unknown_tool", { name });
      } else if (allowedTools) {
        reason = t("errors.tool_not_allowed", { name });
      } else if (known.scope === CROSS_BOARD_SCOPE) {
        reason = t("errors.tool_cross_board", { name });
      } else {
        reason = t("errors.tool_scope_required", { name, required: known.scope, scope });
      }
      return { isError: true, content: [{ type: "text", text: reason }] };
    }
    const handler = HANDLERS[name];
    if (!handler) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.tool_without_handler", { name }) }],
      };
    }
    const args = req.params.arguments ?? {};
    const missing = (TOOLS.find((tool) => tool.name === name)?.inputSchema?.required ?? []).filter(
      (k) => args[k] === undefined || args[k] === null || args[k] === "",
    );
    if (missing.length) {
      return {
        isError: true,
        content: [
          {
            type: "text",
            text: t("errors.missing_arguments", { tool: name, args: missing.join(", ") }),
          },
        ],
      };
    }
    try {
      return await handler(args, { taskId });
    } catch (err) {
      return {
        isError: true,
        content: [
          { type: "text", text: t("errors.tool_failed", { tool: name, err: err.message || String(err) }) },
        ],
      };
    }
  });

  return server;
}

// The dashboard persists "+" (commas would be re-split by the MCP_TOOLS/label encoding),
// mcp-config.sh converts to commas,
// and stale agent images still send "+", which arrives decoded as a space.
const TOOL_LIST_SEP = /[\s,+]+/;

const app = express();
// Never apply express.json globally: SSEServerTransport reads the raw request body.
const transports = new Map();

app.get("/health", (_req, res) =>
  res.json({ ok: true, octopus_api: OCTOPUS_API, tools: TOOLS.length }),
);

app.get("/sse", async (req, res) => {
  const scopeRaw = String(req.query.scope ?? "read");
  const toolsRaw = String(req.query.tools ?? "");
  const scope = SCOPE_IMPLIES[scopeRaw] ? scopeRaw : "read";
  if (scope !== scopeRaw && !toolsRaw) {
    console.warn(tLog("log.unknown_scope", { scope: scopeRaw }));
  }
  const allowedTools = toolsRaw
    ? toolsRaw
        .split(TOOL_LIST_SEP)
        .map((s) => s.trim())
        .filter(Boolean)
    : null;
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
  // ?task= comes from the launcher (images/snippets/mcp-config.sh), never the model; it pins the
  // board for board-scoped tools.
  const taskId = String(req.query.task ?? "").trim();
  const catalog = String(req.query.catalog ?? "") === "1";
  const server = buildServer({ scope, allowedTools, taskId, catalog, listOnly });
  await server.connect(transport);
});

app.post("/messages", async (req, res) => {
  const sessionId = String(req.query.sessionId ?? "");
  const transport = transports.get(sessionId);
  if (!transport) {
    res.status(404).send(t("errors.session_not_found"));
    return;
  }
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
  const byScope = (s) => TOOLS.filter((tool) => tool.scope === s).length;
  console.log(
    tLog("log.tools", {
      total: TOOLS.length,
      read: byScope("read"),
      write: byScope("write"),
      cross_board: byScope(CROSS_BOARD_SCOPE),
    }),
  );
  console.log(tLog("log.sse_endpoint"));
  console.log(tLog("log.health"));
});

const shutdown = (sig) => {
  console.log(tLog("log.shutting_down", { signal: sig }));
  process.exit(0);
};
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));
