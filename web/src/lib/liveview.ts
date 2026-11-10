import { t } from "../i18n";
import { codeBlockHTML, esc, icoSvg } from "./ui";

export interface LiveBlock {
  type?: string;
  text?: string;
  thinking?: string;
  id?: string;
  name?: string;
  input?: unknown;
  tool_use_id?: string;
  content?: unknown;
  is_error?: boolean;
}

export interface LiveEvent {
  type?: string;
  subtype?: string;
  model?: string;
  session_id?: string;
  message?: { content?: string | LiveBlock[] };
  text?: string;
  is_error?: boolean;
  num_turns?: number;
  stop_reason?: string;
  usage?: unknown;
  total_cost_usd?: number;
  cost_usd?: number;
  mode?: string;
  thread_id?: string;
  error?: unknown;
  item?: CodexItem;
  part?: OpenCodePart;
  [key: string]: unknown;
}

export interface CodexItem {
  type?: string;
  text?: string;
  summary?: string;
  command?: string;
  name?: string;
  arguments?: unknown;
  message?: string;
}

export interface OpenCodePart {
  text?: string;
  tool?: string;
  callID?: string;
  state?: { status?: string; input?: unknown; output?: unknown };
  reason?: string;
}

// Schemas pinned in internal/engine/codex/parse.go and internal/engine/opencode/parse.go; change
// together.
export function normalizeLiveEvent(ev: LiveEvent | null | undefined): LiveEvent | null {
  switch (ev?.type) {
    case "system":
    case "assistant":
    case "user":
    case "result":
      return ev;
    case "octopus.session":
      return { type: "session", mode: ev.mode ?? "" };
    case "thread.started":
      return { type: "system", subtype: "init", model: ev.model, session_id: ev.thread_id };
    case "turn.completed":
      // codex emits no result event; a `codex exec` is one turn, so turn completion ends the run.
      return {
        type: "result",
        is_error: !!ev.error,
        num_turns: 1,
        stop_reason: "",
        usage: ev.usage,
      };
    case "turn.failed":
      return {
        type: "result",
        is_error: true,
        num_turns: 1,
        stop_reason: "error",
        usage: ev.usage,
      };
    case "item.completed":
      return normalizeCodexItem(ev.item ?? {});
    case "text":
    case "reasoning":
    case "tool_use":
    case "step_finish":
    case "error":
      return normalizeOpenCodeEvent(ev);
    case undefined:
    default:
      return null;
  }
}

function normalizeCodexItem(it: CodexItem): LiveEvent | null {
  switch (it.type) {
    case "agent_message":
    case "message":
      return { type: "assistant", message: { content: [{ type: "text", text: it.text ?? "" }] } };
    case "reasoning": {
      const text = it.text ?? "";
      const summary = it.summary ?? "";
      return {
        type: "assistant",
        message: { content: [{ type: "thinking", thinking: text || summary }] },
      };
    }
    case "command_execution":
      return {
        type: "assistant",
        message: {
          content: [{ type: "tool_use", name: "shell", input: { command: it.command ?? "" } }],
        },
      };
    case "local_shell_call":
    case "function_call":
    case "tool_call": {
      const name = it.name ?? "";
      return {
        type: "assistant",
        message: {
          content: [{ type: "tool_use", name: name || (it.type ?? ""), input: it.arguments ?? {} }],
        },
      };
    }
    case "error": {
      // codex item errors are non-fatal notices (e.g. unknown model metadata), not run failures.
      const message = it.message ?? "";
      return { type: "warning", text: message || t("lib.liveview.codex_error") };
    }
    case undefined:
    default:
      return null;
  }
}

// Every OpenCode line carries sessionID; codex also emits type "error" without it.
// OpenCode prints no result line: a step_finish whose reason is not tool-calls ends the turn.
function normalizeOpenCodeEvent(ev: LiveEvent): LiveEvent | null {
  if (typeof ev.sessionID !== "string") return null;
  const part = ev.part ?? {};
  switch (ev.type) {
    case "text":
      return { type: "assistant", message: { content: [{ type: "text", text: part.text ?? "" }] } };
    case "reasoning":
      return {
        type: "assistant",
        message: { content: [{ type: "thinking", thinking: part.text ?? "" }] },
      };
    case "tool_use":
      return {
        type: "assistant",
        message: {
          content: [
            {
              type: "tool_use",
              id: part.callID,
              name: part.tool ?? "",
              input: part.state?.input ?? {},
            },
          ],
        },
      };
    case "step_finish":
      if (part.reason === "tool-calls") return null;
      return { type: "result", is_error: false, num_turns: 1, stop_reason: part.reason ?? "" };
    case "error":
      return { type: "result", is_error: true, num_turns: 1, stop_reason: "error" };
    case undefined:
    default:
      return null;
  }
}

const SESSION_MODE: Record<string, string> = {
  native: "lib.liveview.session.native",
  rehydrated: "lib.liveview.session.rehydrated",
  fresh: "lib.liveview.session.fresh",
};

export function sessionModeLabel(mode: string): string {
  return mode in SESSION_MODE ? t(SESSION_MODE[mode]) : "";
}

const TOOL_ICONS: Record<string, string> = {
  Read: "eye",
  Edit: "edit",
  Write: "edit",
  MultiEdit: "edit",
  NotebookEdit: "edit",
  Bash: "terminal",
  Grep: "search",
  Glob: "search",
  WebFetch: "link",
  WebSearch: "link",
  TodoWrite: "list",
  Task: "bot",
  Agent: "bot",
  read: "eye",
  edit: "edit",
  write: "edit",
  bash: "terminal",
  grep: "search",
  glob: "search",
  webfetch: "link",
  todowrite: "list",
  task: "bot",
};

