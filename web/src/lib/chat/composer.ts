// The chat composer: # names a task, @ picks an agent, / runs a command. While a turn runs, Enter
// queues the message and ⌘Enter stops the turn and sends it now.

import { t } from "../../i18n";
import { chordText, MOD_KEY } from "../shortcuts";
import {
  chatAvatarHTML,
  chatKbdHTML,
  chatShortID,
  chatStateHTML,
  chatTaskChipHTML,
  esc,
  icoSvg,
} from "../ui";
import {
  agentReady,
  loadChatAgents,
  loadTaskPicks,
  mcpTagsHTML,
  rankTasks,
  type ChatAgentInfo,
  type TaskPick,
} from "./data";

export type SendMode = "" | "queue" | "steer";

export interface ComposerCommand {
  id: string;
  word: string;
  title: string;
  sub: string;
}

export interface QueuedChip {
  id: string;
  text: string;
}

export interface ChatComposerOptions {
  agent: string;
  agentLabel?: string;
  workspace?: string;
  // The agent pick shows in the bar; started means picking another one opens a new chat.
  pickAgent: boolean;
  started: boolean;
  commands: ComposerCommand[];
  placeholder: string;
  livePlaceholder?: string;
  value?: string;
  onSubmit: (text: string, refs: string[], mode: SendMode) => Promise<boolean> | boolean;
  onAgent?: (type: string) => void;
  onCommand?: (id: string) => void;
  onTask?: (task: TaskPick) => void;
  onUnqueue?: (id: string) => void;
  onInput?: (text: string) => void;
}

type Pop = { kind: "task" | "agent" | "cmd"; start: number; typed: boolean } | null;

const MAX_LINES_PX = 180;
const TASK_ROWS = 6;

export class ChatComposer {
  readonly form: HTMLFormElement;
  readonly hint: HTMLElement;
  private readonly opts: ChatComposerOptions;
  private readonly input: HTMLTextAreaElement;
  private readonly chips: HTMLElement;
  private readonly slot: HTMLElement;
  private readonly sendWrap: HTMLElement;
  private pickBtn: HTMLButtonElement | null = null;
  private pop: Pop = null;
  private live = false;
  private busy = false;
  private refs: string[] = [];
  private queued: QueuedChip[] = [];
  private agent: string;
  private readonly offDoc: () => void;

