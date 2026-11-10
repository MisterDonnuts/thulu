import { t } from "../i18n";
import { taskAnchor } from "./test-cases";
import type { TableColumn } from "./ui/table";

export type TriggerKind = "schedule" | "event" | "metric" | "manual";

export interface TriggerForm {
  kind: TriggerKind;
  schedule: string;
  tz: string;
  event: string;
  filter: string;
  environment: string;
  metric: string;
  window: string;
  agg: string;
  op: string;
  threshold: string;
  every: string;
}

// Mirrors the daemon's automation.Trigger.
export interface ParsedTrigger {
  schedule?: string;
  tz?: string;
  event?: string;
  filter?: string;
  manual?: boolean;
  metric?: {
    environment?: string;
    metric?: string;
    window?: string;
    agg?: string;
    op?: string;
    threshold?: number;
    every?: string;
  };
}

export function emptyTrigger(kind: TriggerKind = "manual"): TriggerForm {
  return {
    kind,
    schedule: "",
    tz: "",
    event: "",
    filter: "",
    environment: "",
    metric: "",
    window: "",
    agg: "",
    op: "",
    threshold: "",
    every: "",
  };
}

export function readTrigger(p: ParsedTrigger | undefined): TriggerForm {
  const f = emptyTrigger();
  if (!p) return f;
  if (p.schedule) {
    return { ...f, kind: "schedule", schedule: p.schedule, tz: p.tz ?? "" };
  }
  if (p.event) {
    return { ...f, kind: "event", event: p.event, filter: p.filter ?? "" };
  }
  if (p.metric) {
    const m = p.metric;
    return {
      ...f,
      kind: "metric",
      environment: m.environment ?? "",
      metric: m.metric ?? "",
      window: m.window ?? "",
      agg: m.agg ?? "",
      op: m.op ?? "",
      threshold: m.threshold === undefined ? "" : String(m.threshold),
      every: m.every ?? "",
    };
  }
  return f;
}

