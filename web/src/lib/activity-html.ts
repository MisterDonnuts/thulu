import { t } from "../i18n";
import {
  ACTIVITY_FILTERS,
  dayLabel,
  groupSummary,
  noteLink,
  prOpenedText,
  prRef,
  relTime,
  runDurationText,
  stoppedText,
  turnsText,
  type ActivityFilter,
  type RunInfo,
  type StreamComment,
  type StreamEvent,
  type SystemEntry,
  type Viewer,
} from "./activity";
import { causeLink, causePicksAgent, quotaLink, runBadgeTone, type Tone } from "./task-detail";
import { fmtDate, fmtDayClock } from "./time";
import {
  authorLineHTML,
  avatarHTML as uiAvatarHTML,
  badgeHTML,
  buttonHTML,
  calloutHTML,
  composerHTML,
  esc,
  icoSvg,
  listRowHTML,
  segmentedHTML,
  tagHTML,
  type Attrs,
  type TagTone,
} from "./ui";
import {
  isBlocker,
  isBlocking,
  questionItems,
  recordedAnswer,
  type TaskQuestion,
} from "./questions";
import type { Thread } from "./threads";

export interface ActivityCtx {
  now: number;
  stageLabel(id: string): string;
  eventLabel(kind: string): string;
  liveAction?(runID: string): string;
  viewer?: Viewer | null;
  eventText?(e: { kind: string; payload?: Record<string, unknown> | null }): string;
  eventLine?(ev: StreamEvent): string;
  fixedAbove?: string;
}

interface ActButtonOptions {
  icon?: string;
  danger?: boolean;
  title?: string;
  href?: string;
}

function actButton(label: string, attrs: Attrs, opts: ActButtonOptions = {}): string {
  return buttonHTML({
    label,
    icon: opts.icon,
    href: opts.href,
    size: "sm",
    variant: opts.danger ? "danger" : "ghost",
    cls: "act-btn",
    title: opts.title,
    attrs,
  });
}

export function timeHTML(iso: string, now: number): string {
  if (!iso) return "";
  return `<time class="act-time" datetime="${esc(iso)}" data-ts="${esc(iso)}" title="${esc(fmtDate(iso))}">${esc(relTime(iso, now))}</time>`;
}

export type AuthorKind = "agent" | "person" | "system";

export function authorKind(source: string | undefined): AuthorKind {
  if (source === "agent" || source === "auditor") return "agent";
  if (source === "system" || source === "automation") return "system";
  return "person";
}

export function avatarHTML(kind: AuthorKind, name: string): string {
  return uiAvatarHTML({ kind, name });
}

const SOURCE_TONE: Record<string, TagTone> = {
  agent: "special",
  auditor: "warn",
  system: "muted",
};

export function sourceTagHTML(source: string, label: string): string {
  return tagHTML(label, { size: "sm", tone: SOURCE_TONE[source] ?? "neutral" });
}

export interface MetaInput {
  name: string;
  kind: AuthorKind;
  at: string;
  stage?: string;
}

// asideHTML goes after the author line: a comment's kind tag, for one.
export function metaHTML(m: MetaInput, ctx: ActivityCtx, asideHTML = ""): string {
  const line = authorLineHTML(
    {
      name: m.name,
      kind: m.kind,
      at: m.at,
      stage: m.stage ? ctx.stageLabel(m.stage) : undefined,
    },
    ctx.now,
  );
  return `<div class="act-meta">${line}${asideHTML}</div>`;
}

export function runningForText(startedAt: string, now: number): string {
  const run = { startedAt, finishedAt: "" } as RunInfo;
  return t("lib.activity.run.for", { duration: runDurationText(run, now) });
}

