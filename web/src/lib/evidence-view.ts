import { t } from "../i18n";
import { agentLink } from "./agent-link";
import { runAgoLabel, type TestCasePriority } from "./test-cases";
import { formatSeconds } from "./time";

export type CaseStatus =
  | "draft"
  | "ready"
  | "queued"
  | "running"
  | "pass"
  | "fail"
  | "blocked"
  | "error"
  | "canceled";
export type RunStatus = "queued" | "running" | "pass" | "fail" | "blocked" | "error" | "canceled";
export type BatchStatus = "queued" | "running" | "done" | "canceled" | "error";
export type Trigger = "manual" | "schedule" | "deploy" | "api" | "recording";
export type EvPriority = "low" | "medium" | "high" | "critical";

export interface LatestResult {
  run_id: string;
  status: RunStatus;
  finished_at: string | null;
  poster_id: string;
}

export interface EvCase {
  id: string;
  key: string;
  title: string;
  description: string;
  steps: string;
  expected: string;
  priority: EvPriority;
  tags?: string[] | null;
  project_id: string;
  status: CaseStatus;
  has_take?: boolean;
  take_source: "agent" | "task" | "upload" | "";
  take_updated_at: string | null;
  source_task_id?: string;
  source_case_key?: string;
  last_run_at: string | null;
  created_by?: string;
  updated_by?: string;
  created_at: string;
  updated_at: string;
  latest?: Record<string, LatestResult>;
}

export interface EvCheck {
  label: string;
  expected: string;
  observed: string;
  pass: boolean;
  measured: boolean;
}

export interface EvFailure {
  step: number | string;
  kind: string;
  label: string;
  error: string;
}

export interface EvArtifact {
  id: string;
  kind: "video" | "take" | "chapters" | "shot" | "poster";
  filename: string;
  mime_type: string;
  size: number;
}

export interface EvRun {
  id: string;
  batch_id: string;
  case_id: string;
  environment_id: string;
  position?: number;
  status: RunStatus;
  checks?: EvCheck[];
  failures?: EvFailure[];
  error?: string;
  error_cause?: string;
  duration_ms?: number;
  report_md?: string;
  video_id?: string;
  take_id?: string;
  chapters_id?: string;
  poster_id?: string;
  shot_ids?: string[];
  artifacts?: EvArtifact[];
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  case_key?: string;
  case_title?: string;
  trigger?: Trigger;
  trigger_ref?: string;
}

export interface EvCaseDetail extends EvCase {
  take?: unknown;
  runs?: EvRun[];
}

export interface CasesResponse {
  cases: EvCase[];
  counts?: Partial<Record<LibraryStatus, number>>;
  tags?: string[];
}

export interface BatchCounts {
  total: number;
  passed: number;
  failed: number;
  blocked: number;
  errored: number;
  canceled?: number;
}

export interface EvBatch {
  id: string;
  environment_id: string;
  trigger: Trigger;
  trigger_ref: string;
  status: BatchStatus;
  counts?: Partial<BatchCounts>;
  total?: number;
  passed?: number;
  failed?: number;
  blocked?: number;
  errored?: number;
  canceled?: number;
  error?: string;
  error_cause?: string;
  created_by?: string;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
  base_url?: string;
  source_branch?: string;
  source_commit?: string;
  sandbox_status?: SandboxStatus;
  sandbox_phase?: SandboxPhase;
  sandbox_error?: string;
  sandbox_reason?: string;
  sandbox_log?: string;
  runs?: EvRun[];
}

export type SandboxStatus = "" | "pending" | "starting" | "ready" | "failed" | "released";
export type SandboxPhase = "" | "room" | "source" | "tools" | "setup" | "start";

export type EnvKind = "project" | "standalone" | "sandbox";
export type EnvAction = "run" | "record" | "edit" | "secrets" | "delete" | "link";
export type WorkspaceAction =
  | "create_case"
  | "run"
  | "create_environment"
  | "create_production_environment";

export type Reason =
  | "viewer"
  | "production"
  | "admin_only"
  | "delete_on_project"
  | "sandbox_fixed"
  | "sandbox_no_record"
  | "no_url"
  | "no_init_script"
  | "no_preview_port"
  | "sandbox_unavailable"
  | "repo_not_github";

export interface Permission {
  allowed: boolean;
  reason?: string;
}

export interface DeployInfo {
  available: boolean;
  reason?: string;
  repo: string;
  environment: string;
}

