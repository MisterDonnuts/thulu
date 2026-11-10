// Every value from the API must go through esc(): these strings are set with innerHTML.

import { t } from "../i18n";
import { labelTagHTML } from "./chips";
import {
  batchCounts,
  batchDuration,
  caseRunsOn,
  cardStatus,
  causeLabel,
  checksSummary,
  clockLabel,
  deployLabel,
  durationLabel,
  envKind,
  envLabel,
  isActiveBatch,
  isActiveRun,
  isNotRun,
  isRecorded,
  isSandboxFailed,
  isSandboxStarting,
  latestPoster,
  needsInitScript,
  pendingCount,
  permission,
  reasonLabel,
  sameName,
  SANDBOX_STEPS,
  sandboxStep,
  sandboxStepLabel,
  scheduleLabel,
  scriptFailure,
  sentence,
  shortCommit,
  statusHint,
  statusLabel,
  statusTone,
  triggerLabel,
  whenLabel,
  caseHash,
  type BatchCounts,
  type CaseGroup,
  type Chapter,
  type EnvGroup,
  type EvBatch,
  type EvCase,
  type EvCheck,
  type EvEnvironment,
  type EvFailure,
  type EvRun,
  type Permission,
} from "./evidence-view";
import { safeUrl } from "./safe-url";
import { casePriorityTagHTML, type TestCasePriority } from "./test-cases";
import {
  badgeHTML,
  buttonHTML,
  calloutHTML,
  cardHTML,
  codeBlockHTML,
  disclosureHTML,
  dotHTML,
  emptyRowHTML,
  emptyStateHTML,
  esc,
  groupHeadHTML,
  icoSvg,
  listRowHTML,
  progressBarHTML,
  stepListHTML,
  tableHTML,
  tableRowHTML,
  type BadgeEdge,
  type BadgeTone,
  type ButtonOptions,
  type DotTone,
  type ListRowOptions,
  type ProgressSegment,
  type StepItem,
  type TableColumn,
  type TableRow,
} from "./ui";

export interface ListCtx {
  envs: EvEnvironment[];
  projects: Map<string, string>;
  envName?: (env: EvEnvironment, c: EvCase) => string;
  envFirst?: string;
  canWrite: boolean;
  writeReason?: string;
  artifactURL: (id: string) => string;
}

export function offProps(p: Permission): { disabled?: boolean; title?: string } {
  return p.allowed ? {} : { disabled: true, title: reasonLabel(p.reason) };
}

export type EvTone = ReturnType<typeof statusTone> | "env";

const TONE_LOOK: Record<EvTone, { tone: BadgeTone; edge?: BadgeEdge; dot: DotTone }> = {
  ok: { tone: "ok", dot: "ok" },
  bad: { tone: "danger", dot: "danger" },
  err: { tone: "accent", dot: "special" },
  warn: { tone: "warn", dot: "warn" },
  live: { tone: "info", dot: "accent" },
  env: { tone: "warn", edge: "solid", dot: "warn" },
  idle: { tone: "muted", dot: "neutral" },
  none: { tone: "neutral", edge: "hollow", dot: "empty" },
};

export interface EvPillOptions {
  title?: string;
  raised?: boolean;
  cls?: string;
}

export function evPillHTML(label: string, tone: EvTone, opts: EvPillOptions = {}): string {
  const look = TONE_LOOK[tone];
  return badgeHTML(label, look.tone, { shape: "pill", edge: look.edge, ...opts });
}

export function statusPillHTML(status: string, opts: EvPillOptions = {}): string {
  return evPillHTML(statusLabel(status), statusTone(status), {
    title: statusHint(status) || undefined,
    ...opts,
  });
}

export function statusDotHTML(status: string): string {
  const tone = statusTone(status);
  return dotHTML(TONE_LOOK[tone].dot, { pulse: tone === "live" });
}

function priorityHTML(p: string): string {
  return casePriorityTagHTML(p as TestCasePriority);
}

function tagsHTML(tags: string[] | null | undefined): string {
  if (!tags?.length) return "";
  return `<span class="ev-tags">${tags.map((tag) => labelTagHTML(tag, { size: "sm" })).join("")}</span>`;
}

export function prodBadgeHTML(env: Pick<EvEnvironment, "is_production">): string {
  return env.is_production ? badgeHTML(t("evidence.envs.production"), "warn") : "";
}