const RUN_STATE: Record<RunInfo["state"], { word: string; tone: Tone; icon: string }> = {
  running: { word: "lib.activity.run.state.running", tone: "info", icon: "" },
  queued: { word: "lib.activity.run.state.queued", tone: "muted", icon: "i-clock" },
  done: { word: "lib.activity.run.state.done", tone: "success", icon: "i-check-circle" },
  failed: { word: "lib.activity.run.state.failed", tone: "danger", icon: "i-x-circle" },
  ended: { word: "lib.activity.run.state.ended", tone: "muted", icon: "i-circle" },
  not_started: { word: "lib.activity.run.state.not_started", tone: "danger", icon: "i-x-circle" },
  stopped: { word: "lib.activity.run.state.stopped", tone: "muted", icon: "i-power" },
  out_of_turns: { word: "lib.activity.run.state.out_of_turns", tone: "danger", icon: "i-alert" },
};

function runErrorHTML(text: string, fixHTML = ""): string {
  return calloutHTML({
    tone: "danger",
    icon: "i-alert",
    body: text,
    actionsHTML: fixHTML || undefined,
  });
}

function fixLink(link: { href: string; label: string } | null): string {
  return link ? actButton(link.label, {}, { href: link.href }) : "";
}

function causeFix(cause: string | undefined, ctx: ActivityCtx): string {
  if (!cause) return "";
  if (causePicksAgent(cause)) {
    if (causePicksAgent(ctx.fixedAbove)) return "";
    return actButton(t("lib.task_detail.cause.pick_agent"), { "data-pick-agent": true });
  }
  return cause === ctx.fixedAbove ? "" : fixLink(causeLink(cause));
}

function runStateWord(run: RunInfo): string {
  if (run.state === "not_started") {
    if (run.cause === "credential") return "lib.task_detail.run.credential_blocked";
    if (causePicksAgent(run.cause)) return "lib.task_detail.run.no_agent";
  }
  return RUN_STATE[run.state].word;
}

// A run's state as a pill: done, failed, running (pulsing), queued...
export function runPillHTML(run: RunInfo): string {
  const st = RUN_STATE[run.state];
  return badgeHTML(t(runStateWord(run)), runBadgeTone(st.tone), {
    shape: "pill",
    icon: st.icon || undefined,
    pulse: !st.icon,
    attrs: { "data-run-state": run.state },
  });
}

// What a run says under its title: the tool a running agent is on, why it waits, why it did not
// start, ran out of turns, stopped or failed.
export function runNoticesHTML(run: RunInfo, ctx: ActivityCtx, errorLine = ""): string[] {
  const action = run.state === "running" ? (ctx.liveAction?.(run.id) ?? "") : "";
  const lines: string[] = [];
  if (run.state === "running")
    lines.push(
      `<div class="act-run-line act-run-action" data-run-action="${esc(run.id)}"${action ? "" : " hidden"}>${esc(action)}</div>`,
    );
  if (run.state === "queued")
    lines.push(
      `<div class="act-run-line">${esc(run.reason || t("lib.activity.run.queued_wait"))}${run.wait === "room" ? fixLink(quotaLink()) : ""}</div>`,
    );
  if (run.state === "not_started")
    lines.push(
      runErrorHTML(run.reason || t("lib.activity.run.not_started_why"), causeFix(run.cause, ctx)),
    );
  if (run.state === "out_of_turns") {
    const turns = turnsText(run);
    lines.push(runErrorHTML(turns || t("lib.activity.run.used_every_turn")));
  }
  if (run.state === "stopped")
    lines.push(`<div class="act-run-line">${esc(stoppedText(run, ctx.viewer))}</div>`);
  if (run.state === "failed") {
    const exit = run.exit !== null ? t("lib.activity.run.exit", { code: run.exit }) : "";
    lines.push(runErrorHTML([exit, errorLine].filter(Boolean).join(". ")));
  }
  return lines;
}

