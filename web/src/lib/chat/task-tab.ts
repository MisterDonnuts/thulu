// A task's Chat tab. A bar on top switches between the task's current run (the agent working on
// it, live), its earlier runs, the person's chats about the task, and a new chat with any agent.
// Runs read their transcript line by line; chats are the same ChatConversation the panel mounts.

import { locale, t } from "../../i18n";
import { ApiError, api } from "../api";
import { apiBase } from "../auth";
import { chatMd } from "../chat-markdown";
import { CHAT_TOPIC, FRAME_END, FRAME_ERROR, FRAME_RESET, events, logsTopic } from "../events";
import {
  chatAvatarHTML,
  chatStepsHTML,
  chatUserHTML,
  confirmDialog,
  emptyStateHTML,
  esc,
  icoSvg,
  makeToast,
} from "../ui";
import { ChatComposer, type SendMode } from "./composer";
import { ChatConversation, type ChatInfo } from "./conversation";
import { agentReady, lastAgent, loadChatAgents, rememberAgent } from "./data";
import { LiveTurn } from "./live-turn";

export interface TaskChatHost {
  task: { id: string; title: string; board_id?: string; workspace_id?: string };
  stageLabel: (stage: string) => string;
  // The task changed (a message went out, a run stopped): the drawer reloads it.
  refresh: () => Promise<void> | void;
  draft: string;
  onDraft: (text: string) => void;
}

interface ConvMessage {
  id: string;
  role: string;
  body: string;
  agent?: string;
  chat_id?: string;
  delivery?: string;
  created_at: string;
}

interface ConvItem {
  kind: "message" | "run";
  at: string;
  message?: ConvMessage;
  run_id?: string;
  agent?: string;
  stage?: string;
  turns?: number;
  live?: boolean;
}

interface AgentRow {
  id: string;
  task_id?: string;
  status?: string;
}

type View = { kind: "current" } | { kind: "past" } | { kind: "chat"; id: string } | { kind: "new" };

const toast = makeToast(4000);
const API = apiBase();
const md = (text: string) => chatMd(text, { mediaBase: API });

export class TaskChatTab {
  private readonly pane: HTMLElement;
  private readonly host: TaskChatHost;
  private readonly bar: HTMLElement;
  private readonly body: HTMLElement;
  private items: ConvItem[] = [];
  private chats: ChatInfo[] = [];
  private view: View = { kind: "current" };
  private conv: ChatConversation | null = null;
  private composer: ChatComposer | null = null;
  private feed: HTMLElement | null = null;
  private offs: Array<() => void> = [];
  private readonly offChat: () => void;
  private destroyed = false;

  constructor(pane: HTMLElement, host: TaskChatHost) {
    this.pane = pane;
    this.host = host;
    pane.innerHTML = `<div class="cx-pane"><div class="cx-inner"><div class="cx-convbar" role="toolbar" aria-label="${esc(t("tasks.chat.convbar.aria"))}"></div><div class="cx-inner cx-tab-body"></div></div></div>`;
    this.bar = pane.querySelector(".cx-convbar") as HTMLElement;
    this.body = pane.querySelector(".cx-tab-body") as HTMLElement;
    this.bar.addEventListener("click", (e) => this.barClick(e));
    this.body.addEventListener("click", (e) => this.bodyClick(e));
    this.offChat = events.subscribe(
      CHAT_TOPIC,
      (f) => {
        const ev = f.data as { chat?: ChatInfo } | undefined;
        if (f.type === "chat_created" && ev?.chat?.task_id === host.task.id) void this.loadChats();
      },
      { replay: false },
    );
    void this.load();
  }

  destroy(): void {
    this.destroyed = true;
    this.unmount();
    this.offChat();
    this.pane.innerHTML = "";
  }

  // The task's run started or ended: the current view follows the new state.
  async reload(): Promise<void> {
    await this.load();
  }

  private async load(): Promise<void> {
    let data: { items?: ConvItem[] };
    try {
      [data] = await Promise.all([
        api.get<{ items?: ConvItem[] }>(`/tasks/${this.host.task.id}/conversation`),
        this.loadChats(false),
      ]);
    } catch {
      this.body.innerHTML = emptyStateHTML({
        size: "sm",
        tone: "danger",
        body: t("tasks.chat.load_failed"),
      });
      return;
    }
    if (this.destroyed) return;
    this.items = data.items ?? [];
    if (
      this.view.kind === "chat" &&
      !this.chats.some((c) => this.view.kind === "chat" && c.id === this.view.id)
    )
      this.view = { kind: "current" };
    if (this.view.kind === "current" && !this.runs().length && this.chats.length)
      this.view = { kind: "chat", id: this.chats[0].id };
    this.renderBar();
    this.show();
  }

