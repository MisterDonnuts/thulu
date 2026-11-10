import { t } from "../i18n";
import { authorLabel, type Viewer } from "./activity";
import { slugifySection } from "./markdown";
import { attachmentIDFromURL, mediaKind } from "./media";
import { fmtDate } from "./time";
import {
  STATUS_ORDER,
  casePriorityTagHTML,
  caseProgressHTML,
  priorityLabel,
  runAgoLabel,
  statusLabel,
  statusPillClasses,
  statusPillHTML,
  summarize,
  type TestCase,
  type TestCasePriority,
  type TestCaseRun,
  type TestCaseStatus,
} from "./test-cases";
import { missingLine, type RunAgent, type RunCasesBlock } from "./test-case-run";
import {
  attrsHTML,
  buttonHTML,
  calloutHTML,
  checkGroupHTML,
  checkHTML,
  disclosureHTML,
  emptyStateHTML,
  esc,
  fieldControlAttrs,
  fieldErrorHTML,
  fieldHTML,
  formErrorHTML,
  icoSvg,
  mdHTML,
  panelHTML,
  sectionHeadHTML,
  tagHTML,
  type Attrs,
} from "./ui";

export interface TcReport {
  id: string;
  title: string;
  body: string;
}

export interface TcAttachment {
  id: string;
  filename: string;
  mime_type?: string;
}

export interface TestCasesInput {
  cases: TestCase[];
  attachments: TcAttachment[];
  comments: Array<{ id: string }>;
  reports?: TcReport[];
  body: string;
}

export interface TestCasesDeps {
  md: (src: string) => string;
  attachmentURL: (id: string) => string;
  openKeys: ReadonlySet<string>;
  openReports: ReadonlySet<string>;
  runCases?: { block: RunCasesBlock; reason: string };
  viewer?: Viewer | null;
  readOnly?: boolean;
}

const PRIORITIES: TestCasePriority[] = ["low", "medium", "high", "critical"];

export function hasTestCasesSection(body: string): boolean {
  let fence = false;
  for (const line of body.split("\n")) {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence;
    if (fence) continue;
    const m = /^##\s+(.+?)\s*#*\s*$/.exec(line);
    if (m && slugifySection(m[1]) === "test-cases") return true;
  }
  return false;
}

function offersImport(input: TestCasesInput): boolean {
  return input.cases.length === 0 && hasTestCasesSection(input.body);
}

export function offersRemoveSection(input: Pick<TestCasesInput, "cases" | "body">): boolean {
  return input.cases.length > 0 && hasTestCasesSection(input.body);
}

export function setByHand(c: Pick<TestCase, "status" | "runs">): boolean {
  const latest = newestFirst(c.runs ?? []).at(0);
  return latest ? latest.verdict !== c.status : c.status !== "pending";
}

export function testCasesSignature(input: TestCasesInput): string {
  const atts = new Set(input.attachments.map((a) => a.id));
  const comments = new Set(input.comments.map((c) => c.id));
  const reports = new Map((input.reports ?? []).map((rep) => [rep.id, rep]));
  const report = (id: string) => {
    const rep = reports.get(id);
    return rep ? `${rep.title.length}:${rep.body.length}` : input.reports ? "gone" : "?";
  };
  const refs = (r: TestCaseRun) =>
    [r.video_attachment_id, r.take_attachment_id, ...r.capture_attachment_ids]
      .map((id) => (id && atts.has(id) ? 1 : 0))
      .join("") +
    (r.comment_id && comments.has(r.comment_id) ? 1 : 0) +
    (r.report_id ? report(r.report_id) : "");
  return JSON.stringify({
    imp: offersImport(input),
    rm: offersRemoveSection(input),
    cases: input.cases.map((c) => [
      c.key,
      c.title,
      c.description,
      c.steps,
      c.expected,
      c.priority,
      c.status,
      c.covered,
      c.position,
      (c.runs ?? []).map((r) => `${r.id}:${refs(r)}`),
    ]),
  });
}

export function plainReportTitle(title: string): string {
  return title.replace(/^(?:\p{Extended_Pictographic}️?\s*)+/u, "").trim();
}

const STATUS_ICON: Record<TestCaseStatus, string> = {
  pass: "i-check-circle",
  fail: "i-x-circle",
  blocked: "i-alert",
  skipped: "i-minus",
  pending: "i-circle",
};