export interface SandboxInfo {
  environment_id: string;
  available: boolean;
  reason?: string;
  port: number;
  default_branch: string;
  init_script_url: string;
}

export interface EvEnvironment {
  id: string;
  kind?: EnvKind;
  project_id?: string;
  project_name?: string;
  project_environment_id?: string;
  name: string;
  display_name?: string;
  base_url: string;
  is_production?: boolean;
  allowed_hosts?: string[] | null;
  secret_names?: string[] | null;
  schedule: string;
  schedule_tz: string;
  on_deploy?: boolean;
  schedule_project_id?: string;
  schedule_tag?: string;
  last_scheduled_at?: string | null;
  deploy?: DeployInfo | null;
  sandbox?: SandboxInfo | null;
  last_batch?: EvBatch | null;
  can?: Partial<Record<EnvAction, Permission>>;
  created_at?: string;
  updated_at?: string;
}

export interface EvContextProject {
  id: string;
  name: string;
  default_branch: string;
  cases: number;
  environments: number;
  sandbox: SandboxInfo | null;
}

export interface EvContext {
  role: string;
  can: Partial<Record<WorkspaceAction, Permission>>;
  projects: EvContextProject[];
  unassigned_cases: number;
  standalone_environments: number;
  show_project_picker?: boolean;
}

export function listOf<T>(r: unknown, key: string): T[] {
  if (Array.isArray(r)) return r as T[];
  if (r && typeof r === "object") {
    const v = (r as Record<string, unknown>)[key];
    if (Array.isArray(v)) return v as T[];
  }
  return [];
}

export function unwrap<T>(r: unknown, key: string): T {
  if (r && typeof r === "object") {
    const o = r as Record<string, unknown>;
    const inner = o[key];
    if (inner && typeof inner === "object" && !Array.isArray(inner)) {
      const rest = Object.fromEntries(Object.entries(o).filter(([k]) => k !== key));
      return { ...rest, ...inner } as T;
    }
  }
  return r as T;
}

export const NOT_RUN = "not_run";

export const CASE_STATUS_ORDER: CaseStatus[] = [
  "fail",
  "blocked",
  "error",
  "pass",
  "running",
  "queued",
  "ready",
  "draft",
  "canceled",
];

const STATUS_KEYS = {
  draft: "evidence.status.draft",
  ready: "evidence.status.ready",
  queued: "evidence.status.queued",
  running: "evidence.status.running",
  pass: "evidence.status.pass",
  fail: "evidence.status.fail",
  blocked: "evidence.status.blocked",
  error: "evidence.status.error",
  canceled: "evidence.status.canceled",
} as const satisfies Record<CaseStatus, string>;

const STATUS_HINT_KEYS = {
  draft: "evidence.status_hint.draft",
  ready: "evidence.status_hint.ready",
  queued: "evidence.status_hint.queued",
  running: "evidence.status_hint.running",
  pass: "evidence.status_hint.pass",
  fail: "evidence.status_hint.fail",
  blocked: "evidence.status_hint.blocked",
  error: "evidence.status_hint.error",
  canceled: "evidence.status_hint.canceled",
} as const satisfies Record<CaseStatus, string>;

const isCaseStatus = (s: string): s is CaseStatus => (CASE_STATUS_ORDER as string[]).includes(s);

export function statusLabel(s: string): string {
  if (s === NOT_RUN) return t("evidence.cases.not_run_here");
  return isCaseStatus(s) ? t(STATUS_KEYS[s]) : s;
}

export function statusHint(s: string): string {
  return isCaseStatus(s) ? t(STATUS_HINT_KEYS[s]) : "";
}

export function statusTone(s: string): "ok" | "bad" | "err" | "warn" | "live" | "idle" | "none" {
  switch (s) {
    case "pass":
      return "ok";
    case "fail":
      return "bad";
    case "error":
      return "err";
    case "blocked":
      return "warn";
    case "queued":
    case "running":
      return "live";
    case "ready":
      return "idle";
    default:
      return "none";
  }
}

export const isActiveRun = (s: string): boolean => s === "queued" || s === "running";
export const isActiveBatch = (b: Pick<EvBatch, "status">): boolean =>
  b.status === "queued" || b.status === "running";

const LIBRARY_ORDER: LibraryStatus[] = [...CASE_STATUS_ORDER, NOT_RUN];

export function visibleStatuses(
  counts: Partial<Record<LibraryStatus, number>>,
  picked: LibraryStatus | "",
): LibraryStatus[] {
  return LIBRARY_ORDER.filter((s) => (counts[s] ?? 0) > 0 || s === picked);
}

