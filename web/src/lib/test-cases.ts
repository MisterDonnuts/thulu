import { t } from "../i18n";
import { agoLabel } from "./task-card";
import {
  badgeClasses,
  badgeHTML,
  priorityTagHTML,
  progressBarHTML,
  type BadgeEdge,
  type BadgeTone,
  type DotTone,
  type PriorityLevel,
  type PriorityTagOptions,
  type ProgressBarOptions,
  type ProgressTone,
} from "./ui";

// "waived": a person decided not to test the case and recorded why (POST /test-cases/waive). It is
// never a pass: the tally and the rows count and draw it on its own.
// "queued": a person gave the agent access (POST /test-cases/access) and the case waits for its
// next run ("Running again"); only the daemon sets it.
export type TestCaseStatus =
  "pending" | "pass" | "waived" | "fail" | "blocked" | "queued" | "skipped";
export type TestCasePriority = "low" | "medium" | "high" | "critical";

export interface TestCaseRun {
  id: string;
  test_case_id: string;
  task_id: string;
  verdict: "pass" | "fail" | "blocked" | "waived";
  observed: string;
  note: string;
  video_attachment_id: string;
  take_attachment_id: string;
  report_id: string;
  comment_id: string;
  capture_attachment_ids: string[];
  // Only on a blocked run: what a person must bring (kinds from GET /test-cases/need-kinds).
  needs?: TestCaseNeed[];
  tried?: "" | "none" | "simulated" | "partial";
  tried_result?: string;
  writes_data?: boolean;
  round?: number;
  vtt_attachment_id?: string;
  author: string;
  ran_at: string;
}

export interface TestCaseNeed {
  kind: string;
  label: string;
  secrets?: Array<{ name: string; label: string }>;
}

export interface TestCase {
  id: string;
  task_id: string;
  key: string;
  title: string;
  description: string;
  steps: string;
  expected: string;
  covers?: string[];
  preconditions?: string;
  priority: TestCasePriority;
  status: TestCaseStatus;
  covered: boolean;
  position: number;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
  runs?: TestCaseRun[];
}

export const STATUS_ORDER: TestCaseStatus[] = [
  "pass",
  "waived",
  "fail",
  "blocked",
  "queued",
  "skipped",
  "pending",
];

// What a person may pick in a case's status select. Waiving takes a reason, so it goes through
// "Skip with a reason" and never through the select; queued comes only from giving access.
export const SETTABLE_STATUS: TestCaseStatus[] = STATUS_ORDER.filter(
  (s) => s !== "waived" && s !== "queued",
);

const STATUS_LABELS: Record<TestCaseStatus, string> = {
  pending: t("lib.test_cases.status.pending"),
  pass: t("lib.test_cases.status.pass"),
  waived: t("lib.test_cases.status.waived"),
  fail: t("lib.test_cases.status.fail"),
  blocked: t("lib.test_cases.status.blocked"),
  queued: t("lib.test_cases.status.queued"),
  skipped: t("lib.test_cases.status.skipped"),
};

const PRIORITY_LABELS: Record<TestCasePriority, string> = {
  low: t("lib.test_cases.priority.low"),
  medium: t("lib.test_cases.priority.medium"),
  high: t("lib.test_cases.priority.high"),
  critical: t("lib.test_cases.priority.critical"),
};

const STATUS_COUNT = {
  pending: "lib.test_cases.progress.pending",
  pass: "lib.test_cases.progress.pass",
  waived: "lib.test_cases.progress.waived",
  fail: "lib.test_cases.progress.fail",
  blocked: "lib.test_cases.progress.blocked",
  queued: "lib.test_cases.progress.queued",
  skipped: "lib.test_cases.progress.skipped",
} as const satisfies Record<TestCaseStatus, string>;

export function statusLabel(s: TestCaseStatus): string {
  return s in STATUS_LABELS ? STATUS_LABELS[s] : s;
}

const STATUS_LOOK: Record<
  TestCaseStatus,
  { tone: BadgeTone; edge?: BadgeEdge; bar: ProgressTone; dot: DotTone }
> = {
  pass: { tone: "ok", bar: "ok", dot: "ok" },
  waived: { tone: "info", edge: "dashed", bar: "info", dot: "info" },
  fail: { tone: "danger", bar: "danger", dot: "danger" },
  blocked: { tone: "warn", bar: "warn", dot: "warn" },
  queued: { tone: "info", bar: "info", dot: "info" },
  skipped: { tone: "neutral", edge: "hollow", bar: "muted", dot: "neutral" },
  pending: { tone: "muted", bar: "faint", dot: "empty" },
};

