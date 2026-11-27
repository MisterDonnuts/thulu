// A turn drawn from the engine's own lines: live while an agent works (a chat turn or a task run),
// or replayed from a transcript to fill a finished turn's steps. Claude, Codex and OpenCode lines
// all pass through normalizeLiveEvent first.

import { t } from "../../i18n";
import { liveToolPreview, normalizeLiveEvent, type LiveBlock, type LiveEvent } from "../liveview";
import {
  chatAvatarHTML,
  chatClock,
  chatCost,
  chatDuration,
  chatFootEl,
  chatSayStepEl,
  chatStepHTML,
  chatStepsHTML,
  chatStepsSummary,
  chatStoppedHTML,
  chatToolInfo,
  chatTokens,
  chatWorkingHTML,
  esc,
  type ChatToolCall,
} from "../ui";

export interface TurnResult {
  ok: boolean;
  outOfTurns: boolean;
  turns: number;
  ms: number;
  cost: number;
  outputTokens: number;
  cacheRead: number;
}

export interface LiveTurnOptions {
  agent: string;
  model?: string;
  // live draws the working line and keeps the steps open; a replay only fills the steps.
  live: boolean;
  // answer: the last text the agent wrote shows as the answer; off, it is dropped (the chat
  // message carries it).
  answer: boolean;
  open?: boolean;
  startedAt?: number;
  md: (text: string) => string;
  onStop?: () => void;
  onChange?: () => void;
}

interface Pending {
  el: HTMLElement;
  call: ChatToolCall;
  at: number;
}

export function parseLine(raw: string): LiveEvent | null {
  const s = raw.trim();
  const i = s.indexOf("{");
  if (i < 0) return null;
  try {
    return normalizeLiveEvent(JSON.parse(s.slice(i)) as LiveEvent);
  } catch {
    return null;
  }
}

