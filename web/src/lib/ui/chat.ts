// The chat's pieces as HTML: a person's message, an agent's turn with its folded steps, what the
// turn changed in Thulu, the live line, the cards that wait on the person. styles/primitives/
// chat.css styles them; lib/chat/ drives them in the chat panel and in a task's Chat tab.

import { locale, t } from "../../i18n";
import { avatarHTML, avatarHue } from "./avatar";
import { esc } from "./dom";
import { icoSvg } from "./icon";

export type ChatToolKind = "cmd" | "thulu" | "mcp" | "read" | "edit" | "sub";

export interface ChatToolInfo {
  kind: ChatToolKind;
  label: string;
  text: string;
  mono: boolean;
  icon: string;
}

export interface ChatToolCall {
  name: string;
  hint?: string;
}

// The octopus-api tools a person reads as an action, by tool name.
const THULU_TOOL_KEYS: Record<string, string> = {
  task_get: "chat.tools.thulu.task_get",
  tasks_list: "chat.tools.thulu.tasks_list",
  board_tasks_list: "chat.tools.thulu.board_tasks_list",
  board_current: "chat.tools.thulu.board_current",
  task_create: "chat.tools.thulu.task_create",
  task_create_on_board: "chat.tools.thulu.task_create",
  subtask_create: "chat.tools.thulu.subtask_create",
  task_clone: "chat.tools.thulu.task_clone",
  task_update: "chat.tools.thulu.task_update",
  task_move_to_board: "chat.tools.thulu.task_move_to_board",
  tasks_move_batch: "chat.tools.thulu.task_move_to_board",
  task_add_dependency: "chat.tools.thulu.task_add_dependency",
  task_add_comment: "chat.tools.thulu.task_add_comment",
  task_attach: "chat.tools.thulu.task_attach",
  task_attachments: "chat.tools.thulu.task_attachments",
  task_sections_get: "chat.tools.thulu.task_sections_get",
  task_delete: "chat.tools.thulu.task_delete",
  task_relaunch: "chat.tools.thulu.task_relaunch",
  project_list: "chat.tools.thulu.project_list",
  skills_list: "chat.tools.thulu.skills_list",
  agent_type_get_settings: "chat.tools.thulu.agent_settings",
};

const THULU_SERVER = "octopus-api";

// mcp__<server>__<tool> is Claude's spelling; Codex and OpenCode write <server>.<tool> and
// <server>_<tool>.
function mcpParts(name: string): { server: string; tool: string } | null {
  const m = name.match(/^mcp__(.+?)__(.+)$/);
  if (m) return { server: m[1], tool: m[2] };
  for (const sep of [".", "_"]) {
    if (name.startsWith(THULU_SERVER + sep))
      return { server: THULU_SERVER, tool: name.slice(THULU_SERVER.length + 1) };
  }
  return null;
}

export function chatToolInfo(name: string, hint = ""): ChatToolInfo {
  const mcp = mcpParts(name);
  if (mcp) {
    if (mcp.server === THULU_SERVER) {
      const key = THULU_TOOL_KEYS[mcp.tool];
      return {
        kind: "thulu",
        label: t("chat.tools.thulu_label"),
        text: key ? t(key) : mcp.tool,
        mono: false,
        icon: "board",
      };
    }
    return { kind: "mcp", label: mcp.server, text: mcp.tool, mono: false, icon: "zap" };
  }
  if (/^(bash|shell)$/i.test(name))
    return {
      kind: "cmd",
      // i18n-ignore the tool's own name, the same in every language
      label: "Bash",
      text: hint.replace(/^cd\s+\S+\s*(;|&&)\s*/, ""),
      mono: true,
      icon: "terminal",
    };
  if (/^(read|grep|glob|ls|webfetch|websearch)$/i.test(name))
    return { kind: "read", label: name, text: hint, mono: true, icon: "file" };
  if (/^(edit|write|multiedit|notebookedit|apply_patch)$/i.test(name))
    return { kind: "edit", label: name, text: hint, mono: true, icon: "edit" };
  if (/^(task|agent)$/i.test(name))
    return { kind: "sub", label: t("chat.tools.subagent"), text: hint, mono: false, icon: "bot" };
  return { kind: "mcp", label: name, text: hint, mono: true, icon: "zap" };
}

