import { t } from "../i18n";
import { isViewer, type Viewer } from "./activity";
import { md } from "./markdown";
import { parseMCPEntry } from "./mcp-entry";

export interface ChatAgent {
  type: string;
  name: string;
  description?: string;
  prompt_ready: boolean;
  llm_ready: boolean;
  mcp?: string[] | null;
}

export type AgentGap = "prompt" | "llm";

export function agentGaps(a: ChatAgent): AgentGap[] {
  const gaps: AgentGap[] = [];
  if (!a.prompt_ready) gaps.push("prompt");
  if (!a.llm_ready) gaps.push("llm");
  return gaps;
}

export function agentOptionLabel(a: ChatAgent): string {
  const gaps = agentGaps(a);
  if (gaps.length === 0) return a.name;
  if (gaps.length === 2) return t("chat.new.option_lacks_both", { agent: a.name });
  return gaps[0] === "prompt"
    ? t("chat.new.option_lacks_prompt", { agent: a.name })
    : t("chat.new.option_lacks_llm", { agent: a.name });
}

const BOARD_WRITE_TOOLS = ["task_create", "subtask_create", "task_update"];

export function canActOnBoards(entries: readonly string[] | null | undefined): boolean {
  for (const raw of entries ?? []) {
    const e = parseMCPEntry(raw.trim());
    if (e.server !== "octopus-api") continue;
    if (e.scope === "write" || e.scope === "admin") return true;
    if (e.tools?.some((tool) => BOARD_WRITE_TOOLS.includes(tool))) return true;
  }
  return false;
}

export function previewText(text: string): string {
  if (!text.trim()) return "";
  const tpl = document.createElement("template");
  tpl.innerHTML = md(text);
  tpl.content
    .querySelectorAll("p, li, h1, h2, h3, h4, h5, h6, blockquote, pre, tr, td, th, br")
    .forEach((el) => el.after(" "));
  return tpl.content.textContent.replace(/\s+/g, " ").trim();
}

export interface ChatAction {
  id: string;
  chat_id: string;
  agent: string;
  method?: string;
  path?: string;
  summary: string;
  status: "pending" | "confirmed" | "failed" | "canceled";
  result_code?: number;
  result_error?: string;
  decided_by?: string;
  created_at: string;
}

function deletesTask(a: ChatAction): boolean {
  return (
    (a.method ?? "").toUpperCase() === "DELETE" && /^\/tasks\/[^/?]+\/?(\?|$)/.test(a.path ?? "")
  );
}

export function actionStateText(
  a: ChatAction,
  decidedHere: boolean,
  viewer?: Viewer | null,
): string {
  const mine = decidedHere || isViewer(a.decided_by, viewer);
  switch (a.status) {
    case "confirmed":
      return mine || !a.decided_by
        ? t("chat.actions.confirmed_by_you")
        : t("chat.actions.confirmed_by", { person: a.decided_by });
    case "failed":
      if (a.result_code === 403) {
        return deletesTask(a) ? t("chat.actions.forbidden_task") : t("chat.actions.forbidden");
      }
      return a.result_error
        ? t("chat.actions.failed_with_error", {
            status: a.result_code ?? "",
            error: a.result_error,
          })
        : t("chat.actions.failed", { status: a.result_code ?? "" });
    case "canceled":
      if (!a.decided_by) return t("chat.actions.canceled_expired");
      return mine
        ? t("chat.actions.canceled_by_you")
        : t("chat.actions.canceled_by", { person: a.decided_by });
    case "pending":
      return "";
  }
}

export interface ChatMsgLike {
  role: string;
  metadata?: Record<string, string>;
}

export function isMentionBrief(m: ChatMsgLike): boolean {
  return m.role === "user" && m.metadata?.source === "task_mention";
}

export interface ToolCall {
  name: string;
  hint?: string;
}

export function replyTools(m: ChatMsgLike): { calls: ToolCall[]; omitted: number } {
  const raw = m.metadata?.tools;
  const omitted = Number(m.metadata?.tools_omitted) || 0;
  if (!raw) return { calls: [], omitted: 0 };
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return { calls: [], omitted: 0 };
    const calls = parsed
      .filter((c): c is ToolCall => !!c && typeof (c as ToolCall).name === "string")
      .map((c) => ({ name: c.name, hint: typeof c.hint === "string" ? c.hint : undefined }));
    return { calls, omitted };
  } catch {
    return { calls: [], omitted: 0 };
  }
}

export class ChatDrafts {
  private texts = new Map<string, string>();

  stash(id: string, text: string): void {
    if (text.trim()) this.texts.set(id, text);
    else this.texts.delete(id);
  }

  take(id: string): string {
    const text = this.texts.get(id) ?? "";
    this.texts.delete(id);
    return text;
  }

  restore(id: string, text: string): void {
    if (!this.texts.get(id)?.trim()) this.texts.set(id, text);
  }
}

export const PREVIEW_CHARS = 600;

export function chatTimeline<M extends { created_at: string }, A extends { created_at: string }>(
  msgs: readonly M[],
  actions: readonly A[],
): Array<{ msg: M } | { action: A }> {
  const at = (iso: string) => Date.parse(iso) || 0;
  const out: Array<{ msg: M } | { action: A }> = [];
  let i = 0;
  let j = 0;
  while (i < msgs.length || j < actions.length) {
    if (
      j >= actions.length ||
      (i < msgs.length && at(msgs[i].created_at) <= at(actions[j].created_at))
    )
      out.push({ msg: msgs[i++] });
    else out.push({ action: actions[j++] });
  }
  return out;
}