  private async loadChats(render = true): Promise<void> {
    try {
      const all = await api.get<ChatInfo[]>("/chats");
      this.chats = all.filter((c) => c.task_id === this.host.task.id && !c.archived);
    } catch {
      this.chats = [];
    }
    if (render && !this.destroyed) this.renderBar();
  }

  private runs(): ConvItem[] {
    return this.items.filter((it) => it.kind === "run" && it.run_id);
  }

  // ── the bar ──

  private renderBar(): void {
    const runs = this.runs();
    const last = runs.at(-1);
    const v = this.view;
    const parts: string[] = [];
    if (last) {
      const icon = last.live
        ? `<span class="cx-spin cx-spin-sm"></span>`
        : chatAvatarHTML(last.agent ?? "", { size: "xs" });
      const label = [last.stage ? this.host.stageLabel(last.stage) : "", last.agent ?? ""]
        .filter(Boolean)
        .join(" · ");
      parts.push(
        `<button type="button" class="cx-cv" data-view="current" aria-pressed="${v.kind === "current"}">${icon}${esc(label)}</button>`,
      );
    }
    for (const c of this.chats) {
      parts.push(
        `<button type="button" class="cx-cv" data-view="chat" data-id="${esc(c.id)}" aria-pressed="${v.kind === "chat" && v.id === c.id}">${chatAvatarHTML(c.agent_type, { size: "xs" })}${esc(t("tasks.chat.convbar.chat", { agent: c.agent_type }))}</button>`,
      );
    }
    if (runs.length > 1)
      parts.push(
        `<button type="button" class="cx-cv" data-plain data-view="past" aria-pressed="${v.kind === "past"}">${esc(t("tasks.chat.convbar.past", { count: runs.length - 1 }))}</button>`,
      );
    parts.push(
      `<button type="button" class="cx-cv" data-plain data-view="new" aria-pressed="${v.kind === "new"}">${icoSvg("plus")}${esc(t("tasks.chat.convbar.new"))}</button>`,
    );
    this.bar.innerHTML = parts.join("");
  }

  private barClick(e: Event): void {
    const b = (e.target as HTMLElement).closest<HTMLElement>("[data-view]");
    if (!b) return;
    const kind = b.dataset.view;
    if (kind === "chat" && b.dataset.id) this.go({ kind: "chat", id: b.dataset.id });
    else if (kind === "past") this.go({ kind: "past" });
    else if (kind === "new") this.go({ kind: "new" });
    else this.go({ kind: "current" });
  }

  private go(v: View): void {
    this.view = v;
    this.renderBar();
    this.show();
  }

  // ── views ──

  private unmount(): void {
    for (const off of this.offs) off();
    this.offs = [];
    this.conv?.destroy();
    this.conv = null;
    this.composer?.destroy();
    this.composer = null;
    this.feed = null;
    this.body.innerHTML = "";
  }

  private show(): void {
    this.unmount();
    const v = this.view;
    if (v.kind === "chat") {
      const chat = this.chats.find((c) => c.id === v.id);
      if (chat) return this.mountChat(chat);
    }
    if (v.kind === "new") return void this.mountNew();
    const runs = this.runs();
    if (!runs.length) {
      this.mountFeed();
      this.addHTML(emptyStateHTML({ size: "sm", bodyHTML: t("tasks.chat.empty_html") }));
      this.mountRunComposer(null);
      return;
    }
    if (v.kind === "past") return this.mountPast(runs.slice(0, -1));
    this.mountCurrent(runs[runs.length - 1]);
  }

  private mountFeed(): HTMLElement {
    const feed = document.createElement("div");
    feed.className = "cx-feed";
    feed.setAttribute("role", "log");
    this.body.appendChild(feed);
    this.feed = feed;
    return feed;
  }

  private addHTML(html: string): void {
    this.feed?.insertAdjacentHTML("beforeend", html);
  }

  private sepHTML(run: ConvItem): string {
    const when = run.at
      ? new Date(run.at).toLocaleString(locale, { dateStyle: "short", timeStyle: "short" })
      : "";
    const parts = [run.stage ? this.host.stageLabel(run.stage) : "", run.agent ?? "", when];
    return `<div class="cx-sep">${esc(parts.filter(Boolean).join(" · "))}</div>`;
  }