export function caseEnvs(c: EvCase, envs: EvEnvironment[], first = ""): EvEnvironment[] {
  const usable = (env: EvEnvironment) =>
    envKind(env) !== "sandbox" || !env.sandbox || env.sandbox.available;
  const mine = envs.filter(
    (env) => (caseRunsOn(c.project_id, env) && usable(env)) || c.latest?.[env.id],
  );
  const ran = mine.filter((env) => c.latest?.[env.id]);
  const ordered = [...ran, ...mine.filter((env) => !c.latest?.[env.id])];
  const top = ordered.findIndex((env) => env.id === first);
  return top > 0 ? [ordered[top], ...ordered.filter((_, i) => i !== top)] : ordered;
}

export function envResultsHTML(
  c: EvCase,
  envs: EvEnvironment[],
  max = 3,
  envName: (env: EvEnvironment, c: EvCase) => string = envLabel,
  first = "",
): string {
  const list = caseEnvs(c, envs, first);
  if (!list.length) return "";
  const shown = list.slice(0, max);
  const items = shown.map((env) => {
    const name = envName(env, c);
    const r = c.latest?.[env.id];
    if (!r)
      return `<li class="ev-env-res ev-env-none">${statusDotHTML("")}<span class="ev-env-name">${esc(name)}</span><span class="ev-when">${esc(t("evidence.cases.not_run_here"))}</span></li>`;
    const when = isActiveRun(r.status) ? statusLabel(r.status) : whenLabel(r.finished_at);
    const label = t("evidence.cases.env_result_aria", {
      env: name,
      status: statusLabel(r.status),
      when,
    });
    return `<li class="ev-env-res" title="${esc(label)}">${statusDotHTML(r.status)}<span class="ev-env-name">${esc(name)}</span><span class="ev-when">${esc(when)}</span></li>`;
  });
  const more = list.length - shown.length;
  if (more > 0)
    items.push(
      `<li class="ev-env-more">${esc(t("evidence.cases.more_envs", { count: more }))}</li>`,
    );
  return `<ul class="ev-envs">${items.join("")}</ul>`;
}

function mainAction(c: EvCase, ctx: Pick<ListCtx, "canWrite" | "writeReason">): ButtonOptions {
  const off = offProps({ allowed: ctx.canWrite, reason: ctx.writeReason ?? "viewer" });
  return isRecorded(c)
    ? {
        label: t("evidence.actions.run_on"),
        size: "sm",
        icon: "play",
        attrs: { "data-act": "run" },
        ...off,
      }
    : {
        label: t("evidence.actions.record"),
        size: "sm",
        icon: "bot",
        attrs: { "data-act": "record" },
        ...off,
      };
}

function menuButton(c: EvCase, canWrite: boolean): ButtonOptions | null {
  if (!canWrite) return null;
  return {
    iconOnly: true,
    icon: "more",
    size: "lg",
    label: t("evidence.actions.more_aria", { key: c.key }),
    attrs: { "data-act": "menu", "aria-haspopup": "menu", "aria-expanded": "false" },
  };
}

function caseActions(c: EvCase, ctx: ListCtx): ButtonOptions[] {
  const menu = menuButton(c, ctx.canWrite);
  return menu ? [mainAction(c, ctx), menu] : [mainAction(c, ctx)];
}

export function casePillHTML(
  c: EvCase,
  ctx: Pick<ListCtx, "envs" | "envFirst" | "envName">,
  opts: EvPillOptions = {},
): string {
  const { status, env } = cardStatus(c, ctx.envs, ctx.envFirst);
  if (!env) return statusPillHTML(status, opts);
  const name = ctx.envName ? ctx.envName(env, c) : envLabel(env);
  const title = t("evidence.cases.status_on", { status: statusLabel(status), env: name });
  return statusPillHTML(status, { ...opts, title });
}

function caseMetaHTML(c: EvCase, project: string | undefined): string {
  return `<div class="ev-card-meta"><span class="ev-key">${esc(c.key)}</span>${priorityHTML(c.priority)}${project ? `<span class="ev-project">${icoSvg("i-folder")}${esc(project)}</span>` : ""}</div>`;
}

export function caseCardHTML(c: EvCase, ctx: ListCtx): string {
  const poster = latestPoster(c);
  const media = poster
    ? `<img src="${esc(ctx.artifactURL(poster))}" alt="" loading="lazy" decoding="async" />`
    : `<span class="ev-card-ph">${icoSvg(isRecorded(c) ? "i-play" : "i-bot", "ico ico-xl")}<span>${esc(isRecorded(c) ? t("evidence.cases.no_video_yet") : t("evidence.cases.not_recorded"))}</span></span>`;
  const project = c.project_id ? ctx.projects.get(c.project_id) : "";
  const [main, ...rest] = caseActions(c, ctx).map((a) => buttonHTML(a));
  return cardHTML({
    title: c.title,
    href: caseHash(c.key),
    mediaHTML: media + casePillHTML(c, ctx, { raised: true, cls: "ev-card-pill" }),
    bodyHTML:
      caseMetaHTML(c, project) +
      tagsHTML(c.tags) +
      envResultsHTML(c, ctx.envs, 3, ctx.envName, ctx.envFirst),
    footHTML: main + (rest.length ? `<span class="grow"></span>${rest.join("")}` : ""),
    cls: "ev-card",
    attrs: { "data-case": c.id, "data-key": c.key },
  });
}