const TALLY = {
  pending: "lib.test_cases.progress.pending",
  pass: "lib.test_cases.progress.pass",
  fail: "lib.test_cases.progress.fail",
  blocked: "lib.test_cases.progress.blocked",
  skipped: "lib.test_cases.progress.skipped",
} as const satisfies Record<TestCaseStatus, string>;

function statusIconHTML(s: TestCaseStatus): string {
  const known = s in STATUS_ICON;
  const status = known ? s : "pending";
  return `<span class="tc-status tc-${status}" role="img" aria-label="${esc(statusLabel(status))}" title="${esc(statusLabel(status))}">${icoSvg(STATUS_ICON[status])}</span>`;
}

function newestFirst(runs: TestCaseRun[]): TestCaseRun[] {
  return [...runs].sort((a, b) => (a.ran_at < b.ran_at ? 1 : a.ran_at > b.ran_at ? -1 : 0));
}

export function testCasesBlockHTML(input: TestCasesInput, deps: TestCasesDeps): string {
  const { cases } = input;
  const sum = summarize(cases);
  const ro = !!deps.readOnly;
  const importBtn =
    !ro && offersImport(input)
      ? buttonHTML({
          label: t("lib.test_cases_drawer.buttons.import"),
          variant: "ghost",
          size: "sm",
          title: t("lib.test_cases_drawer.hints.import"),
          attrs: { "data-tc-act": "import" },
        })
      : "";
  const run = deps.runCases;
  const runBtn =
    !ro && run && cases.length
      ? buttonHTML({
          label: t("lib.test_cases_drawer.buttons.run"),
          size: "sm",
          icon: "play",
          disabled: !!run.block,
          title: run.block ? run.reason : undefined,
          attrs: { "data-tc-act": "run" },
        })
      : "";
  // Tooltips never show on a phone, so a blocked "Run cases" also explains itself in the block.
  const runNote =
    !ro && run?.block && cases.length
      ? calloutHTML({
          tone: "warn",
          icon: "warn",
          body: run.reason,
          actionsHTML: buttonHTML({
            label: t("lib.test_cases_drawer.buttons.open_agents"),
            href: "/agents/",
            variant: "ghost",
            size: "sm",
          }),
          cls: "tc-run-off",
          attrs: { role: "status" },
        })
      : "";
  const removeNote =
    !ro && offersRemoveSection(input)
      ? calloutHTML({
          tone: "warn",
          body: t("lib.test_cases_drawer.notices.section_left"),
          actionsHTML: buttonHTML({
            label: t("lib.test_cases_drawer.buttons.remove_section"),
            variant: "ghost",
            size: "sm",
            attrs: { "data-tc-act": "remove-section" },
          }),
          attrs: { role: "status" },
        })
      : "";
  const tally = STATUS_ORDER.filter((s) => sum.byStatus[s] > 0)
    .map(
      (s) =>
        `<span class="tc-tally-item tc-${s}">${icoSvg(STATUS_ICON[s])}${esc(t(TALLY[s], { count: sum.byStatus[s] }))}</span>`,
    )
    .join("");
  const list =
    cases.length === 0
      ? emptyStateHTML({
          size: "inline",
          dashed: true,
          body: t("lib.test_cases_drawer.empty.cases"),
        })
      : panelHTML({
          tag: "div",
          size: "sm",
          flush: true,
          cls: "tc-list",
          bodyCls: "row-list",
          bodyHTML: cases.map((c, i) => caseHTML(c, i, cases.length, input, deps)).join(""),
        });
  const actions =
    importBtn +
    runBtn +
    (ro
      ? ""
      : buttonHTML({
          label: t("lib.test_cases_drawer.buttons.add"),
          size: "sm",
          icon: "plus",
          attrs: { "data-tc-act": "add" },
        }));
  return `${sectionHeadHTML({
    title: t("lib.test_cases_drawer.labels.title"),
    count: cases.length || undefined,
    asideHTML: tally ? `<div class="tc-tally">${tally}</div>` : "",
    actionsHTML: actions,
  })}
      ${cases.length ? caseProgressHTML(sum, { size: "xs" }) : ""}
      ${runNote}
      ${removeNote}
      <div class="tc-add-slot"></div>
      ${list}`;
}