  constructor(opts: ChatComposerOptions) {
    this.opts = opts;
    this.agent = opts.agent;
    const form = document.createElement("form");
    form.className = "cx-composer";
    form.autocomplete = "off";
    const pick = opts.pickAgent
      ? `<button type="button" class="cx-pick" data-cx="pick" aria-haspopup="listbox" aria-expanded="false" title="${esc(t("chat.composer.agent_pick"))}"></button>`
      : "";
    form.innerHTML =
      `<div class="cx-composer-chips"></div>` +
      `<textarea rows="1" aria-label="${esc(t("chat.composer.label"))}"></textarea>` +
      `<div class="cx-composer-bar">${pick}` +
      `<button type="button" class="cx-ibtn" data-cx="hash" aria-label="${esc(t("chat.composer.hash"))}" title="${esc(t("chat.composer.hash"))}">${icoSvg("hash")}</button>` +
      `<span class="cx-spacer"></span><span class="cx-send-slot"></span></div>` +
      `<div class="cx-pop-slot"></div>`;
    this.form = form;
    this.input = form.querySelector("textarea") as HTMLTextAreaElement;
    this.chips = form.querySelector(".cx-composer-chips") as HTMLElement;
    this.slot = form.querySelector(".cx-pop-slot") as HTMLElement;
    this.sendWrap = form.querySelector(".cx-send-slot") as HTMLElement;
    this.pickBtn = form.querySelector("[data-cx=pick]");
    this.hint = document.createElement("div");
    this.hint.className = "cx-hint";
    this.input.value = opts.value ?? "";
    this.renderPick(opts.agentLabel);
    this.renderSend();
    this.renderHint();
    this.grow();

    this.input.addEventListener("input", () => {
      this.grow();
      this.detect();
      opts.onInput?.(this.input.value);
    });
    this.input.addEventListener("keydown", (e) => this.keydown(e));
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      void this.submit(this.live ? "queue" : "");
    });
    form.addEventListener("click", (e) => this.click(e));
    const onDoc = (e: MouseEvent) => {
      if (!this.form.contains(e.target as Node)) this.closePop();
    };
    document.addEventListener("click", onDoc);
    this.offDoc = () => document.removeEventListener("click", onDoc);
  }

  mount(parent: HTMLElement): void {
    parent.append(this.form, this.hint);
  }

  destroy(): void {
    this.offDoc();
    this.form.remove();
    this.hint.remove();
  }

  get value(): string {
    return this.input.value;
  }

  set value(v: string) {
    this.input.value = v;
    this.grow();
  }

  focus(): void {
    this.input.focus();
    const n = this.input.value.length;
    this.input.setSelectionRange(n, n);
  }

  setAgent(type: string, label?: string): void {
    this.agent = type;
    this.renderPick(label);
  }

  setStarted(started: boolean): void {
    this.opts.started = started;
  }

  setPlaceholder(text: string): void {
    this.opts.placeholder = text;
    this.renderPlaceholder();
  }

  setLive(live: boolean): void {
    if (this.live === live) return;
    this.live = live;
    this.renderSend();
    this.renderHint();
    this.renderPlaceholder();
  }

  setDisabled(disabled: boolean): void {
    this.input.disabled = disabled;
    this.form.toggleAttribute("data-disabled", disabled);
    this.form.querySelectorAll<HTMLButtonElement>("button").forEach((b) => (b.disabled = disabled));
  }

  setQueue(items: readonly QueuedChip[]): void {
    this.queued = [...items];
    this.renderChips();
  }

  addRef(task: TaskPick): void {
    if (!this.refs.includes(task.id)) this.refs.push(task.id);
    this.renderChips();
  }

  insertToken(ch: "#" | "@" | "/"): void {
    const v = this.input.value;
    this.input.value = v + (v && !v.endsWith(" ") ? " " : "") + ch;
    this.focus();
    this.detect();
  }

  // ── rendering ──

  private renderPick(label?: string): void {
    if (!this.pickBtn) return;
    this.pickBtn.innerHTML = `${chatAvatarHTML(this.agent, { size: "xs" })}<span class="cx-pick-name">${esc(label ?? this.agent)}</span>${icoSvg("chevron-down")}`;
  }

  private renderPlaceholder(): void {
    this.input.placeholder =
      this.live && this.opts.livePlaceholder ? this.opts.livePlaceholder : this.opts.placeholder;
  }

  private renderSend(): void {
    if (this.live) {
      this.sendWrap.innerHTML = `<span class="cx-send cx-send-split"><button type="submit" data-cx="queue">${esc(t("chat.composer.queue"))}</button><button type="button" class="cx-send-dd" data-cx="send-menu" aria-haspopup="menu" aria-label="${esc(t("chat.composer.send_options"))}">${icoSvg("chevron-down")}</button></span>`;
    } else {
      this.sendWrap.innerHTML = `<button type="submit" class="cx-send" data-cx="send" aria-label="${esc(t("chat.composer.send"))}" title="${esc(t("chat.composer.send"))}">${icoSvg("arrow-right")}</button>`;
    }
    this.renderPlaceholder();
  }

  private renderHint(): void {
    const parts = [
      `<span>${chatKbdHTML("#")} ${esc(t("chat.composer.hint_task"))}</span>`,
      `<span>${chatKbdHTML("@")} ${esc(t("chat.composer.hint_agent"))}</span>`,
      this.opts.commands.length
        ? `<span>${chatKbdHTML("/")} ${esc(t("chat.composer.hint_commands"))}</span>`
        : "",
      this.live
        ? `<span>${chatKbdHTML(chordText([MOD_KEY, "↵"]))} ${esc(t("chat.composer.hint_steer"))}</span>`
        : "",
    ];
    this.hint.innerHTML = parts.join("");
  }

  private renderChips(): void {
    const queued = this.queued.map(
      (q) =>
        `<span class="cx-chip" data-queued="${esc(q.id)}"><span class="cx-chip-id">${esc(t("chat.user.queued"))}</span><span class="cx-chip-text">${esc(q.text)}</span><button type="button" class="cx-chip-x" data-cx="unqueue" aria-label="${esc(t("chat.user.unqueue"))}">${icoSvg("x")}</button></span>`,
    );
    const refs = this.refs.map((id) =>
      chatTaskChipHTML(
        { id, title: this.taskTitle(id) },
        { short: true, removable: true, removeLabel: t("chat.ctx.remove_task") },
      ),
    );
    this.chips.innerHTML = queued.join("") + refs.join("");
  }

  private taskCache = new Map<string, string>();

  private taskTitle(id: string): string {
    return this.taskCache.get(id) ?? id;
  }

  private grow(): void {
    this.input.style.height = "auto";
    this.input.style.height = Math.min(this.input.scrollHeight, MAX_LINES_PX) + "px";
  }

  // ── popovers ──

  private detect(): void {
    const before = this.input.value.slice(0, this.input.selectionStart);
    const m = before.match(/(^|\s)([#@/])([^\s]*)$/);
    // / only as the first thing written: a path in the middle of a sentence is not a command.
    if (
      !m ||
      (m[2] === "/" && before.trim() !== "/" + m[3]) ||
      (m[2] === "/" && !this.opts.commands.length)
    ) {
      if (this.pop?.typed || this.pop?.kind !== "agent") this.closePop();
      return;
    }
    const start = before.length - m[3].length - 1;
    if (m[2] === "#") void this.openTasks(m[3], start);
    else if (m[2] === "@") void this.openAgents(m[3], start, true);
    else this.openCommands(m[3], start);
  }

  // A popover can close while its list loads; read it fresh after an await.
  private popKind(): string {
    return this.pop ? this.pop.kind : "";
  }

  private closePop(): void {
    this.pop = null;
    this.slot.innerHTML = "";
    this.pickBtn?.setAttribute("aria-expanded", "false");
  }

  private async openTasks(q: string, start: number): Promise<void> {
    this.pop = { kind: "task", start, typed: true };
    const picks = await loadTaskPicks();
    if (this.popKind() !== "task") return;
    for (const p of picks) this.taskCache.set(p.id, p.title);
    const list = rankTasks(picks, q).slice(0, TASK_ROWS);
    const head = q ? t("chat.pop.tasks_query", { q }) : t("chat.pop.tasks");
    const badge = (p: TaskPick) =>
      p.status === "blocked"
        ? chatStateHTML("needs_you")
        : p.status === "failed"
          ? chatStateHTML("error", t("chat.pop.task_failed"))
          : p.status === "running"
            ? chatStateHTML("running", t("chat.pop.task_running"))
            : "<span></span>";
    const rows = list.map(
      (p, i) =>
        `<button type="button" class="cx-opt" role="option" data-task="${esc(p.id)}"${i === 0 ? " data-hl" : ""}><span class="cx-opt-id">#${esc(chatShortID(p.id))}</span><span class="cx-opt-main"><span class="cx-opt-name" data-plain>${esc(p.title)}</span><span class="cx-opt-desc">${esc([p.board, p.stage].filter(Boolean).join(" · "))}</span></span>${badge(p)}</button>`,
    );
    this.slot.innerHTML = `<div class="cx-pop" role="listbox" aria-label="${esc(t("chat.pop.tasks"))}"><div class="cx-pop-head">${esc(head)}</div>${rows.length ? rows.join("") : `<div class="cx-pop-note">${esc(t("chat.pop.tasks_none"))}</div>`}</div>`;
  }

  private async openAgents(q: string, start: number, typed: boolean): Promise<void> {
    this.pop = { kind: "agent", start, typed };
    this.pickBtn?.setAttribute("aria-expanded", "true");
    const agents = await loadChatAgents(this.opts.workspace);
    if (this.popKind() !== "agent") return;
    const ql = q.toLowerCase();
    const match = (a: ChatAgentInfo) =>
      !ql || a.type.toLowerCase().includes(ql) || a.name.toLowerCase().includes(ql);
    const current = agents.filter((a) => a.type === this.agent && match(a));
    const rest = agents.filter((a) => a.type !== this.agent && match(a));
    const opt = (a: ChatAgentInfo) => {
      const meta = [a.engine, a.model, a.effort].filter(Boolean).join(" · ");
      const ready = agentReady(a);
      const desc = ready
        ? (a.description ?? "")
        : !a.prompt_ready
          ? t("chat.pop.lacks_prompt")
          : t("chat.pop.lacks_llm");
      const side =
        a.type === this.agent
          ? icoSvg("check")
          : this.opts.started
            ? `<span class="cx-tag">${esc(t("chat.pop.new_chat"))}</span>`
            : "<span></span>";
      const tools = a.mcp?.length ? `<span class="cx-opt-tools">${mcpTagsHTML(a.mcp)}</span>` : "";
      return `<button type="button" class="cx-opt" role="option" data-agent="${esc(a.type)}" aria-selected="${a.type === this.agent}"${ready ? "" : ' aria-disabled="true"'}${a.type === this.agent ? " data-hl" : ""}>${chatAvatarHTML(a.type)}<span class="cx-opt-main"><span class="cx-opt-name">${esc(a.name)}${meta ? `<span class="cx-opt-meta">${esc(meta)}</span>` : ""}</span><span class="cx-opt-desc">${esc(desc)}</span>${tools}</span>${side}</button>`;
    };
    // Before the first message the chosen agent is only the last one used, not this chat's.
    const head = q
      ? t("chat.pop.agents_query", { q })
      : this.opts.started
        ? t("chat.pop.agents_current")
        : t("chat.pop.agents_recent");
    const parts = [`<div class="cx-pop-head">${esc(head)}</div>`, ...current.map(opt)];
    if (!q && rest.length)
      parts.push(`<div class="cx-pop-head">${esc(t("chat.pop.agents_workspace"))}</div>`);
    parts.push(...rest.map(opt));
    if (!agents.length)
      parts.push(`<div class="cx-pop-note">${esc(t("chat.pop.agents_none"))}</div>`);
    if (this.opts.started && agents.length > 1)
      parts.push(
        `<div class="cx-pop-note">${esc(t("chat.pop.switch_note", { agent: this.agent }))}</div>`,
      );
    this.slot.innerHTML = `<div class="cx-pop" role="listbox" aria-label="${esc(t("chat.pop.agents"))}">${parts.join("")}</div>`;
    if (!this.slot.querySelector("[data-hl]"))
      this.slot.querySelector(".cx-opt:not([aria-disabled])")?.setAttribute("data-hl", "");
  }

  private openCommands(q: string, start: number): void {
    this.pop = { kind: "cmd", start, typed: true };
    const list = this.opts.commands.filter((c) => !q || c.word.startsWith(q.toLowerCase()));
    const rows = list.map(
      (c, i) =>
        `<button type="button" class="cx-opt" role="option" data-cmd="${esc(c.id)}"${i === 0 ? " data-hl" : ""}><span class="cx-opt-cmd">/${esc(c.word)}</span><span class="cx-opt-main"><span class="cx-opt-name">${esc(c.title)}</span><span class="cx-opt-desc">${esc(c.sub)}</span></span><span></span></button>`,
    );
    this.slot.innerHTML = `<div class="cx-pop" role="listbox" aria-label="${esc(t("chat.pop.commands"))}"><div class="cx-pop-head">${esc(t("chat.pop.commands"))}</div>${rows.join("")}</div>`;
  }

  private replaceToken(text: string): void {
    if (!this.pop?.typed) return;
    const end = this.input.selectionStart;
    const v = this.input.value;
    this.input.value = v.slice(0, this.pop.start) + text + v.slice(end);
    const pos = this.pop.start + text.length;
    this.input.focus();
    this.input.setSelectionRange(pos, pos);
    this.grow();
  }

  private moveHL(d: number): void {
    const opts = [...this.slot.querySelectorAll<HTMLElement>(".cx-opt:not([aria-disabled])")];
    if (!opts.length) return;
    let i = opts.findIndex((o) => o.hasAttribute("data-hl"));
    opts.forEach((o) => o.removeAttribute("data-hl"));
    i = (i + d + opts.length) % opts.length;
    opts[i].setAttribute("data-hl", "");
    opts[i].scrollIntoView({ block: "nearest" });
  }

  // ── input ──

  private keydown(e: KeyboardEvent): void {
    if (e.isComposing) return;
    if (this.slot.firstElementChild) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        this.moveHL(e.key === "ArrowDown" ? 1 : -1);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        this.closePop();
        return;
      }
      if ((e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey) || e.key === "Tab") {
        const hl = this.slot.querySelector<HTMLElement>(".cx-opt[data-hl]");
        if (hl) {
          e.preventDefault();
          hl.click();
          return;
        }
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      const steer = (e.metaKey || e.ctrlKey) && this.live;
      void this.submit(steer ? "steer" : this.live ? "queue" : "");
    }
  }

  private click(e: Event): void {
    const el = (e.target as HTMLElement).closest<HTMLElement>("button");
    if (!el || !this.form.contains(el)) return;
    if (el.dataset.task) return this.pickTask(el.dataset.task);
    if (el.dataset.agent) {
      if (el.getAttribute("aria-disabled") === "true") return;
      return this.pickAgent(el.dataset.agent);
    }
    if (el.dataset.cmd) return this.pickCommand(el.dataset.cmd);
    if (el.hasAttribute("data-cx-remove")) {
      const id = el.closest<HTMLElement>("[data-task]")?.dataset.task;
      this.refs = this.refs.filter((r) => r !== id);
      return this.renderChips();
    }
    switch (el.dataset.cx ?? "") {
      case "pick":
        e.stopPropagation();
        if (this.pop?.kind === "agent") this.closePop();
        else void this.openAgents("", 0, false);
        return;
      case "hash":
        return this.insertToken("#");
      case "unqueue": {
        const id = el.closest<HTMLElement>("[data-queued]")?.dataset.queued;
        if (id) this.opts.onUnqueue?.(id);
        return;
      }
      case "send-menu":
        e.stopPropagation();
        return this.toggleSendMenu();
      case "m-queue":
        this.closeSendMenu();
        void this.submit("queue");
        return;
      case "m-steer":
        this.closeSendMenu();
        void this.submit("steer");
        return;
      default:
        return;
    }
  }

  private toggleSendMenu(): void {
    if (this.form.querySelector(".cx-menu")) return this.closeSendMenu();
    const steer = chordText([MOD_KEY, "↵"]);
    this.form.insertAdjacentHTML(
      "beforeend",
      `<div class="cx-menu" role="menu"><button type="button" role="menuitem" data-cx="m-queue"><span class="cx-menu-title">${esc(t("chat.composer.queue"))}</span><span class="cx-menu-sub">${esc(t("chat.composer.queue_sub"))}</span></button><button type="button" role="menuitem" data-cx="m-steer"><span class="cx-menu-title">${esc(t("chat.composer.steer"))}</span><span class="cx-menu-sub">${esc(t("chat.composer.steer_sub", { keys: steer }))}</span></button></div>`,
    );
  }

  private closeSendMenu(): void {
    this.form.querySelector(".cx-menu")?.remove();
  }

  private pickTask(id: string): void {
    void loadTaskPicks().then((picks) => {
      const p = picks.find((x) => x.id === id);
      if (!p) return;
      this.taskCache.set(p.id, p.title);
      this.replaceToken("#" + chatShortID(p.id) + " ");
      this.closePop();
      this.addRef(p);
      this.opts.onTask?.(p);
    });
  }

  private pickAgent(type: string): void {
    this.replaceToken("");
    this.closePop();
    if (type === this.agent) return;
    this.opts.onAgent?.(type);
  }

  private pickCommand(id: string): void {
    this.replaceToken("");
    this.closePop();
    this.opts.onCommand?.(id);
  }

  private async submit(mode: SendMode): Promise<void> {
    const text = this.input.value.trim();
    if (!text || this.busy || this.input.disabled) return;
    this.closePop();
    this.closeSendMenu();
    this.busy = true;
    const refs = [...this.refs];
    try {
      const clear = await this.opts.onSubmit(text, refs, mode);
      if (clear) {
        this.input.value = "";
        this.refs = [];
        this.renderChips();
        this.grow();
        this.opts.onInput?.("");
      }
    } finally {
      this.busy = false;
      this.input.focus();
    }
  }
}