export function liveToolIcon(name: string): string {
  return TOOL_ICONS[name] ?? "zap";
}

interface ToolInput {
  file_path?: string;
  filePath?: string;
  notebook_path?: string;
  command?: string;
  pattern?: string;
  url?: string;
  query?: string;
  description?: string;
  subagent_type?: string;
  todos?: unknown[];
}

export function liveToolPreview(name: string, input: unknown): string {
  if (!input) return "";
  const arg = input as ToolInput;
  switch (name) {
    case "Read":
    case "Edit":
    case "Write":
    case "MultiEdit":
      return (arg.file_path ?? "") || (arg.notebook_path ?? "");
    case "read":
    case "edit":
    case "write":
      return arg.filePath ?? "";
    case "Bash":
    case "bash":
      return (arg.command ?? "").replace(/\s+/g, " ").slice(0, 120);
    case "Grep":
    case "Glob":
    case "grep":
    case "glob":
      return arg.pattern ?? "";
    case "WebFetch":
    case "WebSearch":
    case "webfetch":
      return (arg.url ?? "") || (arg.query ?? "");
    case "Task":
    case "Agent":
    case "task":
      return (arg.description ?? "") || (arg.subagent_type ?? "");
    case "TodoWrite":
    case "todowrite": {
      const n = (arg.todos ?? []).length;
      return `${n} item${n === 1 ? "" : "s"}`;
    }
    default: {
      try {
        const s = JSON.stringify(input);
        return s.length > 100 ? s.slice(0, 100) + "…" : s;
      } catch {
        return "";
      }
    }
  }
}

export function toolInputJSON(input: unknown): string {
  try {
    return JSON.stringify(input ?? {}, null, 2);
  } catch {
    return String(input);
  }
}

export function toolCardHTML(block: LiveBlock): string {
  const name = block.name ?? "";
  return `
        <summary>
          <span class="lv-tool-icon">${icoSvg(liveToolIcon(name))}</span>
          <span class="lv-tool-name">${esc(name || t("lib.liveview.tool.fallback_name"))}</span>
          <span class="lv-tool-preview">${esc(liveToolPreview(name, block.input ?? {}))}</span>
          <span class="lv-tool-status pending">…</span>
        </summary>
        <div class="lv-tool-detail">
          <div class="lv-tool-label">${esc(t("lib.liveview.tool.input"))}</div>
          ${codeBlockHTML({ text: toolInputJSON(block.input), multiline: true, maxHeight: "sm" })}
          <div class="lv-tool-result"></div>
        </div>`;
}

export function toolResultText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content))
    return content
      .map((c: unknown) => (c as { text?: unknown } | null)?.text ?? JSON.stringify(c))
      .join("\n");
  if (content !== null && content !== undefined) return JSON.stringify(content, null, 2);
  return "";
}

const RESULT_CAP = 4000;

export function toolResultHTML(text: string, isError: boolean): string {
  const preview =
    text.length > RESULT_CAP
      ? text.slice(0, RESULT_CAP) + "\n" + t("lib.liveview.tool.truncated")
      : text;
  const label = isError ? t("lib.liveview.tool.result_error") : t("lib.liveview.tool.result");
  return `<div class="lv-tool-label">${esc(label)}</div>${codeBlockHTML({
    text: preview,
    multiline: true,
    maxHeight: "sm",
    tone: isError ? "danger" : "neutral",
  })}`;
}

export function prettyEvent(ev: LiveEvent): string {
  const kind = (ev.type ?? "") || "unknown";
  let cls = kind;
  let head = kind;
  let body = "";
  if (kind === "system" && ev.subtype === "init") {
    head = t("lib.liveview.event.init", { model: esc(ev.model ?? "") });
  } else if (kind === "octopus.session" && sessionModeLabel(ev.mode ?? "")) {
    head = sessionModeLabel(ev.mode ?? "");
  } else if (kind === "user" && ev.message?.content) {
    const c = ev.message.content;
    if (typeof c === "string") body = c;
    else
      body = c
        .map((b) =>
          b.type === "tool_result"
            ? `[tool_result]\n${typeof b.content === "string" ? b.content : JSON.stringify(b.content, null, 2)}`
            : (b.text ?? "") || JSON.stringify(b),
        )
        .join("\n");
    cls = Array.isArray(c) && c.some((b) => b.type === "tool_result") ? "tool_result" : "user";
    head = cls;
  } else if (kind === "assistant" && ev.message?.content) {
    const c = ev.message.content;
    const blocks: string[] = [];
    if (Array.isArray(c)) {
      for (const b of c) {
        if (b.type === "text") blocks.push(b.text ?? "");
        else if (b.type === "tool_use")
          blocks.push(`[tool_use: ${b.name}]\n${JSON.stringify(b.input ?? {}, null, 2)}`);
      }
    }
    body = blocks.join("\n\n");
    cls = Array.isArray(c) && c.some((b) => b.type === "tool_use") ? "tool_use" : "assistant";
    head = cls;
  } else if (kind === "result") {
    const cost = (ev.total_cost_usd ?? 0) || (ev.cost_usd ?? 0);
    head = t("lib.liveview.event.result", {
      turns: ev.num_turns ?? 0,
      cost: cost.toFixed(4),
      reason: ev.stop_reason ?? "",
    });
  } else {
    body = JSON.stringify(ev, null, 2);
  }
  return `<div class="ev ${esc(cls)}"><div class="ev-h">${esc(head)}</div>${body ? `<div class="ev-b">${esc(body)}</div>` : ""}</div>`;
}