export function runCardHTML(run: RunInfo, ctx: ActivityCtx, errorLine = ""): string {
  const stage = run.stage ? ctx.stageLabel(run.stage) : "";
  const agent = run.agent || t("lib.activity.the_agent");
  const pending = run.state === "queued" || run.state === "not_started";
  const attempt = run.agent
    ? stage
      ? t("lib.activity.run.attempt_title", { agent, stage })
      : t("lib.activity.run.attempt_title_nostage", { agent })
    : stage
      ? t("lib.activity.run.attempt_title_noagent", { stage })
      : t("lib.activity.run.attempt_title_noagent_nostage");
  const title =
    run.state === "queued"
      ? stage
        ? t("lib.activity.run.queued_title", { agent, stage })
        : t("lib.activity.run.queued_title_nostage", { agent })
      : run.state === "not_started"
        ? attempt
        : stage
          ? t("lib.activity.run.title", { number: run.number, agent, stage })
          : t("lib.activity.run.title_nostage", { number: run.number, agent });
  const dur = pending
    ? ""
    : run.state === "running"
      ? `<span class="act-run-dur" data-running-since="${esc(run.startedAt)}">${esc(runningForText(run.startedAt, ctx.now))}</span>`
      : `<span class="act-run-dur">${esc(t("lib.activity.run.took", { duration: runDurationText(run, ctx.now) }))}</span>`;
  const lines = runNoticesHTML(run, ctx, errorLine);
  const id = esc(run.id);
  const btns = [
    pending
      ? ""
      : actButton(t("lib.activity.run.view_log"), { "data-run-log": run.id }, { icon: "terminal" }),
    pending
      ? ""
      : actButton(
          t("lib.activity.run.open_chat"),
          { "data-run-chat": run.id },
          { icon: "message" },
        ),
    run.state === "queued"
      ? actButton(
          t("lib.activity.run.cancel_wait"),
          { "data-run-cancel": run.id, "data-run-agent": run.agent || "", "data-run-stage": stage },
          { icon: "x", danger: true },
        )
      : "",
    run.state === "running"
      ? actButton(
          t("lib.activity.run.stop"),
          { "data-run-stop": run.id, "data-run-n": run.number },
          { icon: "power", danger: true },
        )
      : "",
  ].join("");
  return `<div class="act-run act-run-${run.state}" data-run="${id}">
      <div class="act-run-head">
        <span class="act-run-title">${esc(title)}</span>
        ${runPillHTML(run)}
        ${dur}
        ${timeHTML(run.startedAt, ctx.now)}
      </div>
      ${lines.join("")}
      <div class="act-actions">${btns}</div>
    </div>`;
}

const SYS_ICON: Record<SystemEntry["kind"], string> = {
  stage: "i-arrow-right",
  run: "i-check-circle",
  dep: "i-lock",
  subtask: "i-list",
  note: "i-info",
  other: "i-circle",
};