function rowPriorityHTML(p: TestCasePriority): string {
  if (p !== "critical" && p !== "high") return "";
  return casePriorityTagHTML(p, { title: t("lib.test_cases_drawer.labels.priority") });
}

function caseHTML(
  c: TestCase,
  index: number,
  count: number,
  input: TestCasesInput,
  deps: TestCasesDeps,
): string {
  const key = esc(c.key);
  const runs = newestFirst(c.runs ?? []);
  const last = runs.at(0);
  const lastLabel = last
    ? t("lib.test_cases_drawer.labels.last_run", {
        runs: t("lib.test_cases_drawer.labels.runs_count", { count: runs.length }),
        ago: runAgoLabel(last.ran_at),
      })
    : t("lib.test_cases_drawer.labels.not_run");
  const meta = `<span class="tc-meta">
            ${setByHand(c) ? tagHTML(t("lib.test_cases_drawer.labels.by_hand"), { tone: "muted", dashed: true, size: "sm", title: t("lib.test_cases_drawer.hints.by_hand"), cls: "tc-by-hand" }) : ""}
            ${rowPriorityHTML(c.priority)}
            <span class="tc-last"${last ? ` title="${esc(fmtDate(last.ran_at))}"` : ""}>${esc(lastLabel)}</span>
          </span>`;
  return disclosureHTML({
    variant: "row",
    cls: `tc-case tc-case-${c.status}`,
    id: `tc-${c.key}`,
    open: deps.openKeys.has(c.key),
    attrs: { "data-key": c.key },
    summaryHTML: `<span class="tc-row">${statusIconHTML(c.status)}<span class="tc-main"><span class="tc-key">${key}</span><span class="tc-title disclosure-title">${esc(c.title)}</span></span>${meta}</span>`,
    bodyHTML: `<div class="tc-detail">
          ${deps.readOnly ? readOnlyBarHTML(c) : caseBarHTML(c, index, count)}
          <div class="tc-edit-slot"></div>
          ${specHTML(c, deps)}
          ${runsHTML(runs, input, deps)}
        </div>`,
  });
}

function caseBarHTML(c: TestCase, index: number, count: number): string {
  return `<div class="tc-bar">
            <label class="tc-bar-field"><span>${esc(t("lib.test_cases_drawer.labels.status"))}</span>${statusSelectHTML(c)}</label>
            <span class="tc-bar-field"><span>${esc(t("lib.test_cases_drawer.labels.priority"))}</span><span class="tc-bar-value">${esc(priorityLabel(c.priority))}</span></span>
            ${checkHTML({
              label: t("lib.test_cases_drawer.labels.covered"),
              checked: c.covered,
              inputAttrs: {
                "data-tc-act": "covered",
                "aria-label": t("lib.test_cases_drawer.labels.covered_aria", { key: c.key }),
              },
              attrs: { title: t("lib.test_cases_drawer.hints.covered") },
            })}
            <span class="tc-bar-actions">
              ${buttonHTML({ iconOnly: true, icon: "edit", label: t("lib.test_cases_drawer.buttons.edit", { key: c.key }), title: t("common.buttons.edit"), attrs: { "data-tc-act": "edit" } })}
              ${buttonHTML({ iconOnly: true, icon: "arrow-up", label: t("lib.test_cases_drawer.buttons.move_up", { key: c.key }), disabled: index === 0, attrs: { "data-tc-act": "up" } })}
              ${buttonHTML({ iconOnly: true, icon: "arrow-down", label: t("lib.test_cases_drawer.buttons.move_down", { key: c.key }), disabled: index === count - 1, attrs: { "data-tc-act": "down" } })}
              ${buttonHTML({ iconOnly: true, icon: "trash", variant: "danger", label: t("lib.test_cases_drawer.buttons.delete", { key: c.key }), title: t("common.buttons.delete"), attrs: { "data-tc-act": "delete" } })}
            </span>
          </div>`;
}

function readOnlyBarHTML(c: TestCase): string {
  const covered = c.covered
    ? `<span class="tc-bar-field"><span>${esc(t("lib.test_cases_drawer.labels.covered"))}</span></span>`
    : "";
  return `<div class="tc-bar">
            <span class="tc-bar-field"><span>${esc(t("lib.test_cases_drawer.labels.status"))}</span>${statusPillHTML(c.status)}</span>
            <span class="tc-bar-field"><span>${esc(t("lib.test_cases_drawer.labels.priority"))}</span><span class="tc-bar-value">${esc(priorityLabel(c.priority))}</span></span>
            ${covered}
          </div>`;
}

