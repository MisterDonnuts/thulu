// Filter query names (status, board_id, project_id, q, uncovered) must equal GET /test-cases
// params: the page reuses its own query string.

import { t } from "../i18n";
import { authorLabel, type Viewer } from "./activity";
import { caseHash } from "./evidence-view";
import {
  badgeHTML,
  buttonHTML,
  dotHTML,
  esc,
  groupHeadHTML as uiGroupHeadHTML,
  icoSvg,
  tableRowHTML,
  type TableColumn,
} from "./ui";
import {
  STATUS_ORDER,
  caseAnchor,
  casePriorityTagHTML,
  caseProgressHTML,
  runAgoLabel,
  statusLabel,
  statusLook,
  statusPillHTML,
  summarize,
  taskAnchor,
  type TestCase,
  type TestCaseRun,
  type TestCaseStatus,
  type TestCaseSummary,
} from "./test-cases";

export interface QAFilters {
  status: TestCaseStatus[];
  boardId: string;
  projectId: string;
  q: string;
  uncovered: boolean;
}

export interface QARow extends TestCase {
  task_title: string;
  task_stage: string;
  board_id: string;
  last_run: TestCaseRun | null;
  runs_count: number;
}

export interface QAResponse {
  test_cases: QARow[];
  counts: Partial<Record<TestCaseStatus, number>>;
  covered_counts?: Partial<Record<TestCaseStatus, number>>;
}

export interface TaskGroup {
  taskId: string;
  boardId: string;
  title: string;
  stage: string;
  rows: QARow[];
}

export const EMPTY_FILTERS: QAFilters = {
  status: [],
  boardId: "",
  projectId: "",
  q: "",
  uncovered: false,
};

const isStatus = (s: string): s is TestCaseStatus => (STATUS_ORDER as string[]).includes(s);