function systemText(e: SystemEntry, ctx: ActivityCtx): string {
  if (e.kind === "run" && e.run) {
    const r = e.run;
    const agent = r.agent || t("lib.activity.the_agent");
    const duration = runDurationText(r, ctx.now);
    const text = r.stage
      ? t("lib.activity.sys.run", { agent, stage: ctx.stageLabel(r.stage), duration })
      : t("lib.activity.sys.run_nostage", { agent, duration });
    return `<span class="act-sys-text">${esc(text)}</span>${actButton(t("lib.activity.run.view_log"), { "data-run-log": r.id })}`;
  }
  if (e.kind === "note" && e.note) {
    return `<span class="act-sys-text">${esc(excerpt(e.note.body ?? "", 240))}</span>${fixLink(noteLink(e.note.key))}`;
  }
  if (e.kind === "subtask" && e.child) {
    return `<span class="act-sys-text">${esc(t("lib.activity.sys.subtask_added"))}</span><a class="act-sys-link" href="#task-${esc(e.child.id)}" data-open-task="${esc(e.child.id)}">${esc(e.child.title)}</a>`;
  }
  const ev = e.event;
  if (!ev) return "";
  const p = ev.payload ?? {};
  if (e.kind === "stage" && typeof p.from === "string" && typeof p.to === "string") {
    return `<span class="act-sys-text">${esc(
      t("lib.activity.sys.stage", { from: ctx.stageLabel(p.from), to: ctx.stageLabel(p.to) }),
    )}</span>`;
  }
  if (ev.kind === "pr_opened") {
    const url = typeof p.url === "string" ? p.url : (ev.summary ?? "");
    return `<span class="act-sys-text">${esc(prOpenedText(p))}</span>${url ? `<a class="act-sys-link" href="${esc(url)}" target="_blank" rel="noopener">${esc(prRef(url))}</a>` : ""}`;
  }
  const line = ctx.eventLine?.(ev) ?? "";
  if (line) return `<span class="act-sys-text">${esc(line)}</span>`;
  const label = ctx.eventLabel(ev.kind);
  // preview_url comes from an agent: only link plain http(s) addresses.
  if (ev.kind === "preview_url" && typeof p.url === "string" && /^https?:\/\//.test(p.url)) {
    return `<span class="act-sys-text">${esc(label)}</span><a class="act-sys-link" href="${esc(p.url)}" target="_blank" rel="noopener noreferrer">${esc(p.url)}</a>`;
  }
  // The daemon writes a merge's summary in the merger's language; build the line from the payload
  // instead.
  if (ev.kind === "branch_merged" && typeof p.branch === "string" && typeof p.target === "string") {
    const commit = typeof p.commit === "string" ? p.commit.slice(0, 7) : "";
    const text = commit
      ? t("lib.activity.sys.merged", { branch: p.branch, target: p.target, commit })
      : t("lib.activity.sys.merged_nocommit", { branch: p.branch, target: p.target });
    const who = ev.actor ? `<span class="act-sys-sum">${esc(ev.actor)}</span>` : "";
    return `<span class="act-sys-text">${esc(text)}</span>${who}`;
  }
  const own = ctx.eventText?.(ev) ?? "";
  const said = own !== "" ? own : (ev.summary ?? "");
  const summary = said && said !== label ? said : "";
  return `<span class="act-sys-text">${esc(label)}</span>${summary ? `<span class="act-sys-sum">${esc(summary)}</span>` : ""}`;
}

// A system entry's text and links, without the line's icon and time.
export function systemTextHTML(e: SystemEntry, ctx: ActivityCtx): string {
  return systemText(e, ctx);
}

export function systemLineHTML(e: SystemEntry, ctx: ActivityCtx): string {
  return `<div class="act-sys" data-sys="${esc(e.id)}">${icoSvg(SYS_ICON[e.kind])}${systemText(e, ctx)}${timeHTML(e.at, ctx.now)}</div>`;
}

export function groupHTML(
  g: { id: string; at: string; entries: SystemEntry[] },
  open: boolean,
  ctx: ActivityCtx,
): string {
  const id = esc(g.id);
  const body = open
    ? `<div class="act-group-body" id="${id}-body">${g.entries.map((e) => systemLineHTML(e, ctx)).join("")}</div>`
    : "";
  return `<div class="act-group">
      <button class="act-group-btn" type="button" data-group="${id}" aria-expanded="${open}" aria-controls="${id}-body">${icoSvg(open ? "i-chevron-down" : "i-chevron-right")}<span class="act-sys-text">${esc(groupSummary(g.entries))}</span><span class="act-day">${esc(dayLabel(g.entries[0]?.at ?? g.at, ctx.now))}</span></button>
      ${body}
    </div>`;
}

export function threadNoteHTML(c: StreamComment, ctx: ActivityCtx): string {
  const entry: SystemEntry = { id: c.id, at: c.created_at, kind: "note", note: c };
  return `<div class="act-sys act-sys-reply" id="comment-${esc(c.id)}">${icoSvg("i-info")}${systemText(entry, ctx)}${timeHTML(c.created_at, ctx.now)}</div>`;
}