function statusSelectHTML(c: TestCase): string {
  const opts = STATUS_ORDER.map(
    (s) => `<option value="${s}"${s === c.status ? " selected" : ""}>${statusLabel(s)}</option>`,
  ).join("");
  return `<select class="${statusPillClasses(c.status, "tc-status-select")}" data-tc-act="status" aria-label="${esc(t("lib.test_cases_drawer.labels.status_of", { key: c.key }))}">${opts}</select>`;
}

function subHeadHTML(title: string): string {
  return sectionHeadHTML({ title, level: 4 });
}

function specHTML(c: TestCase, deps: TestCasesDeps): string {
  const block = (label: string, src: string, cls: string) =>
    src.trim()
      ? `<section class="tc-spec-part ${cls}">${subHeadHTML(label)}${mdHTML(deps.md(src), { compact: true, cls: "tc-md" })}</section>`
      : "";
  const desc = block(t("lib.test_cases_drawer.fields.description"), c.description, "tc-spec-desc");
  const steps = block(t("lib.test_cases_drawer.fields.steps"), c.steps, "tc-spec-steps");
  const expected = block(
    t("lib.test_cases_drawer.fields.expected"),
    c.expected,
    "tc-spec-expected",
  );
  if (!desc && !steps && !expected) {
    return emptyStateHTML({ size: "inline", body: t("lib.test_cases_drawer.empty.details") });
  }
  return `<div class="tc-spec">${desc}${steps || expected ? `<div class="tc-spec-pair">${steps}${expected}</div>` : ""}</div>`;
}

function runsHTML(runs: TestCaseRun[], input: TestCasesInput, deps: TestCasesDeps): string {
  if (!runs.length) {
    return `<section class="tc-runs">${subHeadHTML(t("lib.test_cases_drawer.labels.latest_run"))}${emptyStateHTML({ size: "inline", body: t("lib.test_cases_drawer.empty.runs") })}</section>`;
  }
  const [latest, ...older] = runs;
  const olderHTML = older.length
    ? disclosureHTML({
        variant: "code",
        cls: "tc-older",
        summary: t("lib.test_cases_drawer.labels.older_runs", { count: older.length }),
        bodyHTML: `<div class="tc-older-runs">${older.map((r) => runHTML(r, input, deps)).join("")}</div>`,
      })
    : "";
  return `<section class="tc-runs">
        ${subHeadHTML(t("lib.test_cases_drawer.labels.latest_run"))}
        ${runHTML(latest, input, deps)}
        ${olderHTML}
      </section>`;
}