const KIND_ORDER: ChatToolKind[] = ["cmd", "thulu", "mcp", "read", "edit", "sub"];
const KIND_KEYS: Record<ChatToolKind, string> = {
  cmd: "chat.steps.kinds.cmd",
  thulu: "chat.steps.kinds.thulu",
  mcp: "chat.steps.kinds.mcp",
  read: "chat.steps.kinds.read",
  edit: "chat.steps.kinds.edit",
  sub: "chat.steps.kinds.sub",
};

export function chatToolSummary(tools: readonly ChatToolCall[]): string {
  const count: Partial<Record<ChatToolKind, number>> = {};
  for (const call of tools) {
    const k = chatToolInfo(call.name, call.hint).kind;
    count[k] = (count[k] ?? 0) + 1;
  }
  return KIND_ORDER.filter((k) => count[k])
    .map((k) => t(KIND_KEYS[k], { count: count[k] ?? 0 }))
    .join(", ");
}

export function chatDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("chat.duration.seconds", { s });
  return t("chat.duration.minutes", { m: Math.floor(s / 60), s: String(s % 60).padStart(2, "0") });
}

export function chatClock(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
}

// A day and time a person reads at a glance: today's messages show the hour only.
export function chatWhen(iso: string, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  const time = d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", hour12: false });
  if (d.toDateString() === now.toDateString()) return time;
  return d.toLocaleDateString(locale, { day: "2-digit", month: "2-digit" }) + " " + time;
}

export function chatAvatarHTML(
  name: string,
  opts: { size?: "xs" | "sm"; pulse?: boolean } = {},
): string {
  return avatarHTML({
    kind: "agent",
    name: name || "?",
    letters: true,
    shape: "square",
    size: opts.size ?? "sm",
    hue: avatarHue(name),
    status: opts.pulse ? "ok" : undefined,
    pulse: opts.pulse,
  });
}

export interface ChatTaskRef {
  id: string;
  title?: string;
  board?: string;
  stage?: string;
  href?: string;
}

export function chatShortID(id: string): string {
  return id.slice(0, 6);
}

// A ticket key at the start of a title reads better than the title cut short.
export function chatShortTitle(title: string): string {
  const key = title.match(/^([A-Z][A-Z0-9]+-\d+)/);
  if (key) return key[1];
  return title.length > 34 ? title.slice(0, 33) + "…" : title;
}

export function chatTaskChipHTML(
  task: ChatTaskRef,
  opts: { short?: boolean; removable?: boolean; removeLabel?: string } = {},
): string {
  const title = task.title ?? task.id;
  const label = opts.short ? chatShortTitle(title) : title;
  const tip = [title, task.board, task.stage].filter(Boolean).join(" · ");
  const x = opts.removable
    ? `<button type="button" class="cx-chip-x" data-cx-remove aria-label="${esc(opts.removeLabel ?? t("chat.ctx.remove_task"))}">${icoSvg("x")}</button>`
    : "";
  const inner = `<span class="cx-chip-id">#${esc(chatShortID(task.id))}</span><span class="cx-chip-text">${esc(label)}</span>${x}`;
  if (task.href && !opts.removable)
    return `<a class="cx-chip" href="${esc(task.href)}" title="${esc(tip)}" data-task="${esc(task.id)}">${inner}</a>`;
  return `<span class="cx-chip" title="${esc(tip)}" data-task="${esc(task.id)}">${inner}</span>`;
}

export function chatInlineTaskHTML(task: ChatTaskRef): string {
  const title = task.title ?? task.id;
  return `<a class="cx-it" href="${esc(task.href ?? "#")}" title="${esc(title)}" data-task="${esc(task.id)}"><span class="cx-it-id">#${esc(chatShortID(task.id))}</span>${esc(chatShortTitle(title))}</a>`;
}

export function chatUserHTML(o: {
  text: string;
  at?: string;
  id?: string;
  pending?: boolean;
  noteHTML?: string;
}): string {
  const when = o.pending ? t("chat.user.sending") : chatWhen(o.at ?? "");
  const id = o.id ? ` data-id="${esc(o.id)}"` : "";
  const pending = o.pending ? " data-pending" : "";
  const note = o.noteHTML ? `<span class="cx-note">${o.noteHTML}</span>` : "";
  return `<div class="cx-user"${id}${pending}>${esc(o.text)}<span class="cx-when">${esc(when)}</span>${note}</div>`;
}