function scalar(v: string): string {
  if (/^[A-Za-z0-9_][\w./+-]*$/.test(v) && !/^(true|false|null|yes|no|on|off)$/i.test(v)) {
    return v;
  }
  if (/["\\]/.test(v) && !/[\n\r]/.test(v)) return `'${v.replace(/'/g, "''")}'`;
  return JSON.stringify(v);
}

export function renderTrigger(f: TriggerForm): string {
  const lines = ["trigger:"];
  const put = (indent: string, key: string, v: string, raw = false) => {
    const val = v.trim();
    if (val) lines.push(`${indent}${key}: ${raw ? val : scalar(val)}`);
  };
  switch (f.kind) {
    case "schedule":
      lines.push(`  schedule: ${JSON.stringify(f.schedule.trim())}`);
      put("  ", "tz", f.tz);
      break;
    case "event":
      put("  ", "event", f.event);
      put("  ", "filter", f.filter);
      break;
    case "metric":
      lines.push("  metric:");
      put("    ", "environment", f.environment);
      put("    ", "metric", f.metric);
      put("    ", "window", f.window);
      put("    ", "agg", f.agg);
      put("    ", "op", f.op);
      // Unquoted when numeric: a quoted threshold reaches the daemon as a string.
      put("    ", "threshold", f.threshold, /^-?\d+(\.\d+)?$/.test(f.threshold.trim()));
      put("    ", "every", f.every);
      break;
    case "manual":
      // i18n-ignore a YAML line, not a message
      lines.push("  manual: true");
  }
  return lines.join("\n") + "\n";
}

export function triggerRange(spec: string): { start: number; end: number } | null {
  const lines = spec.split("\n");
  let offset = 0;
  let startLine = -1;
  let start = 0;
  const offsets: number[] = [];
  for (const line of lines) {
    offsets.push(offset);
    offset += line.length + 1;
  }
  for (let i = 0; i < lines.length; i++) {
    if (/^trigger:/.test(lines[i])) {
      startLine = i;
      start = offsets[i];
      break;
    }
  }
  if (startLine < 0) return null;
  let endLine = lines.length;
  for (let i = startLine + 1; i < lines.length; i++) {
    if (/^[^\s#]/.test(lines[i])) {
      endLine = i;
      break;
    }
  }
  while (endLine - 1 > startLine && /^(\s*|#.*)$/.test(lines[endLine - 1])) endLine--;
  const end = endLine >= lines.length ? spec.length : offsets[endLine];
  return { start, end };
}

export function withTrigger(spec: string, f: TriggerForm): string {
  const block = renderTrigger(f);
  const r = triggerRange(spec);
  if (r) {
    const tail = spec.slice(r.end);
    return (
      spec.slice(0, r.start) +
      (r.end >= spec.length && !spec.endsWith("\n") ? block.slice(0, -1) : block) +
      tail
    );
  }
  const head = spec.match(/^(?:(?:name|description):.*\n)+/);
  const at = head ? head[0].length : 0;
  return spec.slice(0, at) + block + spec.slice(at);
}

export function triggerBlock(spec: string): string | null {
  const r = triggerRange(spec);
  return r ? spec.slice(r.start, r.end).trimEnd() : null;
}

// Locks when nothing parsed so the form never overwrites YAML it did not read, except the block
// it wrote itself (an Event trigger with no event picked yet does not parse).
export function shouldLockGuide(
  parsedTrigger: boolean,
  spec: string,
  lastWritten: string,
): boolean {
  if (parsedTrigger) return false;
  if (!lastWritten) return true;
  return triggerBlock(spec) !== lastWritten.trimEnd();
}

export function insertAt(
  text: string,
  start: number,
  end: number,
  value: string,
): { text: string; cursor: number } {
  const a = Math.max(0, Math.min(start, text.length));
  const b = Math.max(a, Math.min(end, text.length));
  return { text: text.slice(0, a) + value + text.slice(b), cursor: a + value.length };
}

export function effectiveTz(tz?: string): string {
  const v = tz?.trim();
  if (v) return v;
  return "UTC";
}

export function withTimezone(spec: string, tz: string): string {
  if (!tz || tz === "UTC") return spec;
  return spec.replace(/^(\s+)tz: UTC$/m, `$1tz: ${tz}`);
}

export interface PayloadSource {
  trigger?: ParsedTrigger;
  debounce?: string;
  board?: { id: string; stages: string[] };
}

const PR_URL = "https://github.com/acme/web/pull/42";

// Must match the flattened GitHub payload in internal/api/automation_events.go; every key present,
// empty when absent.
function githubBase(event: string): Record<string, unknown> {
  return {
    event,
    action: "",
    repo: "acme/web",
    sender: "ana",
    sender_is_bot: false,
    pr_url: PR_URL,
    pr_number: 42,
    pr_title: t("lib.automation_editor.sample.pr_title"),
    pr_draft: false,
    branch: "feature/login",
    comment: "",
    comment_url: "",
    comment_by: "ana",
    review_state: "",
    review_body: "",
    issue_number: 0,
    issue_title: "",
    ref: "",
    task_id: "",
    task_title: "",
    task_stage: "",
  };
}

function boardStages(board?: { id: string; stages: string[] }): [string, string] {
  const st = board?.stages.length ? board.stages : ["backlog", "in_progress"];
  return [st[0], st[1] ?? st[0]];
}

function taskBase(board?: { id: string; stages: string[] }): Record<string, unknown> {
  return {
    task_id: "",
    title: t("lib.automation_editor.sample.task_title"),
    stage: boardStages(board)[0],
    board_id: board?.id ?? "",
    status: "idle",
    tags: [],
  };
}

export function samplePayload(src: PayloadSource): Record<string, unknown> | null {
  const tr = src.trigger;
  if (!tr) return null;
  let p: Record<string, unknown> | null = null;
  if (tr.metric) {
    const m = tr.metric;
    p = {
      environment: m.environment ?? "",
      metric: m.metric ?? "",
      value: crossing(m.threshold ?? 0, m.op ?? ">"),
      threshold: m.threshold ?? 0,
      op: m.op ?? ">",
      window: m.window ?? "",
      agg: m.agg ?? "avg",
    };
  } else if (tr.event) {
    p = eventPayload(tr.event, src.board);
  }
  if (p && src.debounce && tr.event) {
    p = { ...p, events: [{ ...p }], event_count: 1 };
  }
  return p;
}

export function syncBurst(p: Record<string, unknown>): Record<string, unknown> {
  const events = p.events;
  if (!Array.isArray(events) || events.length !== 1) return p;
  const event = { ...p };
  delete event.events;
  delete event.event_count;
  return { ...p, events: [event], event_count: 1 };
}

export function parsePayload(raw?: string): Record<string, unknown> | null {
  if (!raw) return null;
  try {
    const v: unknown = JSON.parse(raw);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

export function batchCount(payload: unknown): number {
  if (!payload || typeof payload !== "object") return 0;
  const p = payload as Record<string, unknown>;
  if (typeof p.event_count === "number") return p.event_count;
  return Array.isArray(p.events) ? p.events.length : 0;
}

function crossing(threshold: number, op: string): number {
  const step = Math.max(1, Math.abs(threshold) * 0.5);
  return op.startsWith("<") ? threshold - step : threshold + step;
}

function eventPayload(
  event: string,
  board?: { id: string; stages: string[] },
): Record<string, unknown> {
  const [first, next] = boardStages(board);
  switch (event) {
    case "github.pull_request_review_comment":
      return {
        ...githubBase(event),
        action: "created",
        comment: t("lib.automation_editor.sample.comment"),
        comment_url: `${PR_URL}#discussion_r1`,
      };
    case "github.pull_request_review":
      return {
        ...githubBase(event),
        action: "submitted",
        review_state: "changes_requested",
        review_body: t("lib.automation_editor.sample.review_body"),
      };
    case "github.issue_comment":
      return {
        ...githubBase(event),
        action: "created",
        comment: t("lib.automation_editor.sample.comment"),
        issue_number: 42,
        issue_title: t("lib.automation_editor.sample.pr_title"),
      };
    case "github.pull_request":
      return { ...githubBase(event), action: "opened" };
    case "github.push":
      return {
        ...githubBase(event),
        pr_url: "",
        pr_number: 0,
        pr_title: "",
        ref: "refs/heads/main",
        branch: "",
      };
    case "github.check_suite":
      return { ...githubBase(event), action: "completed" };
    case "task.stage_changed":
      return { ...taskBase(board), stage: next, from: first, to: next };
    case "task.failed":
      return { ...taskBase(board), stage: next, status: "failed" };
    case "task.created":
      return taskBase(board);
    default:
      return { event };
  }
}

export interface RunStepView {
  kind: string;
  status: string;
  output?: string;
  error?: string;
}

export interface StepSummary {
  text: string;
  taskID?: string;
  href?: string;
}

function parseOutput(output?: string): Record<string, unknown> | null {
  if (!output?.startsWith("{")) return null;
  try {
    const v: unknown = JSON.parse(output);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    // Step output is truncated to a fixed length; the daemon writes keys sorted, so the counters
    // precede the bulky `json`/`stdout` and survive the cut.
    const o: Record<string, unknown> = {};
    for (const key of ["count", "exit_code", "status_code"]) {
      const m = output.match(new RegExp(`"${key}":(-?\\d+(?:\\.\\d+)?)`));
      if (m) o[key] = Number(m[1]);
    }
    return Object.keys(o).length ? o : null;
  }
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number => (typeof v === "number" ? v : Number(v) || 0);

export function stepSummary(s: RunStepView): StepSummary | null {
  const sum = summarize(s);
  return sum?.taskID ? { ...sum, href: taskAnchor(sum.taskID, "") } : sum;
}

function summarize(s: RunStepView): StepSummary | null {
  if (s.status !== "ok") return null;
  const o = parseOutput(s.output);
  if (!o) return null;
  const task = str(o.task_id);
  const title = str(o.title) || task;
  switch (s.kind) {
    case "task":
      if (o.created === true) {
        return o.agent
          ? {
              text: t("lib.automation_editor.summary.task_created_agent", {
                title,
                agent: str(o.agent),
              }),
              taskID: task,
            }
          : { text: t("lib.automation_editor.summary.task_created", { title }), taskID: task };
      }
      if (o.commented === true) {
        return { text: t("lib.automation_editor.summary.task_commented", { title }), taskID: task };
      }
      if (o.duplicate === true) {
        return { text: t("lib.automation_editor.summary.task_duplicate", { title }), taskID: task };
      }
      return null;
    case "mention":
      if (o.mentioned === true) {
        return {
          text: t("lib.automation_editor.summary.mentioned", { agent: str(o.agent), title }),
          taskID: task,
        };
      }
      if (o.commented === true) {
        const skipped = str(o.skipped_agent);
        return skipped
          ? {
              text: t("lib.automation_editor.summary.commented_no_mention", {
                title,
                agent: skipped,
              }),
              taskID: task,
            }
          : { text: t("lib.automation_editor.summary.commented", { title }), taskID: task };
      }
      return null;
    case "query": {
      if ("has_data" in o) {
        return o.has_data
          ? { text: t("lib.automation_editor.summary.metric", { value: num(o.value) }) }
          : { text: t("lib.automation_editor.summary.metric_empty") };
      }
      const count = num(o.count);
      return { text: t("lib.automation_editor.summary.tasks_found", { count }) };
    }
    case "script":
      return { text: t("lib.automation_editor.summary.script", { code: num(o.exit_code) }) };
    case "http":
      return { text: t("lib.automation_editor.summary.http", { code: num(o.status_code) }) };
    default:
      return null;
  }
}

export function isJSONOutput(output?: string): boolean {
  return !!output && output.startsWith("{");
}

export function runColumns(): TableColumn[] {
  return [
    { label: t("automations.columns.started"), mono: true, width: "shrink" },
    { label: t("automations.columns.trigger") },
    { label: t("automations.columns.status") },
    { label: t("automations.columns.agent_runs"), num: true, mono: true, width: "shrink" },
    { label: t("automations.columns.duration"), mono: true, width: "shrink" },
    { label: t("automations.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}