function num(v: unknown): number {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

export class LiveTurn {
  readonly el: HTMLElement;
  result: TurnResult | null = null;
  private readonly opts: LiveTurnOptions;
  private readonly details: HTMLDetailsElement;
  private readonly sum: HTMLElement;
  private readonly body: HTMLElement;
  private readonly answerEl: HTMLElement;
  private working: HTMLElement | null = null;
  private readonly pending = new Map<string, Pending>();
  private readonly tools: ChatToolCall[] = [];
  private lastText = "";
  private timer = 0;
  private readonly started: number;
  private modelEl: HTMLElement | null = null;

  constructor(opts: LiveTurnOptions) {
    this.opts = opts;
    this.started = opts.startedAt ?? Date.now();
    const el = document.createElement("div");
    el.className = "cx-turn";
    const model = opts.model ? esc(opts.model) : "";
    el.innerHTML =
      `<div class="cx-who">${chatAvatarHTML(opts.agent)}${esc(opts.agent)}<span class="cx-who-model">${model}</span></div>` +
      chatStepsHTML({ summaryHTML: "", bodyHTML: "", open: opts.open ?? opts.live }) +
      `<div class="cx-answer"></div>` +
      (opts.live ? chatWorkingHTML({ stop: !!opts.onStop }) : "");
    this.el = el;
    this.details = el.querySelector("details") as HTMLDetailsElement;
    this.sum = el.querySelector(".cx-steps-sum") as HTMLElement;
    this.body = el.querySelector(".cx-steps-body") as HTMLElement;
    this.answerEl = el.querySelector(".cx-answer") as HTMLElement;
    this.modelEl = el.querySelector(".cx-who-model");
    this.working = el.querySelector(".cx-working");
    this.working
      ?.querySelector("[data-cx-act=stop]")
      ?.addEventListener("click", () => opts.onStop?.());
    if (opts.live) {
      this.timer = window.setInterval(() => this.tick(), 1000);
      this.tick();
    }
    this.renderSummary();
  }

  // stepsOnly hides the answer and the who line: the finished chat turn already shows both.
  static stepsBody(lines: readonly string[], md: (s: string) => string): LiveTurn {
    const turn = new LiveTurn({ agent: "", live: false, answer: false, md, open: true });
    for (const l of lines) turn.line(l);
    turn.finishReplay();
    return turn;
  }

  stepsHTML(): string {
    return this.body.innerHTML;
  }

  stepCount(): number {
    return this.tools.length;
  }

  line(raw: string): void {
    const ev = parseLine(raw);
    if (ev) this.event(ev);
  }

  event(ev: LiveEvent): void {
    switch (ev.type ?? "") {
      case "system":
        if (ev.subtype === "init" && ev.model && this.modelEl && !this.modelEl.textContent)
          this.modelEl.textContent = ev.model;
        return;
      case "assistant":
        return this.assistant(ev);
      case "user":
        return this.user(ev);
      case "result":
        return this.finish(ev);
      case "warning":
        if (ev.text) this.body.append(chatSayStepEl(String(ev.text)));
        return;
      default:
        return;
    }
  }

  private blocks(ev: LiveEvent): LiveBlock[] {
    const c = ev.message?.content;
    return Array.isArray(c) ? c : [];
  }

  private assistant(ev: LiveEvent): void {
    for (const b of this.blocks(ev)) {
      if (b.type === "text" && b.text?.trim()) {
        this.flushText();
        this.lastText = b.text;
        if (this.opts.answer) this.answerEl.innerHTML = this.opts.md(b.text);
      } else if (b.type === "thinking") {
        this.setWorking(t("chat.live.thinking"), "");
      } else if (b.type === "tool_use") {
        this.flushText();
        this.toolUse(b);
      }
    }
    this.changed();
  }

  // A text followed by more work was a note on the way, not the answer.
  private flushText(): void {
    if (!this.lastText) return;
    this.body.append(chatSayStepEl(this.lastText));
    if (this.opts.answer) this.answerEl.innerHTML = "";
    this.lastText = "";
  }

  private toolUse(b: LiveBlock): void {
    const name = b.name ?? "";
    const call: ChatToolCall = { name, hint: liveToolPreview(name, b.input ?? {}) };
    this.tools.push(call);
    // Codex reports a tool when it is done and gives it no id: nothing will come to close it.
    const open = !!b.id && this.opts.live;
    const tpl = document.createElement("template");
    tpl.innerHTML = chatStepHTML(call, open ? "run" : "ok");
    const el = tpl.content.firstElementChild as HTMLElement;
    this.body.appendChild(el);
    if (b.id) this.pending.set(b.id, { el, call, at: Date.now() });
    const info = chatToolInfo(call.name, call.hint);
    this.setWorking(info.label, info.text);
    this.renderSummary();
  }

  private user(ev: LiveEvent): void {
    for (const b of this.blocks(ev)) {
      if (b.type !== "tool_result" || !b.tool_use_id) continue;
      const p = this.pending.get(b.tool_use_id);
      if (!p) continue;
      this.pending.delete(b.tool_use_id);
      const secs = this.opts.live ? Math.round((Date.now() - p.at) / 1000) : 0;
      const tpl = document.createElement("template");
      tpl.innerHTML = chatStepHTML(p.call, b.is_error ? "err" : "ok", secs);
      p.el.replaceWith(tpl.content);
    }
    this.changed();
  }

  private finish(ev: LiveEvent): void {
    const usage = (ev.usage ?? {}) as Record<string, unknown>;
    this.result = {
      ok: !ev.is_error,
      outOfTurns: ev.subtype === "error_max_turns",
      turns: num(ev.num_turns),
      ms: num(ev.duration_ms) || Date.now() - this.started,
      cost: num(ev.total_cost_usd ?? ev.cost_usd),
      outputTokens: num(usage.output_tokens),
      cacheRead: num(usage.cache_read_input_tokens),
    };
    if (typeof ev.result === "string" && ev.result.trim() && !this.lastText) {
      this.lastText = ev.result;
      if (this.opts.answer) this.answerEl.innerHTML = this.opts.md(ev.result);
    }
    this.close();
    if (this.opts.live) this.details.open = false;
    if (this.opts.answer) {
      const r = this.result;
      const status = r.outOfTurns
        ? t("chat.run.out_of_turns")
        : r.ok
          ? t("chat.run.ok")
          : t("chat.run.failed");
      const parts = [
        status,
        chatDuration(r.ms),
        chatCost(r.cost),
        r.outputTokens ? t("chat.run.output", { tokens: chatTokens(r.outputTokens) }) : "",
        r.cacheRead ? t("chat.run.cache", { tokens: chatTokens(r.cacheRead) }) : "",
      ];
      this.el.append(chatFootEl(parts));
    }
    this.changed();
  }

  // A replay that ends without a result line (an older transcript, a cut run) still closes.
  finishReplay(): void {
    if (!this.result) {
      this.lastText = this.opts.answer ? this.lastText : "";
      this.close();
    }
  }

  stopped(): void {
    this.close();
    this.el.insertAdjacentHTML("beforeend", chatStoppedHTML());
    this.changed();
  }

  private close(): void {
    window.clearInterval(this.timer);
    this.timer = 0;
    this.working?.remove();
    this.working = null;
    for (const p of this.pending.values()) {
      const tpl = document.createElement("template");
      tpl.innerHTML = chatStepHTML(p.call, "ok");
      p.el.replaceWith(tpl.content);
    }
    this.pending.clear();
    if (!this.opts.answer) this.lastText = "";
    this.renderSummary();
  }

  destroy(): void {
    window.clearInterval(this.timer);
    this.timer = 0;
  }

  private setWorking(what: string, now: string): void {
    if (!this.working) return;
    const whatEl = this.working.querySelector(".cx-working-what");
    const nowEl = this.working.querySelector(".cx-working-now");
    if (whatEl) whatEl.textContent = what;
    if (nowEl) nowEl.textContent = now;
  }

  private tick(): void {
    const el = this.working?.querySelector(".cx-working-clock");
    if (el) el.textContent = chatClock(Date.now() - this.started);
  }

  private renderSummary(): void {
    const live = !!this.working;
    this.sum.innerHTML = chatStepsSummary({
      live,
      ms: this.result?.ms ?? Date.now() - this.started,
      steps: this.tools.length,
      tools: live ? undefined : this.tools,
    });
    // A finished turn that did nothing has no steps to fold.
    this.details.hidden = !live && this.tools.length === 0 && !this.body.childElementCount;
  }

  private changed(): void {
    this.opts.onChange?.();
  }
}