export function chatWhoHTML(agent: string, model?: string): string {
  const m = model ? `<span class="cx-who-model">${esc(model)}</span>` : "";
  return `<div class="cx-who">${chatAvatarHTML(agent)}${esc(agent)}${m}</div>`;
}

export type ChatStepState = "ok" | "err" | "run" | "say";

const STEP_ICON: Record<ChatStepState, string> = {
  ok: "check",
  err: "alert",
  run: "circle",
  say: "chevron-right",
};

export function chatStepHTML(call: ChatToolCall, state: ChatStepState = "ok", secs = 0): string {
  const i = chatToolInfo(call.name, call.hint);
  const plain = i.mono ? "" : " data-plain";
  const s = secs ? `${secs} s` : "";
  return `<div class="cx-step" data-state="${state}">${icoSvg(STEP_ICON[state])}<span class="cx-step-name">${esc(i.label)}</span><span class="cx-step-hint"${plain}>${esc(i.text)}</span><span class="cx-step-secs">${esc(s)}</span></div>`;
}

function sayCut(text: string): string {
  return text.length > 240 ? text.slice(0, 239) + "…" : text;
}

// The live turn appends what the agent streams, so it builds the node and sets the text as text:
// no markup made from the stream reaches the page.
export function chatSayStepEl(text: string): HTMLElement {
  const row = document.createElement("div");
  row.className = "cx-step";
  row.dataset.state = "say";
  row.innerHTML = icoSvg(STEP_ICON.say);
  const span = document.createElement("span");
  span.textContent = sayCut(text);
  row.append(span);
  return row;
}

export function chatStepsSummary(o: {
  live?: boolean;
  ms?: number;
  steps: number;
  tools?: readonly ChatToolCall[];
}): string {
  const steps = t("chat.steps.count", { count: o.steps });
  const head = o.live
    ? t("chat.steps.working", { steps })
    : t("chat.steps.worked", { duration: chatDuration(o.ms ?? 0), steps });
  const kinds = o.tools?.length ? chatToolSummary(o.tools) : "";
  return `<span>${esc(head)}</span>${kinds ? `<span class="cx-steps-kinds">· ${esc(kinds)}</span>` : ""}`;
}

export function chatStepsHTML(o: {
  summaryHTML: string;
  bodyHTML: string;
  open?: boolean;
  // The turn whose transcript fills the steps when they open.
  turn?: string;
}): string {
  const turn = o.turn ? ` data-turn="${esc(o.turn)}"` : "";
  return `<details class="cx-steps"${turn}${o.open ? " open" : ""}><summary>${icoSvg("chevron-right", { cls: "cx-steps-car" })}<span class="cx-steps-sum">${o.summaryHTML}</span></summary><div class="cx-steps-body">${o.bodyHTML}</div></details>`;
}

export interface ChatEffect {
  action: string;
  task_id?: string;
  other_task_id?: string;
  detail?: string;
  count?: number;
  error?: boolean;
}

const EFFECT_KEYS: Record<string, string> = {
  created: "chat.effects.created",
  updated: "chat.effects.updated",
  moved: "chat.effects.moved",
  commented: "chat.effects.commented",
  linked: "chat.effects.linked",
  attached: "chat.effects.attached",
  deleted: "chat.effects.deleted",
  relaunched: "chat.effects.relaunched",
};

export function parseChatEffects(raw: string | undefined): ChatEffect[] {
  if (!raw) return [];
  try {
    const v = JSON.parse(raw) as unknown;
    return Array.isArray(v)
      ? v.filter((e): e is ChatEffect => !!e && typeof (e as ChatEffect).action === "string")
      : [];
  } catch {
    return [];
  }
}

