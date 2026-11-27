// One chat's feed and composer. The chat panel and a task's Chat tab mount the same controller:
// it loads the messages, follows a running turn line by line, keeps the queue, and sends.

import { t } from "../../i18n";
import { viewerFor, type Viewer } from "../activity";
import { ApiError, api } from "../api";
import { apiBase, authDisabled, sessionInfo } from "../auth";
import { chatMd } from "../chat-markdown";
import {
  actionStateText,
  chatTimeline,
  isMentionBrief,
  replyTools,
  type ChatAction,
} from "../chat-drawer";
import { CHAT_TOPIC, FRAME_RESET, chatTurnTopic, events, type Frame } from "../events";
import {
  chatCost,
  chatDuration,
  chatEffectsHTML,
  chatFailHTML,
  chatFootHTML,
  chatStepHTML,
  chatStepsHTML,
  chatStepsSummary,
  chatTokens,
  chatUserHTML,
  chatWhen,
  chatWhoHTML,
  esc,
  icoSvg,
  makeToast,
  parseChatEffects,
} from "../ui";
import { ChatComposer, type ComposerCommand, type QueuedChip, type SendMode } from "./composer";
import {
  agentReady,
  loadChatAgents,
  loadTaskPicks,
  onTasksLoaded,
  taskChip,
  upgradeTaskIDs,
  type ChatAgentInfo,
  type TaskPick,
} from "./data";
import { LiveTurn } from "./live-turn";

export type ChatStatus = "idle" | "running" | "needs_you" | "error";

export interface ChatInfo {
  id: string;
  workspace_id?: string;
  agent_type: string;
  task_id?: string;
  task_title?: string;
  task_board_id?: string;
  thread_id?: string;
  archived?: boolean;
  title: string;
  created_by?: string;
  delegated_by?: string;
  delegated_until?: string;
  status: ChatStatus;
  queue?: QueuedChip[];
  updated_at: string;
  last_message_preview?: string;
  last_message_at?: string;
}

export interface ChatMsg {
  id: string;
  role: string;
  author?: string;
  text: string;
  created_at: string;
  duration_ms?: number;
  input_tokens?: number;
  metadata?: Record<string, string>;
}

// What the chat bus sends: every event names its chat; the rest depends on the type.
export interface ChatEvent {
  chat_id?: string;
  message?: ChatMsg;
  turn_id?: string;
  queue?: QueuedChip[];
  action?: ChatAction;
  chat?: Partial<ChatInfo>;
}

export interface ConversationHost {
  // A draft has no chat yet: the first message creates it with createChat.
  chat: ChatInfo | null;
  agent: string;
  workspace?: string;
  commands: ComposerCommand[];
  // Shown over an empty feed: suggestions a click writes into the composer.
  suggestions?: () => Promise<string[]>;
  handoffFrom?: { id: string; title: string; agent: string };
  pickAgent?: boolean;
  createChat?: (agent: string, refs: string[]) => Promise<ChatInfo | null>;
  onCreated?: (chat: ChatInfo) => void;
  onAgent?: (type: string, started: boolean) => void;
  onCommand?: (id: string) => void;
  onTask?: (task: TaskPick) => void;
  onError?: (err: { code?: string; message: string; status: number }) => boolean;
  draft?: string;
  onDraft?: (text: string) => void;
}

const toast = makeToast(4000);
const API = apiBase();
const MSG_LIMIT = 200;

const viewerReady: Promise<Viewer | null> = Promise.all([sessionInfo(), authDisabled()])
  .then(([s, dev]) => viewerFor(s?.user, dev))
  .catch(() => null);

function md(text: string): string {
  // Attachment links are stored root-relative; behind a tunnel the daemon is not this page's
  // origin.
  return chatMd(text, { mediaBase: API });
}