export function totalOf(counts: Partial<Record<LibraryStatus, number>>): number {
  return LIBRARY_ORDER.reduce((n, s) => n + (counts[s] ?? 0), 0);
}

export type EvidenceTab = "cases" | "runs" | "environments" | "tasks";
export const TABS: EvidenceTab[] = ["cases", "runs", "environments", "tasks"];

export function tabFromQuery(search: string): EvidenceTab {
  const v = new URLSearchParams(search).get("tab") ?? "";
  return (TABS as string[]).includes(v) ? (v as EvidenceTab) : "cases";
}

export function pageQuery(tab: EvidenceTab, filterQuery: string): string {
  const p = new URLSearchParams(filterQuery);
  p.delete("tab");
  const rest = p.toString();
  const head = tab === "cases" ? "" : `tab=${tab}`;
  const q = [head, rest].filter(Boolean).join("&");
  return q ? `?${q}` : "";
}

export type LibraryStatus = CaseStatus | typeof NOT_RUN;

export interface CaseFilters {
  status: LibraryStatus | "";
  projectId: string;
  includeUnassigned?: boolean;
  tags: string[];
  priority: TestCasePriority | "";
  envId: string;
  q: string;
}

export const EMPTY_CASE_FILTERS: CaseFilters = {
  status: "",
  projectId: "",
  tags: [],
  priority: "",
  envId: "",
  q: "",
};

const PRIORITIES: TestCasePriority[] = ["critical", "high", "medium", "low"];
const isPriority = (s: string): s is TestCasePriority => (PRIORITIES as string[]).includes(s);

export function caseFiltersFromQuery(search: string): CaseFilters {
  const p = new URLSearchParams(search);
  const status = p.get("status") ?? "";
  const projectId = p.get("project_id") ?? "";
  const envId = p.get("environment_id") ?? "";
  const priority = p.get("priority") ?? "";
  return {
    status: isCaseStatus(status) || (envId && status === NOT_RUN) ? status : "",
    projectId,
    ...(projectId && p.get("include_unassigned") === "1" ? { includeUnassigned: true } : {}),
    tags: parseTags(p.get("tag") ?? ""),
    priority: isPriority(priority) ? priority : "",
    envId,
    q: p.get("q") ?? "",
  };
}