  private messageHTML(m: ConvMessage): string {
    // A steered message is echoed inside its run's transcript; drawing it here would repeat it.
    if (m.delivery === "steered") return "";
    if (m.role === "system") return `<div class="cx-sep">${esc(m.body)}</div>`;
    const agent = m.agent ?? t("tasks.chat.agent_fallback");
    let note = "";
    if (m.delivery === "chat" && m.chat_id)
      note = `<button type="button" data-tt="open-chat" data-id="${esc(m.chat_id)}">${esc(t("tasks.chat.answers_in_chat", { agent }))}</button>`;
    else if (m.delivery === "unreachable")
      note = `${esc(t("tasks.chat.unread_note"))} <button type="button" data-tt="to-comment" data-id="${esc(m.id)}">${esc(t("tasks.chat.to_comments"))}</button>`;
    else if (m.delivery === "mention") note = esc(t("tasks.chat.answers_in_comments", { agent }));
    return chatUserHTML({ text: m.body, at: m.created_at, id: m.id, noteHTML: note });
  }

  // The run under way (or the last one) and what people wrote since the run before it.
  private mountCurrent(run: ConvItem): void {
    const feed = this.mountFeed();
    const runs = this.runs();
    const prev = runs.length > 1 ? runs[runs.length - 2] : null;
    const from = prev ? this.items.indexOf(prev) + 1 : 0;
    for (const it of this.items.slice(from)) {
      if (it === run) {
        this.addHTML(this.sepHTML(run));
        this.followRun(run, feed, true);
      } else if (it.kind === "message" && it.message) this.addHTML(this.messageHTML(it.message));
    }
    this.mountRunComposer(run);
    feed.scrollTop = feed.scrollHeight;
  }

  private mountPast(runs: ConvItem[]): void {
    const feed = this.mountFeed();
    for (const run of [...runs].reverse()) {
      const turns = run.turns ? t("tasks.chat.run_turns", { count: run.turns }) : "";
      const when = new Date(run.at).toLocaleString(locale, {
        dateStyle: "short",
        timeStyle: "short",
      });
      const label = [run.stage ? this.host.stageLabel(run.stage) : "", run.agent ?? "", when, turns]
        .filter(Boolean)
        .join(" · ");
      const wrap = document.createElement("div");
      wrap.innerHTML = chatStepsHTML({
        summaryHTML: `${chatAvatarHTML(run.agent ?? "", { size: "xs" })}<span>${esc(label)}</span>`,
        bodyHTML: "",
      });
      const details = wrap.firstElementChild as HTMLDetailsElement;
      details.addEventListener(
        "toggle",
        () => {
          if (!details.open || details.dataset.loaded) return;
          details.dataset.loaded = "1";
          const body = details.querySelector(".cx-steps-body") as HTMLElement;
          this.followRun(run, body, false);
        },
        { once: false },
      );
      feed.appendChild(details);
    }
    this.mountRunComposer(runs.at(-1) ?? null, true);
  }

  private followRun(run: ConvItem, into: HTMLElement, current: boolean): void {
    const live = !!run.live && current;
    const view = new LiveTurn({
      agent: run.agent ?? "",
      live,
      answer: true,
      open: live,
      startedAt: Date.parse(run.at) || Date.now(),
      md,
      onStop: live ? () => void this.stopRun() : undefined,
      onChange: () => {
        if (this.feed && current) this.feed.scrollTop = this.feed.scrollHeight;
      },
    });
    into.appendChild(view.el);
    const off = events.subscribe(logsTopic(run.run_id ?? ""), (f) => {
      if (f.type === FRAME_RESET) return;
      if (f.type === FRAME_END || f.type === FRAME_ERROR) {
        view.finishReplay();
        if (live) void this.host.refresh();
        return;
      }
      if (f.type === "line") view.line(String(f.data ?? ""));
    });
    this.offs.push(() => {
      off();
      view.destroy();
    });
  }

  private mountRunComposer(run: ConvItem | null, past = false): void {
    const agent = run?.agent ?? "";
    const live = !!run?.live && !past;
    const placeholder = !agent
      ? t("tasks.chat.placeholder_none")
      : live
        ? t("tasks.chat.placeholder_live", { agent })
        : t("tasks.chat.placeholder_resume", { agent });
    const composer = new ChatComposer({
      agent,
      agentLabel: live ? t("tasks.chat.agent_running", { agent }) : agent,
      workspace: this.host.task.workspace_id,
      pickAgent: !!agent,
      started: true,
      commands: [],
      placeholder,
      livePlaceholder: placeholder,
      value: this.host.draft,
      onSubmit: (text, _refs, mode) => this.converse(text, mode),
      onAgent: (type) => void this.openChatWith(type),
      onInput: (text) => this.host.onDraft(text),
    });
    composer.mount(this.body);
    composer.setLive(live);
    this.composer = composer;
  }