// ev-col-project: the page hides this column under a project heading.
export function caseListColumns(): TableColumn[] {
  return [
    { label: t("evidence.columns.key"), mono: true, width: "shrink" },
    { label: t("evidence.columns.case"), width: "fill" },
    { label: t("evidence.columns.project"), cls: "ev-col-project" },
    { label: t("evidence.columns.priority"), width: "shrink" },
    { label: t("evidence.columns.result") },
    { label: t("evidence.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}

export const CASE_LIST_COLUMNS = caseListColumns().length;

export function caseRowHTML(c: EvCase, ctx: ListCtx): string {
  const project = c.project_id ? (ctx.projects.get(c.project_id) ?? "") : "";
  return tableRowHTML(
    caseListColumns(),
    {
      cells: [
        c.key,
        {
          html: `<a class="ev-row-title" href="${esc(caseHash(c.key))}">${esc(c.title)}</a>${tagsHTML(c.tags)}`,
        },
        project,
        { html: priorityHTML(c.priority) },
        { html: casePillHTML(c, ctx) + envResultsHTML(c, ctx.envs, 2, ctx.envName, ctx.envFirst) },
        { actions: caseActions(c, ctx) },
      ],
      attrs: { "data-case": c.id, "data-key": c.key },
    },
    { stack: true },
  );
}

const caseGroupName = (g: CaseGroup): string => g.projectName || t("evidence.picker.no_project");

export function caseGroupsGridHTML(groups: CaseGroup[], ctx: ListCtx): string {
  return groups
    .map(
      (g) =>
        groupHeadHTML({
          level: 2,
          icon: g.projectId ? "folder" : "tt-checklist",
          title: caseGroupName(g),
          meta: t("evidence.picker.cases_count", { count: g.cases.length }),
        }) + g.cases.map((c) => caseCardHTML(c, ctx)).join(""),
    )
    .join("");
}

export function caseGroupsRowsHTML(groups: CaseGroup[], ctx: ListCtx): string {
  return groups
    .map(
      (g) =>
        groupHeadHTML({
          as: "tr",
          cols: CASE_LIST_COLUMNS,
          band: true,
          icon: g.projectId ? "folder" : "tt-checklist",
          title: caseGroupName(g),
          meta: t("evidence.picker.cases_count", { count: g.cases.length }),
        }) + g.cases.map((c) => caseRowHTML(c, ctx)).join(""),
    )
    .join("");
}

export function batchBarHTML(c: BatchCounts): string {
  const segments: ProgressSegment[] = [
    { value: c.passed, tone: "ok", label: t("evidence.counts.passed", { count: c.passed }) },
    { value: c.failed, tone: "danger", label: t("evidence.counts.failed", { count: c.failed }) },
    { value: c.blocked, tone: "warn", label: t("evidence.counts.blocked", { count: c.blocked }) },
    {
      value: c.errored,
      tone: "special",
      label: t("evidence.counts.errored", { count: c.errored }),
    },
  ];
  const pending = pendingCount(c);
  const words = segments.filter((g) => g.value > 0).map((g) => g.label);
  if (c.canceled) words.push(t("evidence.counts.canceled", { count: c.canceled }));
  if (pending) words.push(t("evidence.counts.pending", { count: pending }));
  const label = words.length ? words.join(", ") : t("evidence.counts.none");
  return progressBarHTML(segments, { total: c.total, label });
}

export function countsLine(c: BatchCounts): string {
  const words: string[] = [];
  if (c.passed) words.push(t("evidence.counts.passed", { count: c.passed }));
  if (c.failed) words.push(t("evidence.counts.failed", { count: c.failed }));
  if (c.blocked) words.push(t("evidence.counts.blocked", { count: c.blocked }));
  if (c.errored) words.push(t("evidence.counts.errored", { count: c.errored }));
  if (c.canceled) words.push(t("evidence.counts.canceled", { count: c.canceled }));
  const pending = pendingCount(c);
  if (pending) words.push(t("evidence.counts.pending", { count: pending }));
  return words.join(" · ");
}

const BATCH_STATUS = {
  queued: "evidence.batch_status.queued",
  running: "evidence.batch_status.running",
  done: "evidence.batch_status.done",
  canceled: "evidence.batch_status.canceled",
  error: "evidence.batch_status.error",
} as const;

export function batchStatusHTML(b: EvBatch): string {
  if (isSandboxStarting(b)) return evPillHTML(t("evidence.sandbox.starting"), "live");
  if (isSandboxFailed(b)) return evPillHTML(t("evidence.sandbox.failed_short"), "env");
  const c = batchCounts(b);
  if (b.status === "done") {
    const bad = c.failed + c.errored;
    const tone: EvTone = bad ? "bad" : c.blocked ? "warn" : "ok";
    const word = bad
      ? t("evidence.batch_status.problems")
      : c.blocked
        ? t("evidence.batch_status.blocked")
        : t("evidence.batch_status.all_passed");
    return evPillHTML(word, tone);
  }
  const tone = isActiveBatch(b) ? "live" : b.status === "error" ? "bad" : "none";
  const key = b.status in BATCH_STATUS ? BATCH_STATUS[b.status] : "";
  return evPillHTML(key ? t(key) : b.status, tone);
}

export function sandboxStepsHTML(b: EvBatch): string {
  const now = SANDBOX_STEPS.indexOf(sandboxStep(b.sandbox_phase));
  const steps = SANDBOX_STEPS.map(
    (step, i): StepItem => ({
      title: sandboxStepLabel(step),
      state: i < now ? "done" : i === now ? "now" : "pending",
    }),
  );
  return stepListHTML(steps, {
    inline: true,
    attrs: { "aria-label": t("evidence.sandbox.steps_aria") },
  });
}

export function sourceHTML(b: EvBatch): string {
  if (!b.source_branch) return "";
  const commit = shortCommit(b.source_commit);
  return `<span class="ev-source" title="${esc(t("evidence.sandbox.source_title", { branch: b.source_branch }))}">${icoSvg("i-branch")}<span class="ev-mono">${esc(b.source_branch)}</span>${commit ? `<span class="ev-mono ev-dim">${esc(commit)}</span>` : ""}</span>`;
}

export interface SandboxFix {
  initScriptURL?: string;
  canAdmin?: boolean;
}

function failureNote(reason: string | undefined, fix: SandboxFix): string {
  if (scriptFailure(reason)) {
    const href = safeUrl(fix.initScriptURL ?? "");
    if (!fix.canAdmin || !href)
      return `<p class="ev-dim ev-small">${esc(t("evidence.sandbox.failed_note_ask"))}</p>`;
    return `<p class="ev-dim ev-small">${esc(t("evidence.sandbox.failed_note"))} <a class="ev-link" href="${esc(href)}">${esc(t("evidence.sandbox.edit_script"))}</a></p>`;
  }
  const key =
    reason === "branch_not_found"
      ? "evidence.sandbox.failed_note_branch"
      : reason === "no_room"
        ? "evidence.sandbox.failed_note_busy"
        : "evidence.sandbox.failed_note_octopus";
  return `<p class="ev-dim ev-small">${esc(t(key))}</p>`;
}

export function runnerErrorHTML(
  text: string,
  cause: string | undefined,
  cls: string,
  tone: "danger" | "special" = "danger",
): string {
  const why = causeLabel(cause);
  if (!why) return calloutHTML({ tone, bare: true, body: text, cls });
  const log = codeBlockHTML({ text, multiline: true, maxHeight: "sm", tone: "danger" });
  const details = disclosureHTML({
    variant: "code",
    summary: t("evidence.sandbox.show_details"),
    bodyHTML: log,
  });
  return calloutHTML({ tone, bodyHTML: `<p>${esc(why)}</p>${details}`, cls });
}

export function sandboxFailureHTML(b: EvBatch, canRetry: Permission, fix: SandboxFix = {}): string {
  const why = sentence(b.sandbox_error ?? "");
  const log = b.sandbox_log?.trim();
  return calloutHTML({
    tone: "warn",
    icon: "warn",
    title: t("evidence.sandbox.failed_title"),
    bodyHTML: `<p>${esc(why === "" ? t("evidence.sandbox.failed_unknown") : why)}</p>${failureNote(b.sandbox_reason, fix)}`,
    detail: log
      ? {
          summary: scriptFailure(b.sandbox_reason)
            ? t("evidence.sandbox.show_log")
            : t("evidence.sandbox.show_details"),
          text: b.sandbox_log ?? "",
        }
      : undefined,
    actionsHTML: buttonHTML({
      label: t("evidence.sandbox.retry"),
      size: "sm",
      icon: "refresh",
      attrs: { "data-act": "retry-batch" },
      ...offProps(canRetry),
    }),
    cls: "ev-bringup-fail",
    attrs: { role: "status" },
  });
}

export interface BatchCtx {
  envName: string;
  open: boolean;
  canWrite: boolean;
  canRetry?: Permission;
  runs?: EvRun[];
  log?: string;
  initScriptURL?: string;
  canAdmin?: boolean;
  caseNames: Map<string, { key: string; title: string }>;
}

export function batchHTML(b: EvBatch, ctx: BatchCtx): string {
  const c = batchCounts(b);
  const dur = batchDuration(b);
  const when = whenLabel(b.started_at ?? b.created_at);
  const id = esc(b.id);
  const starting = isSandboxStarting(b);
  const failed = isSandboxFailed(b);
  const cancel =
    ctx.canWrite && isActiveBatch(b)
      ? buttonHTML({
          label: t("evidence.runs.cancel"),
          variant: "ghost",
          size: "sm",
          attrs: { "data-act": "cancel-batch" },
        })
      : "";
  const runs = ctx.open ? batchRunsHTML(b, ctx) : "";
  const progress = starting
    ? sandboxStepsHTML(b)
    : failed
      ? `<span class="ev-batch-counts">${esc(t("evidence.sandbox.not_run_count", { count: c.total }))}</span>`
      : `${batchBarHTML(c)}<span class="ev-batch-counts">${esc(countsLine(c) || t("evidence.counts.none"))}</span>`;
  const cls = [
    "ev-batch",
    isActiveBatch(b) ? "ev-batch-live" : "",
    failed ? "ev-batch-envfail" : "",
  ]
    .filter(Boolean)
    .join(" ");
  const failure = failed
    ? sandboxFailureHTML(
        { ...b, sandbox_log: ctx.log ?? b.sandbox_log },
        ctx.canRetry ?? { allowed: ctx.canWrite, reason: "viewer" },
        { initScriptURL: ctx.initScriptURL, canAdmin: ctx.canAdmin },
      )
    : b.error
      ? runnerErrorHTML(b.error, b.error_cause, "ev-batch-error")
      : "";
  const head = listRowHTML({
    as: "button",
    expanded: ctx.open,
    title: ctx.envName || t("evidence.runs.unknown_env"),
    badgesHTML: batchStatusHTML(b),
    metaHTML: `<span class="ev-batch-trigger">${esc(triggerLabel(b.trigger, b.trigger_ref))}</span>${sourceHTML(b)}`,
    asideHTML: `<span class="ev-batch-time"><span>${esc(when)}</span>${dur ? `<span class="ev-dim">${esc(dur)}</span>` : ""}</span>`,
    cls: "ev-batch-head",
    attrs: { "data-act": "toggle-batch", "aria-controls": `ev-batch-runs-${b.id}` },
  });
  return `<li class="${cls}" data-batch="${id}">
    ${head}
    <div class="ev-batch-progress"><span class="ev-batch-bar">${progress}</span>${cancel}</div>
    ${failure}
    <div class="ev-batch-runs" id="ev-batch-runs-${id}"${ctx.open ? "" : " hidden"}>${runs}</div>
  </li>`;
}

function batchRunsHTML(b: EvBatch, ctx: BatchCtx): string {
  if (!ctx.runs) return emptyRowHTML(undefined, { as: "div", loading: true });
  if (!ctx.runs.length) return emptyRowHTML(t("evidence.runs.no_runs"), { as: "div" });
  const items = ctx.runs.map((r) => `<li>${listRowHTML(batchRun(b, r, ctx))}</li>`);
  return `<ul class="row-list ev-runs">${items.join("")}</ul>`;
}

function batchRun(b: EvBatch, r: EvRun, ctx: BatchCtx): ListRowOptions {
  const known = ctx.caseNames.get(r.case_id);
  const key = r.case_key ?? known?.key ?? "";
  const title = r.case_title ?? known?.title ?? "";
  const notRun = isNotRun(b, r);
  const checks = r.checks?.length ? checksSummary(r.checks) : null;
  const meta = [
    key ? `<span class="ev-key">${esc(key)}</span>` : "",
    esc(notRun ? t("evidence.sandbox.not_run") : statusLabel(r.status)),
    checks
      ? esc(t("evidence.runs.checks_short", { passed: checks.passed, total: checks.total }))
      : "",
    esc(durationLabel(r.duration_ms)),
  ].filter(Boolean);
  return {
    href: caseHash(key || r.case_id, r.id),
    leadHTML: statusDotHTML(notRun ? "" : r.status),
    title: title || key || r.case_id,
    metaHTML: meta.join(" · "),
    aside: r.video_id ? t("evidence.runs.watch") : t("evidence.runs.open"),
    cls: "ev-run",
  };
}

export interface EnvCtx {
  lastBatch?: EvBatch | null;
  canAdmin?: boolean;
}

function envURLHTML(env: EvEnvironment): string {
  const href = safeUrl(env.base_url);
  if (!env.base_url)
    return `<span class="ev-env-url ev-env-nourl">${esc(t("evidence.envs.no_url"))}</span>`;
  if (!href) return `<span class="ev-env-url">${esc(env.base_url)}</span>`;
  return `<a class="ev-env-url" href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(env.base_url)}</a>`;
}

function lastBatchHTML(b: EvBatch | null | undefined): string {
  if (!b) return `<div class="ev-env-last ev-dim">${esc(t("evidence.envs.never_ran"))}</div>`;
  const extra = isSandboxStarting(b)
    ? `<span class="ev-dim">${esc(sandboxStepLabel(sandboxStep(b.sandbox_phase)))}</span>`
    : `<span class="ev-dim">${esc(triggerLabel(b.trigger, b.trigger_ref))}</span>`;
  return `<div class="ev-env-last">${batchStatusHTML(b)}${extra}<span class="ev-dim">${esc(whenLabel(b.started_at ?? b.created_at))}</span></div>`;
}

function loginsFactHTML(env: EvEnvironment): string {
  const n = (env.secret_names ?? []).length;
  const logins = n ? t("evidence.envs.logins_count", { count: n }) : t("evidence.envs.no_logins");
  return `<li>${icoSvg("i-tt-shield")}<span>${esc(logins)}</span></li>`;
}

function limitHTML(env: EvEnvironment): string {
  const edit = permission(env, "edit");
  if (edit.allowed || edit.reason === "viewer") return "";
  return `<p class="ev-env-limit">${icoSvg("i-info")}<span>${esc(reasonLabel(edit.reason))}</span></p>`;
}

function deleteHTML(env: EvEnvironment): string {
  const p = permission(env, "delete");
  const label = envLabel(env);
  if (!p.allowed && p.reason === "delete_on_project" && env.project_id)
    return buttonHTML({
      label: t("evidence.envs.open_project"),
      variant: "ghost",
      size: "sm",
      icon: "arrow-up-right",
      href: `/project/?id=${encodeURIComponent(env.project_id)}#environments`,
      title: reasonLabel(p.reason),
      cls: "ev-env-del",
    });
  if (!p.allowed && p.reason === "sandbox_fixed") return "";
  return buttonHTML({
    iconOnly: true,
    icon: "trash",
    size: "lg",
    variant: "danger",
    label: t("evidence.envs.delete_aria", { name: label }),
    cls: "ev-env-del",
    attrs: { "data-act": "delete-env" },
    ...offProps(p),
  });
}

export function envCardHTML(env: EvEnvironment, ctx: EnvCtx = {}): string {
  const label = envLabel(env);
  const triggers: string[] = [];
  if (env.schedule)
    triggers.push(
      `<li>${icoSvg("i-clock")}<span>${esc(scheduleLabel(env.schedule, env.schedule_tz))}</span></li>`,
    );
  const deploy = deployLabel(env);
  if (deploy) triggers.push(`<li>${icoSvg("i-github")}<span>${esc(deploy)}</span></li>`);
  if (!triggers.length)
    triggers.push(
      `<li class="ev-dim">${icoSvg("i-play")}<span>${esc(t("evidence.envs.manual_only"))}</span></li>`,
    );
  const edit = permission(env, "edit");
  const viewOnly = !edit.allowed;
  const run = permission(env, "run");
  const link = permission(env, "link");
  const runBtn = buttonHTML({
    label: t("evidence.envs.run_here"),
    size: "sm",
    icon: "play",
    attrs: { "data-act": "run-env" },
    ...offProps(run),
  });
  const editBtn = buttonHTML({
    label: viewOnly ? t("evidence.envs.view") : t("common.buttons.edit"),
    variant: "ghost",
    size: "sm",
    icon: viewOnly ? "eye" : "edit",
    attrs: { "data-act": "edit-env" },
  });
  const linkBtn =
    envKind(env) === "standalone" && link.allowed
      ? buttonHTML({
          iconOnly: true,
          icon: "link",
          size: "lg",
          label: t("evidence.link.action_aria", { name: label }),
          title: t("evidence.link.action"),
          cls: "ev-env-link",
          attrs: { "data-act": "link-env" },
        })
      : "";
  const slug =
    env.display_name && !sameName(env.display_name, env.name)
      ? `<span class="ev-env-slug ev-mono">${esc(env.name)}</span>`
      : "";
  return cardHTML({
    title: label,
    headHTML: prodBadgeHTML(env) + slug,
    bodyHTML:
      envURLHTML(env) +
      `<ul class="ev-env-facts">${triggers.join("")}${loginsFactHTML(env)}</ul>` +
      lastBatchHTML(ctx.lastBatch ?? env.last_batch) +
      limitHTML(env),
    footHTML: runBtn + editBtn + linkBtn + deleteHTML(env),
    id: `ev-env-${env.id}`,
    cls: env.is_production ? "ev-env ev-env-prod" : "ev-env",
    attrs: { "data-env": env.id, tabindex: "-1" },
  });
}

export function sandboxReasonHTML(
  reason: string | undefined,
  initScriptURL: string,
  canAdmin = false,
): string {
  const href = safeUrl(initScriptURL);
  const writing = reason === "no_init_script";
  let why = reasonLabel(reason);
  let fix = "";
  if (needsInitScript(reason) && !canAdmin)
    why += ` ${writing ? t("evidence.sandbox.ask_admin_write") : t("evidence.sandbox.ask_admin_fix")}`;
  else if (needsInitScript(reason) && href)
    fix = `<a class="ev-link" href="${esc(href)}">${esc(writing ? t("evidence.sandbox.write_script") : t("evidence.sandbox.edit_script"))}</a>`;
  return calloutHTML({
    icon: "i-info",
    body: why,
    actionsHTML: fix || undefined,
    cls: "ev-sandbox-off",
  });
}

export function sandboxCardHTML(env: EvEnvironment, ctx: EnvCtx = {}): string {
  const sb = env.sandbox;
  const available = sb?.available ?? true;
  const run = permission(env, "run");
  const edit = permission(env, "edit");
  const facts: string[] = [];
  if (sb?.default_branch)
    facts.push(
      `<li>${icoSvg("i-branch")}<span>${esc(t("evidence.sandbox.default_branch", { branch: sb.default_branch }))}</span></li>`,
    );
  if (sb?.port)
    facts.push(
      `<li>${icoSvg("i-monitor")}<span>${esc(t("evidence.sandbox.port", { port: sb.port }))}</span></li>`,
    );
  const off = available
    ? ""
    : sandboxReasonHTML(sb?.reason, sb?.init_script_url ?? "", ctx.canAdmin);
  const runBtn = buttonHTML({
    label: t("evidence.sandbox.run"),
    size: "sm",
    icon: "play",
    attrs: { "data-act": "run-env" },
    ...offProps(run),
  });
  const editBtn = buttonHTML({
    label: edit.allowed ? t("common.buttons.edit") : t("evidence.envs.view"),
    variant: "ghost",
    size: "sm",
    icon: edit.allowed ? "edit" : "eye",
    attrs: { "data-act": "edit-env" },
  });
  return cardHTML({
    title: t("evidence.sandbox.name"),
    headHTML: badgeHTML(t("evidence.sandbox.badge"), "muted"),
    bodyHTML:
      `<p class="ev-env-sub">${esc(t("evidence.sandbox.about", { project: env.project_name ?? "" }))}</p>` +
      off +
      `<ul class="ev-env-facts">${facts.join("")}${loginsFactHTML(env)}</ul>` +
      lastBatchHTML(ctx.lastBatch ?? env.last_batch),
    footHTML: runBtn + editBtn,
    dashed: true,
    off: !available,
    id: `ev-env-${env.id}`,
    cls: "ev-env ev-sandbox",
    attrs: { "data-env": env.id, tabindex: "-1" },
  });
}

export interface EnvGroupCtx {
  lastBatch: (envId: string) => EvBatch | undefined;
  canCreate: Permission;
  canAdmin?: boolean;
  headings: boolean;
}

export function envGroupsHTML(groups: EnvGroup[], ctx: EnvGroupCtx): string {
  return groups
    .map((g) => {
      const cards = g.envs.map((env) => envCardHTML(env, { lastBatch: ctx.lastBatch(env.id) }));
      if (g.projectId && !g.envs.length)
        cards.push(
          emptyStateHTML({
            size: "sm",
            dashed: true,
            body: t("evidence.envs.project_empty"),
            action: {
              label: t("evidence.envs.new"),
              size: "sm",
              icon: "plus",
              attrs: { "data-act": "new-env", "data-project": g.projectId },
              ...offProps(ctx.canCreate),
            },
            cls: "ev-env-blank",
          }),
        );
      if (g.sandbox)
        cards.push(
          sandboxCardHTML(g.sandbox, {
            lastBatch: ctx.lastBatch(g.sandbox.id),
            canAdmin: ctx.canAdmin,
          }),
        );
      const head = !ctx.headings
        ? ""
        : g.projectId
          ? groupHeadHTML({
              level: 2,
              icon: "folder",
              title: g.projectName,
              extraHTML: `<a class="ev-link ev-group-link" href="/project/?id=${encodeURIComponent(g.projectId)}#environments">${esc(t("evidence.envs.open_project"))}</a>`,
            })
          : groupHeadHTML({
              level: 2,
              icon: "server",
              title: t("evidence.envs.other"),
              meta: t("evidence.envs.other_hint"),
            });
      return `<section class="ev-env-group" data-group="${esc(g.projectId)}">${head}<div class="ev-env-grid">${cards.join("")}</div></section>`;
    })
    .join("");
}

export function checksTableHTML(checks: EvCheck[]): string {
  if (!checks.length) return `<p class="ev-dim">${esc(t("evidence.detail.no_checks"))}</p>`;
  const cols: TableColumn[] = [
    { label: t("evidence.detail.col_check"), width: "fill" },
    { label: t("evidence.detail.col_expected") },
    { label: t("evidence.detail.col_observed") },
    { label: t("evidence.detail.col_result"), width: "shrink" },
    { label: t("evidence.detail.col_how"), cls: "ev-dim" },
  ];
  const rows = checks.map(
    (c): TableRow => ({
      tone: c.pass ? undefined : "danger",
      cells: [
        c.label,
        c.expected,
        c.observed,
        { html: statusPillHTML(c.pass ? "pass" : "fail") },
        c.measured ? t("evidence.detail.measured") : t("evidence.detail.observed_by_agent"),
      ],
    }),
  );
  return tableHTML(cols, rows, { compact: true, top: true, stack: true, cls: "ev-checks" });
}

export function failuresHTML(failures: EvFailure[]): string {
  if (!failures.length) return "";
  return `<ul class="ev-failures">${failures
    .map((f) => {
      const step = t("evidence.detail.step", { step: String(f.step) });
      const box = calloutHTML({
        tone: "danger",
        title: `${step} · ${f.label || f.kind}`,
        bodyHTML: f.error ? `<p class="ev-mono">${esc(f.error)}</p>` : undefined,
      });
      return `<li>${box}</li>`;
    })
    .join("")}</ul>`;
}

export function capturesHTML(ids: string[], url: (id: string) => string): string {
  if (!ids.length) return "";
  return `<div class="ev-shots">${ids
    .map(
      (id, i) =>
        `<button class="ev-shot" type="button" data-act="shot" data-index="${i}" aria-label="${esc(t("evidence.detail.capture_aria", { n: i + 1, total: ids.length }))}"><img src="${esc(url(id))}" alt="" loading="lazy" decoding="async" /></button>`,
    )
    .join("")}</div>`;
}

export function chaptersHTML(chapters: Chapter[]): string {
  if (!chapters.length) return "";
  return `<ol class="ev-chapters">${chapters
    .map(
      (c) =>
        `<li><button type="button" data-act="seek" data-at="${c.start}"><span class="ev-chapter-at">${esc(clockLabel(c.start))}</span><span>${esc(c.title)}</span></button></li>`,
    )
    .join("")}</ol>`;
}

export function historyHTML(runs: EvRun[], envs: Map<string, string>, selected: string): string {
  if (!runs.length) return `<p class="ev-dim">${esc(t("evidence.detail.no_history"))}</p>`;
  const cols: TableColumn[] = [
    { label: t("evidence.detail.col_result"), width: "shrink" },
    { label: t("evidence.detail.col_env") },
    { label: t("evidence.detail.col_when"), cls: "ev-dim" },
    { label: t("evidence.detail.col_duration"), cls: "ev-dim" },
    { label: t("evidence.detail.col_trigger"), cls: "ev-dim" },
    { label: t("evidence.detail.col_actions"), hideLabel: true, width: "shrink" },
  ];
  const rows = runs.map((r): TableRow => {
    const on = r.id === selected;
    const when = isActiveRun(r.status)
      ? statusLabel(r.status)
      : whenLabel(r.finished_at ?? r.started_at ?? r.created_at);
    return {
      cls: "ev-hist-row",
      current: on,
      attrs: { "data-run": r.id },
      cells: [
        { html: statusPillHTML(r.status) },
        envs.get(r.environment_id) ?? "",
        when,
        durationLabel(r.duration_ms),
        r.trigger ? triggerLabel(r.trigger, r.trigger_ref ?? "") : "",
        {
          actions: [
            {
              label: on ? t("evidence.detail.showing") : t("evidence.detail.show"),
              variant: "ghost",
              attrs: { "data-act": "pick-run", "aria-current": on ? "true" : undefined },
            },
          ],
        },
      ],
    };
  });
  return tableHTML(cols, rows, { compact: true, stack: true, cls: "ev-history" });
}