export function filtersFromQuery(search: string): QAFilters {
  const p = new URLSearchParams(search);
  const status = (p.get("status") ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(isStatus);
  return {
    status: STATUS_ORDER.filter((s) => status.includes(s)),
    boardId: p.get("board_id") ?? "",
    projectId: p.get("project_id") ?? "",
    q: p.get("q") ?? "",
    uncovered: p.get("uncovered") === "1",
  };
}

export function filtersToQuery(f: QAFilters): string {
  const p = new URLSearchParams();
  const status = STATUS_ORDER.filter((s) => f.status.includes(s));
  if (status.length) p.set("status", status.join(","));
  if (f.boardId) p.set("board_id", f.boardId);
  if (f.projectId) p.set("project_id", f.projectId);
  const q = f.q.trim();
  if (q) p.set("q", q);
  if (f.uncovered) p.set("uncovered", "1");
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function testCasesURL(f: QAFilters): string {
  return `/test-cases${filtersToQuery(f)}`;
}

export function toggleStatus(f: QAFilters, s: TestCaseStatus): QAFilters {
  const on = f.status.includes(s);
  const status = on ? f.status.filter((x) => x !== s) : [...f.status, s];
  return { ...f, status: STATUS_ORDER.filter((x) => status.includes(x)) };
}

export function groupByTask(rows: QARow[]): TaskGroup[] {
  const groups = new Map<string, TaskGroup>();
  for (const r of rows) {
    let g = groups.get(r.task_id);
    if (!g) {
      g = {
        taskId: r.task_id,
        boardId: r.board_id,
        title: r.task_title,
        stage: r.task_stage,
        rows: [],
      };
      groups.set(r.task_id, g);
    }
    g.rows.push(r);
  }
  return [...groups.values()];
}

type StatusCounts = QAResponse["counts"];

function sumOf(counts: StatusCounts, status: TestCaseStatus[]): number {
  return (status.length ? status : STATUS_ORDER).reduce((n, s) => n + (counts[s] ?? 0), 0);
}

export function countsSummary(counts: StatusCounts, covered: StatusCounts = {}): TestCaseSummary {
  const byStatus = Object.fromEntries(STATUS_ORDER.map((s) => [s, counts[s] ?? 0])) as Record<
    TestCaseStatus,
    number
  >;
  return { total: sumOf(counts, []), covered: sumOf(covered, []), byStatus };
}

export function matchedTotal(summary: TestCaseSummary, status: TestCaseStatus[]): number {
  return sumOf(summary.byStatus, status);
}

export function matchedCovered(covered: StatusCounts, status: TestCaseStatus[]): number {
  return sumOf(covered, status);
}

export function qaColumns(): TableColumn[] {
  return [
    { label: t("evidence.columns.key"), mono: true, width: "shrink" },
    { label: t("evidence.tasks.columns.title"), width: "fill" },
    { label: t("evidence.columns.priority"), width: "shrink" },
    { label: t("evidence.tasks.columns.status"), width: "shrink" },
    { label: t("evidence.tasks.columns.covered"), width: "shrink" },
    { label: t("evidence.tasks.columns.last_run") },
    { label: t("evidence.tasks.columns.runs"), num: true },
    { label: t("evidence.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}

export const QA_COLUMNS = qaColumns().length;

export function groupHeadHTML(g: TaskGroup, stageLabel: string): string {
  const s = summarize(g.rows);
  return uiGroupHeadHTML({
    as: "tr",
    cols: QA_COLUMNS,
    band: true,
    title: g.title || g.taskId,
    href: taskAnchor(g.taskId, g.boardId),
    extraHTML:
      badgeHTML(stageLabel || g.stage, "neutral", { shape: "pill" }) +
      caseProgressHTML(s, { cls: "qa-group-bar" }) +
      `<span class="qa-group-count">${esc(t("lib.qa_view.group_count", { pass: s.byStatus.pass, total: s.total }))}</span>`,
    attrs: { "data-task": g.taskId },
  });
}

// Minute resolution only: the page skips the repaint while the HTML is unchanged, so seconds would
// rebuild the table and drop focus on every refresh.
export function lastRunHTML(run: TestCaseRun | null, viewer?: Viewer | null): string {
  if (!run) return `<span class="qa-never">${esc(t("lib.qa_view.never_run"))}</span>`;
  const who = run.author
    ? ` · ${esc(authorLabel({ author: run.author, source: "user" }, viewer))}`
    : "";
  return `<span class="qa-verdict">${dotHTML(statusLook(run.verdict).dot)}${esc(statusLabel(run.verdict))}</span>
    <span class="qa-when" title="${esc(run.ran_at)}">${esc(runAgoLabel(run.ran_at))}${who}</span>`;
}

export interface LibraryCtx {
  keys: ReadonlyMap<string, string>;
  canAdd: boolean;
}

const NO_LIBRARY: LibraryCtx = { keys: new Map(), canAdd: false };

function libraryCellHTML(r: QARow, lib: LibraryCtx): string {
  const key = lib.keys.get(`${r.task_id}/${r.key}`);
  if (key)
    return `<a class="qa-lib" href="${esc(caseHash(key))}">${esc(t("lib.qa_view.in_library", { key }))}</a>`;
  if (!lib.canAdd) return "";
  return buttonHTML({
    label: t("lib.qa_view.add_to_library"),
    size: "sm",
    attrs: { "data-act": "to-library", "data-task": r.task_id, "data-key": r.key },
  });
}

export function caseRowHTML(
  r: QARow,
  lib: LibraryCtx = NO_LIBRARY,
  viewer?: Viewer | null,
): string {
  const coveredLabel = esc(t("lib.qa_view.coverage.covered"));
  const uncoveredLabel = esc(t("lib.qa_view.coverage.not_covered"));
  const covered = r.covered
    ? `<span class="qa-covered" title="${coveredLabel}" aria-label="${coveredLabel}">${icoSvg("i-check")}</span>`
    : `<span class="qa-uncovered" title="${uncoveredLabel}" aria-label="${uncoveredLabel}">—</span>`;
  const href = esc(caseAnchor(r.task_id, r.key, r.board_id));
  return tableRowHTML(
    qaColumns(),
    {
      cls: "qa-row",
      attrs: { "data-case": r.id, "data-status": r.status },
      cells: [
        r.key,
        { html: `<a class="qa-title" href="${href}">${esc(r.title)}</a>` },
        { html: casePriorityTagHTML(r.priority) },
        { html: statusPillHTML(r.status) },
        { html: covered },
        { html: lastRunHTML(r.last_run, viewer) },
        r.runs_count,
        {
          html: `<span class="qa-actions">${libraryCellHTML(r, lib)}<a class="qa-open" href="${href}">${esc(t("lib.qa_view.open"))}</a></span>`,
        },
      ],
    },
    { stack: true },
  );
}

export function tableBodyHTML(
  groups: TaskGroup[],
  stageLabels: Map<string, string>,
  lib: LibraryCtx = NO_LIBRARY,
  viewer?: Viewer | null,
): string {
  return groups
    .map(
      (g) =>
        groupHeadHTML(g, stageLabels.get(g.stage) ?? "") +
        g.rows.map((r) => caseRowHTML(r, lib, viewer)).join(""),
    )
    .join("");
}