export function chatEffectsHTML(
  effects: readonly ChatEffect[],
  chip: (id: string) => string,
): string {
  if (!effects.length) return "";
  const rows = effects.map((e) => {
    const key = EFFECT_KEYS[e.action];
    if (!key) return "";
    let what = t(key);
    if (e.action === "moved" && e.count && e.count > 1)
      what = t("chat.effects.moved_many", { count: e.count });
    else if (e.action === "moved" && e.detail)
      what = t("chat.effects.moved_to", { stage: e.detail });
    const task = e.task_id ? chip(e.task_id) : "";
    const other = e.other_task_id
      ? `<span>${esc(t("chat.effects.with"))}</span>${chip(e.other_task_id)}`
      : "";
    if (e.error) {
      const why = e.detail
        ? t("chat.effects.failed_why", { detail: e.detail })
        : t("chat.effects.failed");
      return `<span class="cx-fx-row" data-error>${icoSvg("alert")}<span>${esc(what)}</span>${task}<span>${esc(why)}</span></span>`;
    }
    return `<span class="cx-fx-row"><span>${esc(what)}</span>${task}${other}</span>`;
  });
  return `<div class="cx-fx"><span class="cx-fx-label">${esc(t("chat.effects.label"))}</span>${rows.join("")}</div>`;
}

export function chatFootHTML(o: { parts: string[]; actions?: boolean }): string {
  const parts = o.parts.filter(Boolean).map((p) => `<span>${esc(p)}</span>`);
  const acts = o.actions
    ? `<span class="cx-foot-acts"><button type="button" class="cx-ibtn" data-cx-act="copy" aria-label="${esc(t("chat.foot.copy"))}" title="${esc(t("chat.foot.copy"))}">${icoSvg("copy")}</button><button type="button" class="cx-ibtn" data-cx-act="retry" aria-label="${esc(t("chat.foot.retry"))}" title="${esc(t("chat.foot.retry"))}">${icoSvg("refresh")}</button></span>`
    : "";
  return `<div class="cx-foot">${parts.join("")}${acts}</div>`;
}

export function chatFootEl(parts: string[]): HTMLElement {
  const foot = document.createElement("div");
  foot.className = "cx-foot";
  for (const p of parts.filter(Boolean)) {
    const span = document.createElement("span");
    span.textContent = p;
    foot.append(span);
  }
  return foot;
}

export function chatCost(usd: number): string {
  if (!usd) return "";
  return t("chat.foot.cost", {
    cost: usd.toLocaleString(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 }),
  });
}

export function chatTokens(n: number): string {
  if (n >= 1_000_000)
    return (n / 1_000_000).toLocaleString(locale, { maximumFractionDigits: 1 }) + " M";
  if (n >= 1000) return Math.round(n / 1000) + "k";
  return String(n);
}

export function chatWorkingHTML(o: { stop?: boolean } = {}): string {
  const stop = o.stop
    ? `<button type="button" class="cx-btn" data-cx-act="stop">${icoSvg("stop")}${esc(t("chat.live.stop"))}</button>`
    : "";
  return `<div class="cx-working" role="status"><span class="cx-spin"></span><span class="cx-working-what">${esc(t("chat.live.starting"))}</span><span class="cx-working-now"></span><span class="cx-working-clock">0:00</span>${stop}</div>`;
}

export function chatStoppedHTML(): string {
  return `<div class="cx-working" data-stopped>${icoSvg("stop")}<span>${esc(t("chat.live.stopped"))}</span></div>`;
}

export function chatFailHTML(o: {
  text: string;
  canceled?: boolean;
  transcript?: boolean;
}): string {
  const head = o.canceled ? t("chat.fail.canceled_head") : t("chat.fail.head");
  const transcript = o.transcript
    ? `<button type="button" class="cx-btn" data-cx-act="transcript">${esc(t("chat.fail.transcript"))}</button>`
    : "";
  return `<div class="cx-fail" role="group"><div class="cx-card-head">${icoSvg(o.canceled ? "stop" : "alert")}${esc(head)}</div><div>${esc(o.text)}</div><div class="cx-card-row"><button type="button" class="cx-btn" data-cx-act="retry">${icoSvg("refresh")}${esc(t("chat.fail.retry"))}</button>${transcript}</div></div>`;
}

export type ChatRowState = "running" | "needs_you" | "error" | "idle";

const STATE_KEYS: Record<ChatRowState, string> = {
  running: "chat.rows.running",
  needs_you: "chat.rows.needs_you",
  error: "chat.rows.error",
  idle: "chat.rows.idle",
};

export function chatStateHTML(state: ChatRowState, label?: string): string {
  return `<span class="cx-state" data-state="${state}">${esc(label ?? t(STATE_KEYS[state]))}</span>`;
}

export function chatKbdHTML(text: string): string {
  return `<span class="cx-kbd">${esc(text)}</span>`;
}