export function runHTML(r: TestCaseRun, input: TestCasesInput, deps: TestCasesDeps): string {
  const att = (id: string) => (id ? input.attachments.find((a) => a.id === id) : undefined);
  const links: string[] = [];

  if (r.take_attachment_id) {
    const a = att(r.take_attachment_id);
    links.push(
      a
        ? `<a class="tc-link" href="${esc(deps.attachmentURL(a.id))}" target="_blank" rel="noopener noreferrer" title="${esc(a.filename)}">${icoSvg("i-file")}${esc(t("lib.test_cases_drawer.links.take"))}</a>`
        : `<span class="tc-gone">${esc(t("lib.test_cases_drawer.gone.take"))}</span>`,
    );
  }
  const report = r.report_id ? input.reports?.find((rep) => rep.id === r.report_id) : undefined;
  if (r.report_id) {
    links.push(
      input.reports && !report
        ? `<span class="tc-gone">${esc(t("lib.test_cases_drawer.gone.report"))}</span>`
        : `<button class="tc-link" type="button" data-tc-open-report="${esc(r.report_id)}">${icoSvg("i-arrow-up-right")}${esc(t("lib.test_cases_drawer.links.open_report"))}</button>`,
    );
  } else if (r.comment_id) {
    links.push(
      input.comments.some((c) => c.id === r.comment_id)
        ? `<button class="tc-link" type="button" data-tc-report="${esc(r.comment_id)}">${icoSvg("i-message")}${esc(t("lib.test_cases_drawer.links.report"))}</button>`
        : `<span class="tc-gone">${esc(t("lib.test_cases_drawer.gone.report"))}</span>`,
    );
  }

  const ago = runAgoLabel(r.ran_at);
  const verdict = (r.verdict in STATUS_ICON ? r.verdict : "pending") as TestCaseStatus;
  return `<article class="tc-run tc-run-${esc(verdict)}">
          <header class="tc-run-head">
            ${statusPillHTML(r.verdict)}
            <time class="tc-run-time" datetime="${esc(r.ran_at)}" title="${esc(fmtDate(r.ran_at))}">${esc(ago)}</time>
            ${r.author ? `<span class="tc-run-author">${esc(authorLabel({ author: r.author, source: "user" }, deps.viewer))}</span>` : ""}
            ${links.length ? `<span class="tc-run-links">${links.join("")}</span>` : ""}
          </header>
          ${r.observed ? `<div class="tc-observed"><span class="tc-observed-label">${esc(t("lib.test_cases_drawer.labels.observed"))}</span><span class="tc-observed-text">${esc(r.observed)}</span></div>` : ""}
          ${r.note ? mdHTML(deps.md(r.note), { compact: true, cls: "tc-md tc-note" }) : ""}
          ${evidenceHTML(r, input, deps)}
          ${report ? reportHTML(r, report, deps) : ""}
        </article>`;
}

function evidenceHTML(r: TestCaseRun, input: TestCasesInput, deps: TestCasesDeps): string {
  const att = (id: string) => (id ? input.attachments.find((a) => a.id === id) : undefined);
  const ids = [r.video_attachment_id, ...r.capture_attachment_ids].filter((id) => id && att(id));
  const set = esc(ids.join(","));
  const thumbs: string[] = [];
  if (r.video_attachment_id) {
    const v = att(r.video_attachment_id);
    if (!v) {
      thumbs.push(`<span class="tc-gone">${esc(t("lib.test_cases_drawer.gone.video"))}</span>`);
    } else if (mediaKind(v.mime_type, v.filename) === "video") {
      // t=0.1 makes the browser paint the first frame instead of a black box.
      thumbs.push(`<button class="tc-thumb tc-thumb-video" type="button" data-lightbox="${esc(v.id)}" data-lightbox-set="${set}" title="${esc(v.filename)}" aria-label="${esc(t("lib.test_cases_drawer.links.video", { name: v.filename }))}">
            <video muted preload="metadata" playsinline src="${esc(deps.attachmentURL(v.id))}#t=0.1"></video>
            <span class="tc-play">${icoSvg("i-play")}</span>
          </button>`);
    } else {
      thumbs.push(fileChipHTML(v, deps));
    }
  }
  const caps = r.capture_attachment_ids;
  const shown = caps.map(att).filter((a): a is TcAttachment => !!a);
  for (const a of shown) {
    thumbs.push(
      mediaKind(a.mime_type, a.filename) === "image"
        ? `<button class="tc-thumb" type="button" data-lightbox="${esc(a.id)}" data-lightbox-set="${set}" title="${esc(a.filename)}" aria-label="${esc(a.filename)}"><img alt="" src="${esc(deps.attachmentURL(a.id))}"></button>`
        : fileChipHTML(a, deps),
    );
  }
  if (caps.length && shown.length < caps.length) {
    thumbs.push(`<span class="tc-gone">${esc(t("lib.test_cases_drawer.gone.captures"))}</span>`);
  }
  return thumbs.length ? `<div class="tc-evidence">${thumbs.join("")}</div>` : "";
}

function fileChipHTML(a: TcAttachment, deps: TestCasesDeps): string {
  return `<a class="tc-file" href="${esc(deps.attachmentURL(a.id))}" target="_blank" rel="noopener noreferrer" title="${esc(a.filename)}">${icoSvg("i-file")}<span>${esc(a.filename)}</span></a>`;
}

export function withoutEmbeds(body: string, attachmentIds: readonly string[]): string {
  const shown = new Set(attachmentIds.filter(Boolean));
  if (shown.size === 0) return body;
  return body
    .split("\n")
    .filter((line) => {
      const m = /^\s*!\[[^\]]*\]\(([^)\s]+)\)\s*$/.exec(line);
      return !m || !shown.has(attachmentIDFromURL(m[1]));
    })
    .join("\n");
}