export function statusLook(s: string): (typeof STATUS_LOOK)[TestCaseStatus] {
  return Object.hasOwn(STATUS_LOOK, s) ? STATUS_LOOK[s as TestCaseStatus] : STATUS_LOOK.pending;
}

export function statusPillClasses(s: string, cls?: string): string {
  const { tone, edge } = statusLook(s);
  return badgeClasses({ tone, edge, shape: "pill", cls });
}

export function statusPillHTML(s: TestCaseStatus): string {
  const { tone, edge } = statusLook(s);
  return badgeHTML(statusLabel(s), tone, { shape: "pill", edge });
}

export function priorityLabel(p: TestCasePriority): string {
  return p in PRIORITY_LABELS ? PRIORITY_LABELS[p] : p;
}

const PRIORITY_LEVEL: Record<TestCasePriority, PriorityLevel> = {
  critical: "urgent",
  high: "high",
  medium: "medium",
  low: "low",
};

export interface CasePriorityTagOptions extends PriorityTagOptions {
  label?: string;
}

export function casePriorityTagHTML(
  p: TestCasePriority,
  opts: CasePriorityTagOptions = {},
): string {
  const level = Object.hasOwn(PRIORITY_LEVEL, p) ? PRIORITY_LEVEL[p] : undefined;
  const { label = priorityLabel(p), ...rest } = opts;
  return level ? priorityTagHTML(label, level, rest) : "";
}

export interface TestCaseSummary {
  total: number;
  covered: number;
  byStatus: Record<TestCaseStatus, number>;
}

export function summarize(cases: Array<Pick<TestCase, "status" | "covered">>): TestCaseSummary {
  const byStatus = { pending: 0, pass: 0, waived: 0, fail: 0, blocked: 0, queued: 0, skipped: 0 };
  let covered = 0;
  for (const c of cases) {
    if (c.status in byStatus) byStatus[c.status]++;
    if (c.covered) covered++;
  }
  return { total: cases.length, covered, byStatus };
}

export function caseProgressHTML(
  summary: TestCaseSummary,
  opts: Pick<ProgressBarOptions, "size" | "cls"> = {},
): string {
  const segments = STATUS_ORDER.map((s) => ({
    value: summary.byStatus[s],
    tone: STATUS_LOOK[s].bar,
    label: t(STATUS_COUNT[s], { count: summary.byStatus[s] }),
  }));
  const label = summary.total ? undefined : t("lib.test_cases.progress.empty");
  return progressBarHTML(segments, { ...opts, label });
}

// The board must ride along: /tasks/ otherwise opens the last-used board, and the drawer only
// refreshes with the board it sits on.
export function taskAnchor(taskId: string, boardId: string, key = ""): string {
  const board = boardId ? `?board=${encodeURIComponent(boardId)}` : "";
  return `/tasks/${board}#task-${taskId}${key ? `/${key}` : ""}`;
}

export function caseAnchor(taskId: string, key: string, boardId: string): string {
  return taskAnchor(taskId, boardId, key);
}

// Cannot collide with a case key: those always carry "-<n>" (task.ValidTestCaseKey).
export const APPROVALS_KEY = "approvals";

// The drawer tab a hash key opens. History was the Activity tab's old name; its links still land
// there. None of these can collide with a case key either.
const TAB_KEYS: Readonly<Record<string, string>> = {
  [APPROVALS_KEY]: "approvals",
  tests: "tests",
  activity: "timeline",
  history: "timeline",
  files: "files",
};

export function tabForHashKey(key: string): string | null {
  return Object.hasOwn(TAB_KEYS, key) ? TAB_KEYS[key] : null;
}

export function approvalsAnchor(taskId: string, boardId: string): string {
  return taskAnchor(taskId, boardId, APPROVALS_KEY);
}

export function runAgoLabel(iso: string): string {
  const when = agoLabel(iso);
  return when === t("lib.task_card.ago.just_now") || !when
    ? when
    : t("lib.test_cases.labels.ago", { when });
}

export function parseTaskHash(hash: string): { taskId: string; caseKey: string | null } | null {
  if (!hash.startsWith("#task-")) return null;
  const rest = hash.slice("#task-".length);
  const slash = rest.indexOf("/");
  const taskId = slash < 0 ? rest : rest.slice(0, slash);
  if (!taskId) return null;
  const key = slash < 0 ? "" : rest.slice(slash + 1);
  return { taskId, caseKey: key ? safeDecode(key) : null };
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}