function num(v: string | undefined): number {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

export class ChatConversation {
  chat: ChatInfo | null;
  private readonly host: ConversationHost;
  private readonly feed: HTMLElement;
  private readonly banner: HTMLElement;
  private readonly composer: ChatComposer;
  private agent: string;
  private msgs: ChatMsg[] = [];
  private live: { turnID: string; view: LiveTurn; off: () => void } | null = null;
  private pinned = true;
  private readonly decidedHere = new Set<string>();
  private viewer: Viewer | null = null;
  private readonly offs: Array<() => void> = [];
  private destroyed = false;
  private handoff: ConversationHost["handoffFrom"];

  constructor(parent: HTMLElement, host: ConversationHost) {
    this.host = host;
    this.chat = host.chat;
    this.agent = host.chat?.agent_type ?? host.agent;
    this.handoff = host.handoffFrom;
    this.feed = document.createElement("div");
    this.feed.className = "cx-feed";
    this.feed.setAttribute("role", "log");
    this.feed.setAttribute("aria-live", "polite");
    this.banner = document.createElement("p");
    this.banner.className = "cx-banner";
    this.banner.hidden = true;
    this.composer = new ChatComposer({
      agent: this.agent,
      workspace: host.workspace ?? host.chat?.workspace_id,
      pickAgent: host.pickAgent ?? true,
      started: false,
      commands: host.commands,
      placeholder: this.placeholder(),
      livePlaceholder: t("chat.composer.placeholder_live"),
      value: host.draft,
      onSubmit: (text, refs, mode) => this.send(text, refs, mode),
      onAgent: (type) => this.pickAgent(type),
      onCommand: (id) => host.onCommand?.(id),
      onTask: (task) => host.onTask?.(task),
      onUnqueue: (id) => void this.unqueue(id),
      onInput: (text) => host.onDraft?.(text),
    });
    parent.append(this.feed, this.banner);
    this.composer.mount(parent);

    this.feed.addEventListener("scroll", () => {
      this.pinned = this.feed.scrollHeight - this.feed.scrollTop - this.feed.clientHeight < 24;
    });
    this.feed.addEventListener("click", (e) => this.feedClick(e));
    this.feed.addEventListener(
      "toggle",
      (e) => {
        const d = e.target as HTMLDetailsElement;
        if (d.open && d.dataset.turn && !d.dataset.loaded) void this.loadSteps(d);
      },
      true,
    );
    this.offs.push(
      events.subscribe(
        CHAT_TOPIC,
        (f: Frame) => this.event(f.type, f.data as ChatEvent | undefined),
        {
          replay: false,
        },
      ),
      events.onReconnect(() => void this.load()),
      onTasksLoaded(() => upgradeTaskIDs(this.feed)),
    );
    void viewerReady.then((v) => (this.viewer = v));
    void loadTaskPicks();
  }

  destroy(): void {
    this.destroyed = true;
    this.stopLive();
    for (const off of this.offs) off();
    this.composer.destroy();
    this.feed.remove();
    this.banner.remove();
  }

  focus(): void {
    this.composer.focus();
  }

  private chatID(): string {
    return this.chat ? this.chat.id : "";
  }

  get composerValue(): string {
    return this.composer.value;
  }

  insertToken(ch: "#" | "@" | "/"): void {
    this.composer.insertToken(ch);
  }

  addRef(task: TaskPick): void {
    this.composer.addRef(task);
  }

  sendText(text: string): Promise<boolean> {
    return this.send(text, [], this.live ? "queue" : "");
  }

  setBanner(html: string): void {
    this.banner.innerHTML = html;
    this.banner.hidden = !html;
  }

  setDisabled(disabled: boolean): void {
    this.composer.setDisabled(disabled);
  }

  update(chat: ChatInfo): void {
    this.chat = chat;
    this.composer.setQueue(chat.queue ?? []);
  }

  private placeholder(): string {
    return this.chat || this.handoff
      ? t("chat.composer.placeholder_agent", { agent: this.agent })
      : t("chat.composer.placeholder_new");
  }

  // ── loading ──

  async load(): Promise<void> {
    if (!this.chat) return this.renderEmpty();
    const id = this.chat.id;
    let msgs: ChatMsg[];
    let actions: ChatAction[];
    try {
      [msgs, actions] = await Promise.all([
        api.get<ChatMsg[]>(`/chats/${id}/messages?limit=${MSG_LIMIT}`),
        api.get<ChatAction[]>(`/chats/${id}/actions`).catch(() => [] as ChatAction[]),
      ]);
    } catch {
      return;
    }
    if (this.destroyed || this.chatID() !== id) return;
    this.viewer = await viewerReady;
    this.msgs = msgs;
    this.stopLive();
    this.feed.innerHTML = "";
    if (!msgs.length && !actions.length) await this.renderEmpty();
    for (const item of chatTimeline(msgs, actions)) {
      if ("msg" in item) this.appendMsg(item.msg);
      else this.renderAction(item.action);
    }
    this.composer.setQueue(this.chat.queue ?? []);
    this.composer.setAgent(this.agent);
    this.restarted();
    if (this.chat.status === "running") {
      const last = [...msgs].reverse().find((m) => m.role === "user");
      if (last) this.startLive(last.id, Date.parse(last.created_at) || Date.now());
    }
    upgradeTaskIDs(this.feed);
    this.scrollToEnd();
  }

  private restarted(): void {
    this.composer.setStarted(this.msgs.length > 0);
    this.composer.setPlaceholder(this.placeholder());
  }

  private async renderEmpty(): Promise<void> {
    const agents = await loadChatAgents(this.host.workspace ?? this.chat?.workspace_id);
    if (this.destroyed || this.msgs.length) return;
    const a = agents.find((x) => x.type === this.agent);
    const desc = a?.description ?? "";
    const hand = this.handoff
      ? `<p class="cx-empty-text">${esc(t("chat.start.handoff", { title: this.handoff.title, agent: this.agent }))}</p>`
      : "";
    const sugg = this.chat || this.handoff ? [] : ((await this.host.suggestions?.()) ?? []);
    const gaps = a && !agentReady(a) ? this.gapHTML(a) : "";
    this.feed.innerHTML = `<div class="cx-empty"><h3 class="cx-empty-title">${esc(t("chat.start.title"))}</h3>${desc ? `<p class="cx-empty-text">${esc(desc)}</p>` : ""}${hand}${gaps}${sugg.length ? `<div class="cx-sugg">${sugg.map((s) => `<button type="button" data-cx-sugg>${s}</button>`).join("")}</div>` : ""}</div>`;
  }

  private gapHTML(a: ChatAgentInfo): string {
    const text = !a.prompt_ready
      ? t("chat.new.hint_no_prompt", { agent: a.name })
      : t("chat.new.hint_no_llm", { agent: a.name });
    return `<p class="cx-empty-text">${esc(text)}</p>`;
  }

  // ── rendering ──

  private scrollToEnd(force = false): void {
    if (!force && !this.pinned) return;
    this.feed.scrollTop = this.feed.scrollHeight;
    this.pinned = true;
  }

  private add(html: string): HTMLElement {
    this.feed.querySelector(".cx-empty")?.remove();
    const tpl = document.createElement("template");
    tpl.innerHTML = html.trim();
    const el = tpl.content.firstElementChild as HTMLElement;
    const anchor = this.live?.view.el;
    if (anchor?.parentElement === this.feed) this.feed.insertBefore(el, anchor);
    else this.feed.appendChild(el);
    el.querySelectorAll("img, video").forEach((m) =>
      m.addEventListener(m.tagName === "IMG" ? "load" : "loadedmetadata", () => this.scrollToEnd()),
    );
    return el;
  }

  private appendMsg(m: ChatMsg): void {
    if (m.id && this.feed.querySelector(`[data-id="${CSS.escape(m.id)}"]`)) return;
    if (m.role === "user") return this.appendUser(m);
    if (m.role === "assistant") return this.appendTurn(m);
    this.add(`<div class="cx-sep" data-id="${esc(m.id)}">${esc(m.text)}</div>`);
  }

  private appendUser(m: ChatMsg): void {
    const meta: Partial<Record<string, string>> = m.metadata ?? {};
    if (isMentionBrief(m) || meta.brief) {
      const lead =
        meta.brief === "evidence_recording"
          ? t("chat.brief.evidence_recording", { key: meta.case ?? "", env: meta.env ?? "" })
          : t("chat.brief.summary");
      this.add(
        `<div class="cx-steps-wrap" data-id="${esc(m.id)}">${chatStepsHTML({ summaryHTML: `<span>${esc(lead)}</span>`, bodyHTML: `<div class="cx-answer">${md(m.text)}</div>` })}</div>`,
      );
      return;
    }
    const note = meta.handoff_from ? esc(t("chat.user.handoff_note")) : "";
    this.add(chatUserHTML({ text: m.text, at: m.created_at, id: m.id, noteHTML: note }));
  }

  private appendTurn(m: ChatMsg): void {
    const meta: Partial<Record<string, string>> = m.metadata ?? {};
    const agent = this.chat?.agent_type ?? this.agent;
    const turnID = meta.turn_id ?? "";
    if (meta.error === "true") {
      const html = `<div class="cx-turn" data-id="${esc(m.id)}" data-turn-of="${esc(turnID)}">${chatWhoHTML(agent)}${chatFailHTML({ text: m.text, canceled: meta.canceled === "true", transcript: !!turnID })}</div>`;
      this.add(html);
      return;
    }
    const { calls, omitted } = replyTools(m);
    const steps = calls.length + omitted;
    const model = meta.model ?? "";
    const stepsHTML = steps
      ? chatStepsHTML({
          summaryHTML: chatStepsSummary({ ms: m.duration_ms ?? 0, steps, tools: calls }),
          bodyHTML:
            calls.map((c) => chatStepHTML(c)).join("") +
            (omitted
              ? `<div class="cx-steps-more">${esc(t("chat.steps.omitted", { count: omitted }))}</div>`
              : ""),
          turn: turnID,
        })
      : "";
    const ctx = num(meta.context_tokens);
    const foot = chatFootHTML({
      parts: [
        chatWhen(m.created_at),
        m.duration_ms ? chatDuration(m.duration_ms) : "",
        model,
        chatCost(num(meta.cost_usd)),
        ctx ? t("chat.foot.context", { tokens: chatTokens(ctx) }) : "",
      ],
      actions: true,
    });
    const fx = chatEffectsHTML(parseChatEffects(meta.effects), taskChip);
    this.add(
      `<div class="cx-turn" data-id="${esc(m.id)}" data-turn-of="${esc(turnID)}">${chatWhoHTML(agent)}${stepsHTML}<div class="cx-answer">${md(m.text)}</div>${fx}${foot}</div>`,
    );
  }

  private async loadSteps(d: HTMLDetailsElement): Promise<void> {
    d.dataset.loaded = "1";
    const chatID = this.chat?.id;
    const turn = d.dataset.turn;
    if (!chatID || !turn) return;
    try {
      const r = await api.get<{ lines: string[] }>(
        `/chats/${chatID}/turns/${encodeURIComponent(turn)}/transcript`,
      );
      const replay = LiveTurn.stepsBody(r.lines, md);
      const body = d.querySelector(".cx-steps-body");
      if (!replay.stepCount() || !body) return;
      body.innerHTML = replay.stepsHTML();
    } catch {
      // No transcript (a turn from before they were kept): the tool list stays.
    }
  }

  private renderAction(a: ChatAction): void {
    const sel = `[data-action-id="${CSS.escape(a.id)}"]`;
    const card = this.feed.querySelector<HTMLElement>(sel);
    const head = `<div class="cx-card-head">${icoSvg(a.status === "pending" ? "alert" : a.status === "confirmed" ? "check" : "x")}${esc(t("chat.actions.asks_confirmation", { agent: a.agent }))}</div>`;
    const what = `<div>${esc(a.summary)}</div>${a.method && a.path ? `<div class="cx-card-sub">${esc(a.method + " " + a.path)}</div>` : ""}`;
    const tail =
      a.status === "pending"
        ? `<div class="cx-card-row"><button type="button" class="cx-btn cx-btn-danger" data-cx-act="confirm">${esc(t("chat.actions.confirm"))}</button><button type="button" class="cx-btn" data-cx-act="cancel">${esc(t("common.buttons.cancel"))}</button></div>`
        : `<div class="cx-card-sub">${esc(actionStateText(a, this.decidedHere.has(a.id), this.viewer))}</div>`;
    const html = `<div class="cx-ask" role="group" data-action-id="${esc(a.id)}" data-status="${esc(a.status)}">${head}${what}${tail}</div>`;
    if (card) {
      const tpl = document.createElement("template");
      tpl.innerHTML = html;
      card.replaceWith(tpl.content);
      return;
    }
    this.add(html);
    this.scrollToEnd();
  }

  // ── live turn ──

  private startLive(turnID: string, startedAt = Date.now()): void {
    if (!this.chat) return;
    if (this.live?.turnID === turnID) return;
    this.stopLive();
    const chatID = this.chat.id;
    const view = new LiveTurn({
      agent: this.chat.agent_type,
      live: true,
      answer: true,
      startedAt,
      md,
      onStop: () => void this.cancel(),
      onChange: () => this.scrollToEnd(),
    });
    view.el.dataset.liveTurn = turnID;
    this.feed.querySelector(".cx-empty")?.remove();
    this.feed.appendChild(view.el);
    const off = events.subscribe(chatTurnTopic(chatID, turnID), (f: Frame) => {
      if (f.type === FRAME_RESET) return;
      if (f.type === "line") view.line(String(f.data ?? ""));
    });
    this.live = { turnID, view, off };
    this.composer.setLive(true);
    this.scrollToEnd();
  }

  private stopLive(): void {
    if (!this.live) return;
    this.live.off();
    this.live.view.destroy();
    this.live.view.el.remove();
    this.live = null;
    this.composer.setLive(false);
  }

  private async cancel(): Promise<void> {
    if (!this.chat) return;
    try {
      await api.post(`/chats/${this.chat.id}/turn/cancel`, {});
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t("chat.errors.offline_send"), "err");
    }
  }

  private event(type: string, ev: ChatEvent | undefined): void {
    if (!ev || !this.chat || ev.chat_id !== this.chat.id) return;
    switch (type) {
      case "message_added": {
        const m = ev.message;
        if (!m) return;
        if (m.role === "user") this.feed.querySelector(".cx-user[data-pending]")?.remove();
        if (m.role === "assistant" && this.live) this.stopLive();
        if (!this.msgs.some((x) => x.id === m.id)) this.msgs.push(m);
        this.appendMsg(m);
        this.restarted();
        upgradeTaskIDs(this.feed);
        this.scrollToEnd(m.role === "user");
        return;
      }
      case "turn_started":
        this.chat.status = "running";
        if (ev.turn_id) this.startLive(ev.turn_id);
        return;
      case "turn_finished":
      case "turn_failed":
        this.chat.status = "idle";
        // The answer usually lands first; if it does not, the live turn stays until it does.
        if (!this.live) this.composer.setLive(false);
        return;
      case "queue_changed":
        this.chat.queue = ev.queue ?? [];
        this.composer.setQueue(this.chat.queue ?? []);
        return;
      case "action_pending":
      case "action_decided":
        if (ev.action) this.renderAction(ev.action);
        return;
      case "chat_updated":
        if (ev.chat) Object.assign(this.chat, ev.chat);
        return;
    }
  }

  // ── actions on the feed ──

  private feedClick(e: Event): void {
    const el = (e.target as HTMLElement).closest<HTMLElement>("button, a");
    if (!el) return;
    if (el.hasAttribute("data-cx-sugg")) {
      const plain = el.cloneNode(true) as HTMLElement;
      for (const chip of plain.querySelectorAll<HTMLElement>("[data-ref]"))
        chip.replaceWith(chip.dataset.ref ?? "");
      this.composer.value = plain.textContent;
      this.composer.focus();
      return;
    }
    const act = el.dataset.cxAct;
    if (!act) return;
    const turnEl = el.closest<HTMLElement>(".cx-turn");
    if (act === "copy") {
      const m = this.msgs.find((x) => x.id === turnEl?.dataset.id);
      if (m)
        void navigator.clipboard
          .writeText(m.text)
          .then(() => toast(t("chat.foot.copied"), "ok", 1500));
      return;
    }
    if (act === "retry") return void this.retry(turnEl);
    if (act === "transcript") return void this.showTranscript(turnEl, el);
    if (act === "confirm" || act === "cancel") {
      const id = el.closest<HTMLElement>("[data-action-id]")?.dataset.actionId;
      if (id) void this.decide(id, act, el.closest<HTMLElement>(".cx-ask"));
    }
  }

  private userTextOf(turnEl: HTMLElement | null): string {
    const turnID = turnEl?.dataset.turnOf;
    let m = turnID ? this.msgs.find((x) => x.id === turnID) : undefined;
    if (!m) {
      const i = this.msgs.findIndex((x) => x.id === turnEl?.dataset.id);
      m = [...this.msgs.slice(0, i < 0 ? this.msgs.length : i)]
        .reverse()
        .find((x) => x.role === "user");
    }
    return m?.text ?? "";
  }

  private async retry(turnEl: HTMLElement | null): Promise<void> {
    const text = this.userTextOf(turnEl);
    if (!text) return;
    await this.send(text, [], this.live ? "queue" : "");
  }

  private async showTranscript(turnEl: HTMLElement | null, btn: HTMLElement): Promise<void> {
    const turn = turnEl?.dataset.turnOf;
    if (!this.chat || !turn) return;
    (btn as HTMLButtonElement).disabled = true;
    try {
      const r = await api.get<{ lines: string[] }>(
        `/chats/${this.chat.id}/turns/${encodeURIComponent(turn)}/transcript`,
      );
      const replay = LiveTurn.stepsBody(r.lines, md);
      const html = chatStepsHTML({
        summaryHTML: chatStepsSummary({ ms: 0, steps: replay.stepCount(), tools: [] }),
        bodyHTML:
          replay.stepsHTML() || `<div class="cx-steps-more">${esc(t("chat.fail.no_steps"))}</div>`,
        open: true,
      });
      turnEl.insertAdjacentHTML("beforeend", html);
      btn.remove();
    } catch {
      toast(t("chat.fail.no_transcript"), "info");
      (btn as HTMLButtonElement).disabled = false;
    }
  }

  private async decide(
    id: string,
    verb: "confirm" | "cancel",
    card: HTMLElement | null,
  ): Promise<void> {
    if (!this.chat) return;
    card?.querySelectorAll("button").forEach((b) => (b.disabled = true));
    // Mark before the request: the action_decided event can arrive before the response.
    this.decidedHere.add(id);
    try {
      const a = await api.post<ChatAction>(`/chats/${this.chat.id}/actions/${id}/${verb}`, {});
      this.renderAction(a);
    } catch (e) {
      this.decidedHere.delete(id);
      card?.querySelectorAll("button").forEach((b) => (b.disabled = false));
      toast(e instanceof ApiError ? e.message : t("chat.errors.offline_decide"), "err");
    }
  }

  // ── sending ──

  private pickAgent(type: string): void {
    const started = this.msgs.length > 0 || !!this.chat;
    if (started) {
      this.host.onAgent?.(type, true);
      return;
    }
    this.agent = type;
    this.composer.setAgent(type);
    this.host.onAgent?.(type, false);
    if (!this.msgs.length) void this.renderEmpty();
  }

  private async unqueue(id: string): Promise<void> {
    if (!this.chat) return;
    try {
      const r = await api.del<{ queue: QueuedChip[] }>(
        `/chats/${this.chat.id}/queue/${encodeURIComponent(id)}`,
      );
      this.chat.queue = r.queue;
      this.composer.setQueue(this.chat.queue);
    } catch {
      // It already left the queue: the turn took it.
    }
  }

  private async send(text: string, refs: string[], mode: SendMode): Promise<boolean> {
    if (!this.chat) {
      const created = await this.host.createChat?.(this.agent, refs);
      if (!created) return false;
      this.chat = created;
      this.host.onCreated?.(created);
    }
    const chat = this.chat;
    const pending =
      mode === "" || !this.live ? this.add(chatUserHTML({ text, pending: true })) : null;
    this.scrollToEnd(true);
    const body: Record<string, unknown> = { text };
    if (mode) body.mode = mode;
    if (refs.length) body.refs = refs;
    if (this.handoff && !this.msgs.length) body.handoff_from = this.handoff.id;
    try {
      const r = await api.post<{ message?: ChatMsg; queued?: QueuedChip }>(
        `/chats/${chat.id}/messages`,
        body,
      );
      const queued = r.queued;
      if (queued) {
        pending?.remove();
        chat.queue = [...(chat.queue ?? []).filter((q) => q.id !== queued.id), queued];
        this.composer.setQueue(chat.queue);
        return true;
      }
      const msg = r.message;
      if (msg) {
        pending?.remove();
        if (!this.msgs.some((x) => x.id === msg.id)) {
          this.msgs.push(msg);
          this.appendMsg(msg);
        }
        this.handoff = undefined;
        this.restarted();
        if (!this.live) this.startLive(msg.id, Date.parse(msg.created_at) || Date.now());
      }
      return true;
    } catch (e) {
      pending?.remove();
      const err =
        e instanceof ApiError
          ? {
              code: (e.body as { code?: string } | undefined)?.code,
              message: e.message,
              status: e.status,
            }
          : { message: t("chat.errors.offline_send"), status: 0 };
      if (err.status === 409 && mode === "") {
        // A turn started between the click and the request: wait in line instead.
        return this.send(text, refs, "queue");
      }
      if (!this.host.onError?.(err)) toast(err.message || t("chat.errors.offline_send"), "err");
      return false;
    }
  }
}