function reportHTML(r: TestCaseRun, rep: TcReport, deps: TestCasesDeps): string {
  const title = plainReportTitle(rep.title);
  return disclosureHTML({
    variant: "code",
    ruled: true,
    cls: "tc-report",
    open: deps.openReports.has(r.id),
    attrs: { "data-run": r.id },
    summaryHTML: `<span class="tc-report-label">${esc(t("lib.test_cases_drawer.links.report"))}</span>${title ? ` <span class="tc-report-title">${esc(title)}</span>` : ""}`,
    bodyHTML: mdHTML(
      deps.md(withoutEmbeds(rep.body, [r.video_attachment_id, ...r.capture_attachment_ids])),
      { compact: true, cls: "tc-md" },
    ),
  });
}

export function reportsNewestFirst<R extends { created_at: string }>(reports: R[]): R[] {
  return [...reports].sort((a, b) =>
    a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0,
  );
}

export interface ReportFiles<A, R> {
  report: R;
  files: A[];
}

export function groupFilesByReport<
  A extends { id: string },
  R extends TcReport & { created_at: string },
>(files: A[], reports: R[]): { groups: Array<ReportFiles<A, R>>; loose: A[] } {
  const byId = new Map(files.map((f) => [f.id, f]));
  const taken = new Set<string>();
  const groups: Array<ReportFiles<A, R>> = [];
  for (const report of reportsNewestFirst(reports)) {
    const mine: A[] = [];
    for (const m of report.body.matchAll(/\]\(([^)\s]+)\)/g)) {
      const id = attachmentIDFromURL(m[1]);
      const f = id ? byId.get(id) : undefined;
      if (f && !taken.has(f.id)) {
        taken.add(f.id);
        mine.push(f);
      }
    }
    if (mine.length) groups.push({ report, files: mine });
  }
  return { groups, loose: files.filter((f) => !taken.has(f.id)) };
}

export type TcFormIds = Record<
  "title" | "priority" | "agent" | "url" | "compare" | "cases" | "error",
  string
>;

export function tcFormIds(base: string): TcFormIds {
  return {
    title: `${base}-title`,
    priority: `${base}-priority`,
    agent: `${base}-agent`,
    url: `${base}-url`,
    compare: `${base}-compare`,
    cases: `${base}-cases`,
    error: `${base}-error`,
  };
}

export type TcFormField = "title" | "url" | "compare" | "cases";

export function caseFormProblem(f: Pick<CaseFields, "title">): TcFormField | null {
  return f.title.trim() ? null : "title";
}

const httpURL = (u: string) => /^https?:\/\/\S+$/i.test(u);

export function runCasesProblem(f: {
  url: string;
  compare: string;
  keys: readonly string[];
}): TcFormField | null {
  if (!httpURL(f.url.trim())) return "url";
  if (f.compare.trim() && !httpURL(f.compare.trim())) return "compare";
  return f.keys.length ? null : "cases";
}

function formFootHTML(errorId: string, submit: string, submitOff = false): string {
  return `${formErrorHTML({ id: errorId })}
        <div class="form-actions">
          ${buttonHTML({ label: t("common.buttons.cancel"), variant: "ghost", size: "sm", attrs: { "data-tc-act": "cancel" } })}
          ${buttonHTML({ label: submit, variant: "primary", size: "sm", type: "submit", disabled: submitOff })}
        </div>`;
}

function inputHTML(id: string, attrs: Attrs): string {
  return `<input${attrsHTML({ id, ...attrs })} />`;
}

