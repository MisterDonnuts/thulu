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
import { fmtDate } from "./time";
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

export function metaHTML(m: MetaInput, ctx: ActivityCtx): string {
  const line = authorLineHTML(
    {
      name: m.name,
      kind: m.kind,
      at: m.at,
      stage: m.stage ? ctx.stageLabel(m.stage) : undefined,
    },
    ctx.now,
  );
  return `<div class="act-meta">${line}</div>`;
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

export function runCardHTML(run: RunInfo, ctx: ActivityCtx, errorLine = ""): string {
  const st = RUN_STATE[run.state];
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
        ${badgeHTML(t(runStateWord(run)), runBadgeTone(st.tone), {
          shape: "pill",
          icon: st.icon || undefined,
          pulse: !st.icon,
          attrs: { "data-run-state": run.state },
        })}
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
}

export function threadActionsHTML(a: ThreadActionsInput): string {
  const btn = (attrs: Attrs, label: string, title?: string) => actButton(label, attrs, { title });
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
  return `<div class="act-actions">${out.join("")}</div>`;
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
    system: t("lib.activity.filter.system"),
  };
  return segmentedHTML(
    "act-filter",
    ACTIVITY_FILTERS.map((f) => ({ value: f, label: names[f] })),
    current,
    { size: "sm", id: "act-filter", label },
  );
}

export function questionWhoText(agent: string, blocking: boolean): string {
  if (blocking)
    return agent
      ? t("lib.activity.question.waiting", { agent })
      : t("lib.activity.question.waiting_unnamed");
  return agent
    ? t("lib.activity.question.asked_working", { agent })
    : t("lib.activity.question.asked_working_unnamed");
}

export function questionPinHeadHTML(
  agent: string,
  at: string,
  ctx: ActivityCtx,
  blocking = true,
): string {
  const who = questionWhoText(agent, blocking);
  return `<div class="act-q-head">${icoSvg("i-help")}<span class="act-q-who">${esc(who)}</span>${timeHTML(at, ctx.now)}<span class="act-q-pin">${esc(t("lib.activity.question.pinned"))}</span></div>`;
}