  private mountChat(chat: ChatInfo): void {
    this.conv = new ChatConversation(this.body, {
      chat,
      agent: chat.agent_type,
      workspace: chat.workspace_id,
      commands: [],
      draft: this.host.draft,
      onDraft: (text) => this.host.onDraft(text),
      onAgent: (type) => void this.openChatWith(type),
    });
    void this.conv.load();
  }

  private async mountNew(): Promise<void> {
    const agents = await loadChatAgents(this.host.task.workspace_id);
    if (this.destroyed || this.view.kind !== "new") return;
    const pick =
      agents.find((a) => a.type === lastAgent() && agentReady(a))?.type ??
      agents.find(agentReady)?.type ??
      (agents.length ? agents[0].type : "");
    let agent = pick;
    this.conv = new ChatConversation(this.body, {
      chat: null,
      agent: pick,
      workspace: this.host.task.workspace_id,
      commands: [],
      onAgent: (type) => {
        agent = type;
        rememberAgent(type);
      },
      createChat: () => this.createChat(agent),
      onCreated: (c) => {
        if (!this.chats.some((x) => x.id === c.id)) this.chats.push(c);
        this.view = { kind: "chat", id: c.id };
        this.renderBar();
      },
    });
    void this.conv.load();
    this.conv.insertToken("@");
  }

  // ── actions ──

  private async createChat(agent: string): Promise<ChatInfo | null> {
    if (!agent) return null;
    try {
      const c = await api.post<ChatInfo>("/chats", {
        agent_type: agent,
        task_id: this.host.task.id,
      });
      rememberAgent(agent);
      return { ...c, status: c.status };
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t("chat.errors.offline_create"), "err");
      return null;
    }
  }

  private async openChatWith(type: string): Promise<void> {
    const have = this.chats.find((c) => c.agent_type === type);
    if (have) return this.go({ kind: "chat", id: have.id });
    const c = await this.createChat(type);
    if (!c) return;
    if (!this.chats.some((x) => x.id === c.id)) this.chats.push(c);
    this.go({ kind: "chat", id: c.id });
  }

  private async converse(text: string, mode: SendMode): Promise<boolean> {
    try {
      const res = await api.post<{ delivery: string; detail?: string }>(
        `/tasks/${this.host.task.id}/converse`,
        { text, interrupt: mode === "steer" },
      );
      if (res.detail) toast(res.detail, "info");
      await this.load();
      await this.host.refresh();
      return true;
    } catch {
      toast(t("tasks.chat.send_failed"), "err");
      return false;
    }
  }

  private async stopRun(): Promise<void> {
    const ok = await confirmDialog({
      title: t("tasks.chat.stop_title"),
      message: t("tasks.chat.stop_message"),
      confirmLabel: t("chat.live.stop"),
      danger: true,
    });
    if (!ok) return;
    try {
      const list = await api.get<AgentRow[]>("/agents");
      const hit = list.find((a) => a.task_id === this.host.task.id && a.status === "running");
      if (!hit) {
        toast(t("tasks.chat.stop_gone"), "info");
        return;
      }
      await api.post(`/agents/${hit.id}/stop`);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : t("tasks.chat.send_failed"), "err");
      return;
    }
    await this.host.refresh();
    await this.load();
  }

  private bodyClick(e: Event): void {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>("button[data-tt]");
    if (!b) return;
    const id = b.dataset.id ?? "";
    if (b.dataset.tt === "open-chat") {
      if (this.chats.some((c) => c.id === id)) return this.go({ kind: "chat", id });
      document.dispatchEvent(new CustomEvent("octopus:open-chat", { detail: { id } }));
      return;
    }
    if (b.dataset.tt === "to-comment") {
      b.disabled = true;
      void api
        .post(`/tasks/${this.host.task.id}/conversation/${id}/to-comment`, {})
        .then(async () => {
          toast(t("tasks.chat.moved_to_comments"), "ok");
          await this.host.refresh();
          await this.load();
        })
        .catch(() => {
          b.disabled = false;
          toast(t("tasks.chat.to_comments_failed"), "err");
        });
    }
  }
}