export function foldRowHTML(hidden: number): string {
  return `<button class="act-fold" type="button" data-stream-more>${icoSvg("i-chevron-down")}${esc(t("lib.activity.show_earlier", { count: hidden }))}</button>`;
}

export function earlierRepliesHTML(rootID: string, hidden: number): string {
  return `<button class="act-fold act-fold-replies" type="button" data-thread-expand="${esc(rootID)}">${esc(t("lib.activity.thread.show_earlier", { count: hidden }))}</button>`;
}

export function excerpt(body: string, max = 90): string {
  const flat = body
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[#*`>]/g, "")
    .replace(/(?<![\p{L}\p{N}])_+|_+(?![\p{L}\p{N}])/gu, "")
    .replace(/\s+/g, " ")
    .trim();
  return flat.length > max ? flat.slice(0, max - 1).trimEnd() + "…" : flat;
}

export function resolvedLineHTML<C extends StreamComment>(
  th: Thread<C>,
  who: string,
  ctx: ActivityCtx,
): string {
  const root = th.root;
  const text = who
    ? t("lib.activity.thread.resolved_line", {
        excerpt: excerpt(root.body ?? ""),
        who,
        count: th.replies.length,
      })
    : t("lib.activity.thread.resolved_line_nobody", {
        excerpt: excerpt(root.body ?? ""),
        count: th.replies.length,
      });
  return listRowHTML({
    size: "sm",
    dim: true,
    cls: "act-resolved",
    attrs: { "data-thread": root.id },
    leadHTML: `<span class="act-resolved-ico">${icoSvg("i-check")}</span>`,
    title: text,
    asideHTML: root.resolved_at ? timeHTML(root.resolved_at, ctx.now) : "",
    actionsHTML: actButton(t("lib.activity.thread.show"), {
      "data-thread-show": root.id,
      "aria-label": t("lib.activity.thread.show_label", {
        excerpt: excerpt(root.body ?? "", 40),
      }),
    }),
  });
}

export interface ThreadActionsInput {
  id: string;
  replies: number;
  resolved: boolean;
  agent: string;
  goneAgent?: string;
  stageAgent?: string;
  canSubtask: boolean;
  canHide?: boolean;
  // The thread is a moment of a stage visit, so Activity can show it.
  seeActivity?: boolean;
}

// The links under a thread's first comment: Reply, Resolve, Send to the agent, Make subtask, Hide,
// and the same moment in Activity.
export function threadActionsHTML(a: ThreadActionsInput): string {
  const btn = (attrs: Attrs, label: string, title?: string) =>
    buttonHTML({ label, variant: "link", cls: "act-btn", title, attrs });
  const out = [btn({ "data-thread-reply": a.id }, t("lib.activity.thread.reply"))];
  if (a.replies > 0) {
    out.push(
      btn(
        { "data-thread-resolve": a.id, "data-resolved": a.resolved ? "false" : "true" },
        a.resolved ? t("lib.activity.thread.reopen") : t("lib.activity.thread.resolve"),
      ),
    );
    if (!a.resolved) {
      // aria-disabled, not disabled, so the title still shows on hover.
      out.push(
        a.agent
          ? btn(
              { "data-thread-apply": a.id },
              t("lib.activity.thread.send_to", { agent: a.agent }),
              t("lib.activity.thread.send_to_title", { agent: a.agent }),
            )
          : btn(
              { "data-thread-apply": a.id, "aria-disabled": "true" },
              a.stageAgent && !a.goneAgent
                ? t("lib.activity.thread.send_to", { agent: a.stageAgent })
                : t("lib.activity.thread.send_to_stage"),
              a.goneAgent
                ? t("lib.activity.thread.send_to_agent_gone", { agent: a.goneAgent })
                : t("lib.activity.thread.send_to_never_ran"),
            ),
      );
    }
  }
  if (a.canSubtask)
    out.push(btn({ "data-thread-subtask": a.id }, t("lib.activity.thread.make_subtask")));
  if (a.canHide) out.push(btn({ "data-thread-hide": a.id }, t("lib.activity.thread.hide")));
  if (a.seeActivity)
    out.push(btn({ "data-see-activity": a.id }, t("lib.activity.thread.see_activity")));
  return `<div class="cm-ft">${out.join("")}</div>`;
}

export function applyFormHTML(rootID: string, agent: string, note: string): string {
  return composerHTML({
    variant: "thread",
    label: t("lib.activity.thread.apply_note_label", { agent }),
    placeholder: t("lib.activity.thread.apply_note_placeholder"),
    value: note,
    audience: t("lib.activity.thread.apply_hint", { agent }),
    cancel: true,
    cancelAttrs: { "data-thread-apply-cancel": rootID },
    submitLabel: t("lib.activity.thread.send_to", { agent }),
    ready: true,
    submitAttrs: { "data-thread-apply-send": rootID },
    inputCls: "thread-apply-note",
    cls: "thread-apply",
    attrs: { "data-thread-apply-form": rootID },
  });
}

export function filterBarHTML(current: ActivityFilter, label: string): string {
  const names: Record<ActivityFilter, string> = {
    all: t("lib.activity.filter.all"),
    comments: t("lib.activity.filter.comments"),
    runs: t("lib.activity.filter.runs"),
  };
  return segmentedHTML(
    "act-filter",
    ACTIVITY_FILTERS.map((f) => ({ value: f, label: names[f] })),
    current,
    { size: "sm", id: "act-filter", label },
  );
}

export function listedQuestions(qs: TaskQuestion[] | undefined): TaskQuestion[] {
  const shown = (qs ?? []).filter((q) => questionItems(q).length > 0);
  const at = (q: TaskQuestion) => q.created_at ?? "";
  const open = shown
    .filter((q) => q.status === "open")
    .sort((a, b) => (at(a) < at(b) ? -1 : at(a) > at(b) ? 1 : 0));
  const rest = shown
    .filter((q) => q.status !== "open")
    .sort((a, b) => (at(a) < at(b) ? 1 : at(a) > at(b) ? -1 : 0));
  return [...open, ...rest];
}

export interface QuestionTally {
  total: number;
  pending: number;
}

export function questionTally(qs: TaskQuestion[] | undefined): QuestionTally {
  const listed = listedQuestions(qs);
  return { total: listed.length, pending: listed.filter((q) => q.status === "open").length };
}

export function questionHeadline(q: TaskQuestion): { header: string; text: string; more: number } {
  const items = questionItems(q);
  const first = items.at(0);
  const text =
    (first?.question ?? "")
      .split("\n")
      .map((l) => l.trim())
      .find(Boolean) ?? "";
  return { header: (first?.header ?? "").trim(), text, more: Math.max(0, items.length - 1) };
}

export function questionRowBodyId(id: string): string {
  return `q-row-${id}`;
}

export interface QuestionCardOptions {
  expanded: boolean;
  stageLabel: (id: string) => string;
  // The agent's avatar, and the avatar of the person who answered.
  agentAvatarHTML: string;
  personAvatarHTML: string;
  // Who answered, as the viewer reads it ("You" for themselves).
  answeredBy: string;
  answeredByViewer: boolean;
  // An open question's answer form; replies under the question beyond the answer.
  formHTML?: string;
  repliesHTML?: string;
}

function questionStateText(q: TaskQuestion, o: QuestionCardOptions): string {
  if (q.status === "open") return t("lib.activity.question.waiting");
  if (q.status === "canceled") return t("lib.activity.question.canceled");
  const when = q.answered_at ? fmtDayClock(q.answered_at) : "";
  if (o.answeredByViewer) return t("lib.activity.question.you_answered", { when });
  return o.answeredBy
    ? t("lib.activity.question.who_answered", { who: o.answeredBy, when })
    : t("lib.activity.question.answered_at", { when });
}

function questionAnswerText(q: TaskQuestion, i: number): string {
  const a = recordedAnswer(q).answers.find((x) => x.question_index === i);
  if (!a) return "";
  return [(a.selected ?? []).join(", "), a.other ?? ""].filter(Boolean).join(" · ");
}

// One agent question as a card: a row with the agent, how many questions, their headers and
// whether it waits for the person; opened, what it asked and the answer in violet, or the form to
// answer it.
export function questionCardHTML(q: TaskQuestion, o: QuestionCardOptions): string {
  const items = questionItems(q);
  const open = q.status === "open";
  const bodyId = questionRowBodyId(q.id);
  const named = (q.agent ?? "").trim();
  const agent = named !== "" ? named : t("lib.activity.question.unnamed");
  const heads = items
    .map((it) => (it.header ?? "").trim())
    .filter(Boolean)
    .join(" · ");
  const row = `<button class="q-row" type="button" data-q-toggle="${esc(q.id)}" aria-expanded="${o.expanded}" aria-controls="${esc(bodyId)}">${o.agentAvatarHTML}<span class="nm">${esc(agent)}</span>${tagHTML(
    t("lib.activity.question.count", { count: items.length }),
    {
      size: "sm",
      tone: "special",
      icon: isBlocker(q) ? "i-alert" : "i-help",
      cls: "cm-tag cm-tag-dec",
    },
  )}<span class="q-heads">${esc(heads)}</span><span class="q-state${open ? " wait" : ""}">${esc(questionStateText(q, o))}</span><span class="chev">${icoSvg("i-chevron-right")}</span></button>`;
  const info = [
    isBlocking(q)
      ? t("lib.activity.question.stopped_waited")
      : t("lib.activity.question.kept_working"),
    q.stage ? t("lib.activity.question.in_stage", { stage: o.stageLabel(q.stage) }) : "",
    q.created_at ? t("lib.activity.question.asked", { when: fmtDayClock(q.created_at) }) : "",
  ]
    .filter(Boolean)
    .map(esc)
    .join(" · ");
  const see = q.comment_id
    ? ` · ${buttonHTML({ label: t("lib.activity.thread.see_activity"), variant: "link", attrs: { "data-see-activity": q.comment_id } })}`
    : "";
  const asked = items
    .map((it, i) => {
      const answer = questionAnswerText(q, i);
      const given =
        q.status === "answered"
          ? `<div class="a">${o.personAvatarHTML}<span>${esc(answer || t("lib.activity.question.no_answer"))}</span></div>`
          : "";
      const offered =
        q.status === "answered" && (it.options ?? []).length
          ? `<details class="q-opts"><summary>${esc(t("lib.activity.question.options"))}</summary><ul>${(it.options ?? []).map((op) => `<li><b>${esc(op.label)}</b> <span class="q-muted">${esc(op.description ?? "")}</span></li>`).join("")}</ul></details>`
          : "";
      const head = it.header ? `<div class="h">${esc(it.header)}</div>` : "";
      return `<div class="q">${head}<div class="qq">${esc(it.question)}</div>${given}${offered}</div>`;
    })
    .join("");
  const note = recordedAnswer(q).note;
  const noteHTML =
    q.status === "answered" && note
      ? `<div class="a note">${o.personAvatarHTML}<span>${esc(note)}</span></div>`
      : "";
  const what = open && o.formHTML ? o.formHTML : asked + noteHTML;
  const body = `<div class="q-body" id="${esc(bodyId)}"${o.expanded ? "" : " hidden"}><div class="q-info">${info}${see}</div>${what}${o.repliesHTML ?? ""}</div>`;
  return `<div class="qcard${open ? " q-open" : ""}" data-question-row="${esc(q.id)}">${row}${body}</div>`;
}