export function caseFiltersToQuery(f: CaseFilters): string {
  const p = new URLSearchParams();
  if (f.status) p.set("status", f.status);
  if (f.projectId) p.set("project_id", f.projectId);
  if (f.projectId && f.includeUnassigned) p.set("include_unassigned", "1");
  if (f.tags.length) p.set("tag", f.tags.join(","));
  if (f.priority) p.set("priority", f.priority);
  if (f.envId) p.set("environment_id", f.envId);
  const q = f.q.trim();
  if (q) p.set("q", q);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function casesURL(f: CaseFilters): string {
  return `/evidence/cases${caseFiltersToQuery(f)}`;
}

export const hasCaseFilters = (f: CaseFilters): boolean => caseFiltersToQuery(f) !== "";

export const hasOwnCaseFilters = (f: CaseFilters, pickerOn: boolean): boolean =>
  hasCaseFilters(pickerOn ? { ...f, projectId: "", includeUnassigned: false } : f);

export function runsEnvFromQuery(search: string): string {
  return new URLSearchParams(search).get("environment_id") ?? "";
}

export function batchesURL(envId: string, limit = 30): string {
  const p = new URLSearchParams();
  if (envId) p.set("environment_id", envId);
  p.set("limit", String(limit));
  return `/evidence/batches?${p.toString()}`;
}

export function caseHash(key: string, runId = ""): string {
  return `#case-${encodeURIComponent(key)}${runId ? `/${encodeURIComponent(runId)}` : ""}`;
}

export function parseCaseHash(hash: string): { key: string; runId: string } | null {
  if (!hash.startsWith("#case-")) return null;
  const rest = hash.slice("#case-".length);
  const slash = rest.indexOf("/");
  const key = safeDecode(slash < 0 ? rest : rest.slice(0, slash));
  if (!key) return null;
  return { key, runId: slash < 0 ? "" : safeDecode(rest.slice(slash + 1)) };
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function whenLabel(iso: string | null | undefined): string {
  return iso ? runAgoLabel(iso) : "";
}

export function durationLabel(ms: number | null | undefined): string {
  if (!ms || ms < 0) return "";
  return formatSeconds(Math.max(1, Math.round(ms / 1000)));
}

export function batchDuration(b: EvBatch, now = Date.now()): string {
  if (!b.started_at) return "";
  const start = new Date(b.started_at).getTime();
  const end = b.finished_at ? new Date(b.finished_at).getTime() : now;
  if (!isFinite(start) || !isFinite(end)) return "";
  return durationLabel(end - start);
}

export function batchCounts(b: EvBatch): BatchCounts {
  const c = b.counts ?? {};
  const n = (k: keyof BatchCounts) => Number(c[k] ?? b[k] ?? 0) || 0;
  return {
    total: n("total"),
    passed: n("passed"),
    failed: n("failed"),
    blocked: n("blocked"),
    errored: n("errored"),
    canceled: n("canceled"),
  };
}

export function pendingCount(c: BatchCounts): number {
  return Math.max(0, c.total - c.passed - c.failed - c.blocked - c.errored - (c.canceled ?? 0));
}

export function triggerLabel(trigger: string, ref: string): string {
  switch (trigger) {
    case "manual":
      return ref ? t("evidence.trigger.manual_by", { who: ref }) : t("evidence.trigger.manual");
    case "schedule":
      return t("evidence.trigger.schedule");
    case "deploy":
      return ref ? t("evidence.trigger.deploy_ref", { ref }) : t("evidence.trigger.deploy");
    case "api":
      return t("evidence.trigger.api");
    case "recording":
      return ref
        ? t("evidence.trigger.recording_by", { who: ref })
        : t("evidence.trigger.recording");
    default:
      return trigger;
  }
}

export function lastBatchByEnv(batches: EvBatch[]): Map<string, EvBatch> {
  const out = new Map<string, EvBatch>();
  for (const b of batches) if (!out.has(b.environment_id)) out.set(b.environment_id, b);
  return out;
}

export function batchesSignature(batches: EvBatch[]): string {
  return batches
    .map((b) => {
      const c = batchCounts(b);
      return `${b.id}:${b.status}:${c.passed}:${c.failed}:${c.blocked}:${c.errored}:${c.canceled ?? 0}`;
    })
    .join("|");
}

export function latestPoster(c: Pick<EvCase, "latest">): string {
  let best = "";
  let bestAt = -Infinity;
  for (const r of Object.values(c.latest ?? {})) {
    if (!r.poster_id) continue;
    const at = r.finished_at ? new Date(r.finished_at).getTime() : 0;
    if (at > bestAt) {
      best = r.poster_id;
      bestAt = at;
    }
  }
  return best;
}

export const isRecorded = (c: Pick<EvCase, "status" | "has_take">): boolean =>
  c.has_take ?? c.status !== "draft";

export function tagsOf(cases: Array<Pick<EvCase, "tags">>): string[] {
  const out = new Set<string>();
  for (const c of cases) for (const tag of c.tags ?? []) if (tag) out.add(tag);
  return [...out].sort((a, b) => a.localeCompare(b));
}

export function parseTags(s: string): string[] {
  const out: string[] = [];
  for (const raw of s.split(",")) {
    const tag = raw.trim().replace(/^#/, "");
    if (tag && !out.includes(tag)) out.push(tag);
  }
  return out;
}

export function pickRun(runs: EvRun[], wanted: string, envId = ""): EvRun | null {
  const pool = envId ? runs.filter((r) => r.environment_id === envId) : runs;
  if (wanted) {
    const hit = runs.find((r) => r.id === wanted);
    if (hit) return hit;
  }
  return pool.find((r) => !isActiveRun(r.status)) ?? pool.at(0) ?? null;
}

export function checksSummary(checks: EvCheck[]): { passed: number; total: number } {
  return { passed: checks.filter((c) => c.pass).length, total: checks.length };
}

export function libraryIndex(cases: EvCase[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const c of cases)
    if (c.source_task_id && c.source_case_key)
      out.set(`${c.source_task_id}/${c.source_case_key}`, c.key);
  return out;
}

export type SchedulePreset = "off" | "hourly" | "daily" | "weekdays" | "custom";

export interface ScheduleForm {
  preset: SchedulePreset;
  time: string;
  cron: string;
}

const pad2 = (n: number) => String(n).padStart(2, "0");
const intIn = (s: string, max: number): number | null =>
  /^\d{1,2}$/.test(s) && Number(s) <= max ? Number(s) : null;

export function scheduleFromCron(cron: string): ScheduleForm {
  const c = cron.trim().replace(/\s+/g, " ");
  if (!c) return { preset: "off", time: "09:00", cron: "" };
  const f = c.split(" ");
  if (f.length === 5) {
    const [min, hour, dom, mon, dow] = f;
    const m = intIn(min, 59);
    const h = intIn(hour, 23);
    if (m !== null && hour === "*" && dom === "*" && mon === "*" && dow === "*")
      return { preset: "hourly", time: `00:${pad2(m)}`, cron: c };
    if (m !== null && h !== null && dom === "*" && mon === "*") {
      const time = `${pad2(h)}:${pad2(m)}`;
      if (dow === "*") return { preset: "daily", time, cron: c };
      if (dow === "1-5" || dow === "MON-FRI") return { preset: "weekdays", time, cron: c };
    }
  }
  return { preset: "custom", time: "09:00", cron: c };
}

export function parseTime(s: string): [number, number] | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  return h <= 23 && min <= 59 ? [h, min] : null;
}

// cronFromSchedule() falls back to 09:00 on a bad time; check this first so that fallback is never
// saved.
export function scheduleTimeOK(f: ScheduleForm): boolean {
  return (f.preset !== "daily" && f.preset !== "weekdays") || parseTime(f.time) !== null;
}

export function cronFromSchedule(f: ScheduleForm): string {
  const [h, m] = parseTime(f.time) ?? [9, 0];
  switch (f.preset) {
    case "off":
      return "";
    case "hourly":
      return `${m} * * * *`;
    case "daily":
      return `${m} ${h} * * *`;
    case "weekdays":
      return `${m} ${h} * * 1-5`;
    case "custom":
      return f.cron.trim().replace(/\s+/g, " ");
  }
}

export function isValidCron(s: string): boolean {
  const f = s.trim().split(/\s+/);
  return f.length === 5 && f.every((x) => /^[\d*,\-/A-Za-z?]+$/.test(x));
}

export function scheduleLabel(cron: string, tz: string): string {
  const f = scheduleFromCron(cron);
  const zone = tz || "UTC";
  switch (f.preset) {
    case "off":
      return t("evidence.schedule.label_off");
    case "hourly": {
      const min = f.time.slice(3);
      return min === "00"
        ? t("evidence.schedule.label_hourly")
        : t("evidence.schedule.label_hourly_at", { minute: min });
    }
    case "daily":
      return t("evidence.schedule.label_daily", { time: f.time, tz: zone });
    case "weekdays":
      return t("evidence.schedule.label_weekdays", { time: f.time, tz: zone });
    case "custom":
      return t("evidence.schedule.label_custom", { cron: f.cron, tz: zone });
  }
}

export function deployLabel(env: Pick<EvEnvironment, "on_deploy" | "deploy">): string {
  const d = env.deploy;
  if (!env.on_deploy || !d?.available || !d.repo) return "";
  return t("evidence.deploy.label", { repo: d.repo, environment: d.environment });
}

export const envKind = (env: Pick<EvEnvironment, "kind">): EnvKind => env.kind ?? "standalone";

export function envLabel(env: Pick<EvEnvironment, "kind" | "name" | "display_name">): string {
  if (envKind(env) === "sandbox") return t("evidence.sandbox.name");
  const shown = env.display_name?.trim() ?? "";
  return shown === "" ? env.name : shown;
}

export function sandboxBranch(env: Pick<EvEnvironment, "sandbox">): string {
  const branch = env.sandbox?.default_branch ?? "";
  return branch === "" ? "main" : branch;
}

export function envLongLabel(
  env: Pick<EvEnvironment, "kind" | "name" | "display_name" | "project_name">,
): string {
  const label = envLabel(env);
  return env.project_name
    ? t("evidence.envs.label_with_project", { env: label, project: env.project_name })
    : label;
}

// Must match evidence.CaseRunsOn in the daemon.
export function caseRunsOn(
  caseProjectId: string,
  env: Pick<EvEnvironment, "kind" | "project_id">,
): boolean {
  const kind = envKind(env);
  if (kind === "project") return caseProjectId === "" || caseProjectId === env.project_id;
  if (kind === "sandbox") return caseProjectId !== "" && caseProjectId === env.project_id;
  return true;
}

export function envsForProjects(
  envs: EvEnvironment[],
  projectIds: Iterable<string>,
): EvEnvironment[] {
  const ids = [...new Set(projectIds)];
  return envs.filter((env) => ids.some((id) => caseRunsOn(id, env)));
}

export function permission(env: Pick<EvEnvironment, "can">, action: EnvAction): Permission {
  return env.can?.[action] ?? { allowed: true };
}

const REASON_KEYS = {
  viewer: "evidence.can.viewer",
  production: "evidence.can.production",
  admin_only: "evidence.can.admin_only",
  delete_on_project: "evidence.can.delete_on_project",
  sandbox_fixed: "evidence.can.sandbox_fixed",
  sandbox_no_record: "evidence.can.sandbox_no_record",
  no_url: "evidence.can.no_url",
  no_init_script: "evidence.can.no_init_script",
  no_preview_port: "evidence.can.no_preview_port",
  sandbox_unavailable: "evidence.can.sandbox_unavailable",
  repo_not_github: "evidence.can.repo_not_github",
} as const satisfies Record<Reason, string>;

export function reasonLabel(reason: string | undefined): string {
  if (reason && Object.hasOwn(REASON_KEYS, reason)) return t(REASON_KEYS[reason as Reason]);
  return t("evidence.can.unknown");
}

export const needsInitScript = (reason: string | undefined): boolean =>
  reason === "no_init_script" || reason === "no_preview_port";

const SCRIPT_FAILURES = new Set([
  "no_init_script",
  "no_preview_port",
  "params_missing",
  "tools_failed",
  "setup_failed",
  "start_failed",
  "ready_timeout",
]);

export const scriptFailure = (reason: string | undefined): boolean =>
  SCRIPT_FAILURES.has(reason ?? "");

export function sentence(text: string): string {
  const s = text.trim();
  return s ? s.charAt(0).toLocaleUpperCase() + s.slice(1) : "";
}

export function sameName(a: string, b: string): boolean {
  const norm = (s: string) =>
    s
      .trim()
      .toLowerCase()
      .replace(/[\s_-]+/g, "-");
  return norm(a) === norm(b);
}

export interface EnvGroup {
  projectId: string;
  projectName: string;
  envs: EvEnvironment[];
  sandbox: EvEnvironment | null;
}

export function groupEnvs(
  envs: EvEnvironment[],
  projects: Array<{ id: string; name: string }> = [],
): EnvGroup[] {
  const groups = new Map<string, EnvGroup>();
  const group = (id: string, name: string): EnvGroup => {
    let g = groups.get(id);
    if (!g) {
      g = { projectId: id, projectName: name, envs: [], sandbox: null };
      groups.set(id, g);
    }
    if (!g.projectName && name) g.projectName = name;
    return g;
  };
  for (const p of projects) group(p.id, p.name || p.id);
  const rest: EvEnvironment[] = [];
  for (const env of envs) {
    const kind = envKind(env);
    if (kind === "standalone" || !env.project_id) {
      rest.push(env);
      continue;
    }
    const g = group(env.project_id, env.project_name ?? "");
    if (kind === "sandbox") g.sandbox = env;
    else g.envs.push(env);
  }
  const out = [...groups.values()];
  for (const g of out) if (!g.projectName) g.projectName = g.projectId;
  if (rest.length) out.push({ projectId: "", projectName: "", envs: rest, sandbox: null });
  return out;
}

export interface CaseGroup {
  projectId: string;
  projectName: string;
  cases: EvCase[];
}

export function groupCases(cases: EvCase[], projects: Map<string, string>): CaseGroup[] {
  const order = [...projects.keys()];
  const groups = new Map<string, CaseGroup>();
  for (const c of cases) {
    const id = c.project_id;
    let g = groups.get(id);
    if (!g) {
      g = { projectId: id, projectName: id ? (projects.get(id) ?? id) : "", cases: [] };
      groups.set(id, g);
    }
    g.cases.push(c);
  }
  const rank = (id: string): number => {
    if (id === "") return Number.MAX_SAFE_INTEGER;
    const i = order.indexOf(id);
    return i < 0 ? order.length : i;
  };
  return [...groups.values()].sort((a, b) => rank(a.projectId) - rank(b.projectId));
}

export function showProjectPicker(
  ctx: Pick<EvContext, "show_project_picker" | "projects"> | null,
): boolean {
  if (!ctx) return false;
  return ctx.show_project_picker ?? ctx.projects.length > 1;
}

export const PROJECT_NONE = "none";

export function pickProject(
  fromURL: string,
  remembered: string,
  projects: Array<{ id: string }>,
): string {
  const known = (id: string) =>
    id === PROJECT_NONE || (id !== "" && projects.some((p) => p.id === id));
  if (known(fromURL)) return fromURL;
  if (!fromURL && known(remembered)) return remembered;
  return "";
}

export const ENV_SLUG = /^[a-z0-9][a-z0-9._-]{0,39}$/;

export function slugify(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .slice(0, 40)
    .replace(/[-._]+$/, "");
}

export const isSandboxBatch = (b: Pick<EvBatch, "sandbox_status">): boolean =>
  Boolean(b.sandbox_status);

export const isSandboxStarting = (b: Pick<EvBatch, "sandbox_status">): boolean =>
  b.sandbox_status === "pending" || b.sandbox_status === "starting";

export const isSandboxFailed = (b: Pick<EvBatch, "sandbox_status">): boolean =>
  b.sandbox_status === "failed";

export const SANDBOX_STEPS = ["room", "source", "setup", "start"] as const;
export type SandboxStep = (typeof SANDBOX_STEPS)[number];

export function sandboxStep(phase: string | undefined): SandboxStep {
  if (phase === "source" || phase === "start") return phase;
  if (phase === "tools" || phase === "setup") return "setup";
  return "room";
}

const STEP_KEYS = {
  room: "evidence.sandbox.phase_room",
  source: "evidence.sandbox.phase_source",
  setup: "evidence.sandbox.phase_setup",
  start: "evidence.sandbox.phase_start",
} as const satisfies Record<SandboxStep, string>;

export const sandboxStepLabel = (step: SandboxStep): string => t(STEP_KEYS[step]);

export const shortCommit = (sha: string | undefined): string => (sha ?? "").slice(0, 7);

export const isNotRun = (b: Pick<EvBatch, "sandbox_status">, r: Pick<EvRun, "status">): boolean =>
  isSandboxFailed(b) && r.status === "canceled";

export const SECRET_NAME = /^[A-Z][A-Z0-9_]{0,40}$/;

export function normalizeSecretName(s: string): string {
  return s
    .trim()
    .toUpperCase()
    .replace(/[\s-]+/g, "_")
    .replace(/[^A-Z0-9_]/g, "");
}

export function parseHosts(s: string): string[] {
  const out: string[] = [];
  for (const raw of s.split(/[\n,]/)) {
    const host = raw
      .trim()
      .replace(/^[a-z]+:\/\//i, "")
      .replace(/\/.*$/, "");
    if (host && !out.includes(host)) out.push(host);
  }
  return out;
}

export function isHttpURL(s: string): boolean {
  try {
    const u = new URL(s.trim());
    return (u.protocol === "http:" || u.protocol === "https:") && !u.username && !u.password;
  } catch {
    return false;
  }
}

export function ciHookCurl(base: string, envId: string): string {
  const url = `${base.replace(/\/$/, "")}/evidence/environments/${encodeURIComponent(envId)}/run`;
  // i18n-ignore a shell command, not a message
  return `curl -fsS -X POST "${url}" \\\n  -H "X-API-Key: $OCTOPUS_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{}'`;
}

export function timeZones(): string[] {
  try {
    const all = Intl.supportedValuesOf("timeZone");
    if (all.length) return all.includes("UTC") ? all : ["UTC", ...all];
  } catch {
    // an engine without supportedValuesOf
  }
  return ["UTC"];
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export interface Chapter {
  start: number;
  title: string;
}

function vttSeconds(s: string): number {
  const parts = s.trim().split(":").map(Number);
  return parts.reduce((acc, p) => acc * 60 + p, 0);
}

export function parseChapters(vtt: string): Chapter[] {
  const out: Chapter[] = [];
  const blocks = vtt.replace(/\r/g, "").split(/\n\n+/);
  for (const block of blocks) {
    const lines = block.split("\n").filter((l) => l.trim() !== "");
    const at = lines.findIndex((l) => l.includes("-->"));
    if (at < 0) continue;
    const start = vttSeconds(lines[at].split("-->")[0]);
    const title = lines
      .slice(at + 1)
      .join(" ")
      .trim();
    if (title && isFinite(start)) out.push({ start, title });
  }
  return out;
}

export function clockLabel(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = pad2(s % 60);
  return h ? `${h}:${pad2(m)}:${r}` : `${m}:${r}`;
}

export interface RecordingAgent {
  type: string;
  name: string;
  browser: boolean;
  ready: boolean;
  reason?: string;
  // Absent from older daemons; only an explicit false means no prompt.
  prompt_ready?: boolean;
}

export interface RecordingAgents {
  agents: RecordingAgent[];
  auto: string;
}

export type RecordBlock = "" | "no_agent" | "no_credential" | "no_prompt";

export function recordingPick(
  list: RecordingAgents,
  picked: string,
): { agent: RecordingAgent | null; block: RecordBlock } {
  const want = picked || list.auto;
  const agent = list.agents.find((a) => a.type === want) ?? null;
  if (!agent) return { agent: null, block: "no_agent" };
  if (!agent.ready) return { agent, block: "no_credential" };
  return { agent, block: agent.prompt_ready === false ? "no_prompt" : "" };
}

export function recordingAgentLabel(a: RecordingAgent): string {
  const name = a.name || a.type;
  if (!a.ready) return t("evidence.record.agent_no_key", { name });
  if (a.prompt_ready === false) return t("evidence.record.agent_no_prompt", { name });
  return name;
}

// Link by name, not type: /agents/ looks agents up by name and a clone keeps its source's type.
export function recordingPromptHref(a: RecordingAgent): string {
  return agentLink(a.name || a.type, "prompts");
}

const CAUSE_KEYS: Record<string, string> = {
  browser_image: "evidence.cause.browser_image",
  runner_start: "evidence.cause.runner_start",
  runner_exit: "evidence.cause.runner_exit",
  deadline: "evidence.cause.deadline",
  daemon_restarted: "evidence.cause.daemon_restarted",
  runner_lost: "evidence.cause.runner_lost",
};

export function causeLabel(cause: string | undefined): string {
  const key = cause ? CAUSE_KEYS[cause] : undefined;
  return key ? t(key) : "";
}

export function cardEnv(
  c: Pick<EvCase, "project_id">,
  envs: EvEnvironment[],
  filtered = "",
): EvEnvironment | null {
  if (filtered) return envs.find((env) => env.id === filtered) ?? null;
  const runs = envs.filter(
    (env) => envKind(env) !== "sandbox" && !env.is_production && caseRunsOn(c.project_id, env),
  );
  return runs.find((env) => c.project_id && env.project_id === c.project_id) ?? runs.at(0) ?? null;
}

export function cardStatus(
  c: Pick<EvCase, "project_id" | "status" | "has_take" | "latest">,
  envs: EvEnvironment[],
  filtered = "",
): { status: string; env: EvEnvironment | null } {
  if (!isRecorded(c)) return { status: c.status, env: null };
  const env = cardEnv(c, envs, filtered);
  if (!env) return { status: c.status, env: null };
  const latest = c.latest && env.id in c.latest ? c.latest[env.id] : null;
  return { status: latest ? latest.status : NOT_RUN, env };
}

export type CaseSort = "newest" | "priority" | "status";
export const CASE_SORTS: CaseSort[] = ["newest", "priority", "status"];

const PRIORITY_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 };

export function sortCases<C extends Pick<EvCase, "priority" | "status">>(
  cases: C[],
  by: CaseSort,
  statusOf: (c: C) => string = (c) => c.status,
): C[] {
  if (by === "newest") return cases;
  const order: string[] = [...CASE_STATUS_ORDER, NOT_RUN];
  const rank = (c: C): number => {
    if (by === "priority") return PRIORITY_RANK[c.priority] ?? 9;
    const i = order.indexOf(statusOf(c));
    return i < 0 ? order.length : i;
  };
  return cases
    .map((c, i) => ({ c, i, r: rank(c) }))
    .sort((a, b) => a.r - b.r || a.i - b.i)
    .map((x) => x.c);
}

export function scheduleScopeLine(count: number, projectName: string, tag: string): string {
  const vars = { count, project: projectName, tag };
  if (count === 0) {
    if (projectName && tag) return t("evidence.envs.schedule_scope_none.project_tag", vars);
    if (projectName) return t("evidence.envs.schedule_scope_none.project", vars);
    if (tag) return t("evidence.envs.schedule_scope_none.tag", vars);
    return t("evidence.envs.schedule_scope_none.all");
  }
  if (projectName && tag) return t("evidence.envs.schedule_scope.project_tag", vars);
  if (projectName) return t("evidence.envs.schedule_scope.project", vars);
  if (tag) return t("evidence.envs.schedule_scope.tag", vars);
  return t("evidence.envs.schedule_scope.all", vars);
}