export function runCasesFormHTML(
  cases: TestCase[],
  agents: RunAgent[],
  picked: string,
  base = "tc-run",
): string {
  const ids = tcFormIds(base);
  const opts = agents
    .map(
      (a) =>
        `<option value="${esc(a.type)}"${a.type === picked ? " selected" : ""}>${esc(a.missing.length ? t("lib.test_cases_drawer.run.agent_missing", { name: a.name }) : a.name)}</option>`,
    )
    .join("");
  const missing = missingLine(agents.find((a) => a.type === picked));
  const agentA11y = fieldControlAttrs({ for: ids.agent, error: true, errorText: missing });
  const casesErr = `${ids.cases}-error`;
  const boxes = checkGroupHTML({
    layout: "list",
    labelledBy: `${ids.cases}-label`,
    attrs: { "aria-describedby": casesErr },
    items: cases.map((c) => ({
      name: "case",
      value: c.key,
      checked: true,
      labelHTML: `<span class="tc-key">${esc(c.key)}</span> ${esc(c.title)}`,
    })),
  });
  return `<form class="tc-form tc-run-form form-stack" novalidate>
        ${fieldHTML({
          label: t("lib.test_cases_drawer.run.agent"),
          for: ids.agent,
          errorId: `${ids.agent}-error`,
          error: missing || undefined,
          control: `<select${attrsHTML({ id: ids.agent, name: "agent", ...agentA11y })}>${opts}</select>`,
        })}
        <div class="form-row">
          ${fieldHTML({
            label: t("lib.test_cases_drawer.run.url"),
            for: ids.url,
            grow: true,
            errorId: `${ids.url}-error`,
            control: inputHTML(ids.url, {
              name: "url",
              type: "url",
              required: true,
              autocomplete: "url",
              placeholder: t("lib.test_cases_drawer.run.url_hint"),
            }),
          })}
          ${fieldHTML({
            label: t("lib.test_cases_drawer.run.compare"),
            for: ids.compare,
            optional: true,
            grow: true,
            errorId: `${ids.compare}-error`,
            control: inputHTML(ids.compare, {
              name: "compare",
              type: "url",
              autocomplete: "url",
              placeholder: t("lib.test_cases_drawer.run.compare_hint"),
            }),
          })}
        </div>
        <div class="field tc-run-cases" id="${esc(ids.cases)}">
          <span class="field-label" id="${esc(ids.cases)}-label">${esc(t("lib.test_cases_drawer.run.cases"))}</span>
          ${boxes}
          ${fieldErrorHTML({ id: casesErr })}
        </div>
        ${formFootHTML(ids.error, t("lib.test_cases_drawer.run.start"), !!missing)}
      </form>`;
}

export type CaseFields = Pick<
  TestCase,
  "title" | "priority" | "description" | "steps" | "expected"
>;

export function testCaseFormHTML(c?: Partial<CaseFields>, base = "tc-add"): string {
  const ids = tcFormIds(base);
  const prio = c?.priority ?? "medium";
  const opts = PRIORITIES.map(
    (p) => `<option value="${p}"${p === prio ? " selected" : ""}>${priorityLabel(p)}</option>`,
  ).join("");
  const area = (name: keyof CaseFields, label: string, hint: string) => {
    const id = `${base}-${name}`;
    return fieldHTML({
      label,
      for: id,
      optional: true,
      control: `<textarea${attrsHTML({ id, name, rows: "3", placeholder: hint })}>${esc(c?.[name] ?? "")}</textarea>`,
    });
  };
  return `<form class="tc-form form-stack" novalidate>
        <div class="form-row">
          ${fieldHTML({
            label: t("lib.test_cases_drawer.fields.title"),
            for: ids.title,
            grow: true,
            errorId: `${ids.title}-error`,
            control: inputHTML(ids.title, {
              name: "title",
              type: "text",
              required: true,
              maxlength: "200",
              autocomplete: "off",
              value: c?.title ?? "",
            }),
          })}
          ${fieldHTML({
            label: t("lib.test_cases_drawer.labels.priority"),
            for: ids.priority,
            control: `<select id="${esc(ids.priority)}" name="priority">${opts}</select>`,
          })}
        </div>
        ${area("description", t("lib.test_cases_drawer.fields.description"), t("lib.test_cases_drawer.hints.description"))}
        ${area("steps", t("lib.test_cases_drawer.fields.steps"), t("lib.test_cases_drawer.hints.steps"))}
        ${area("expected", t("lib.test_cases_drawer.fields.expected"), t("lib.test_cases_drawer.hints.expected"))}
        ${formFootHTML(ids.error, t("common.buttons.save"))}
      </form>`;
}

export function movedPosition(
  cases: Array<Pick<TestCase, "key">>,
  key: string,
  dir: -1 | 1,
): number | null {
  const i = cases.findIndex((c) => c.key === key);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= cases.length) return null;
  return j + 1;
}
