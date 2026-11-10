import { locale, t } from "../i18n";
import {
  niceMax,
  type ChartLegendItem,
  type ChartSeries as UiChartSeries,
  type TimeChartOpts,
} from "./ui/chart";
import { sandboxOwnerName } from "./sandboxes";
import type { TableColumn } from "./ui/table";

export interface Memory {
  total: number;
  used: number;
  available: number;
  cached: number;
  buffers: number;
  swap_total: number;
  swap_used: number;
  percent: number;
}

export interface NetInterface {
  name: string;
  rx_bps: number;
  tx_bps: number;
  rx_bytes: number;
  tx_bytes: number;
}

export interface Disk {
  path: string;
  roles: string[];
  size: number;
  used: number;
  free: number;
  percent: number;
  inodes_total: number;
  inodes_used: number;
  inodes_percent: number;
}

export interface Host {
  hostname: string;
  os: string;
  kernel: string;
  arch: string;
  uptime_seconds: number;
  cpu_model: string;
  cores: number;
  threads: number;
  cpu_percent: number;
  load: number[] | null;
  memory: Memory;
  net: { rx_bps: number; tx_bps: number; interfaces: NetInterface[] | null };
  disks: Disk[] | null;
  in_container: boolean;
  proc_available: boolean;
  net_in_container?: boolean;
}

export interface DockerInfo {
  server_version: string;
  operating_system: string;
  kernel_version: string;
  ncpu: number;
  mem_total: number;
  storage_driver: string;
  root_dir: string;
  compose_project: string;
  containers: number;
  running: number;
  paused: number;
  stopped: number;
  disk_usage_at?: string;
  images: number;
  images_size: number;
  volumes_size: number;
  build_cache_size: number;
  volumes: Array<{ name: string; size: number }> | null;
}

export interface NodeCondition {
  type: string;
  status: string;
  reason: string;
  message: string;
}

export interface KubeNode {
  name: string;
  kubelet_version: string;
  os_image: string;
  kernel: string;
  container_runtime: string;
  cpu_capacity_milli: number;
  cpu_allocatable_milli: number;
  mem_capacity: number;
  mem_allocatable: number;
  ephemeral_capacity: number;
  pods_capacity: number;
  cpu_usage_milli: number;
  mem_usage: number;
  cpu_requests_milli: number;
  mem_requests: number;
  conditions: NodeCondition[] | null;
}

export interface Quota {
  namespace: string;
  name: string;
  workspace_id: string;
  workspace_name: string;
  hard: Record<string, string>;
  used: Record<string, string>;
}

export interface AgentImageBuild {
  base: string;
  image?: string;
  state: "pending" | "building" | "ready" | "failed" | "skipped";
  job?: string;
  message?: string;
  since: string;
}

export interface KubeInfo {
  runtime_class: string;
  control_plane_namespace: string;
  metrics_available: boolean;
  agent_images: AgentImageBuild[] | null;
  nodes: KubeNode[] | null;
  quotas: Quota[] | null;
  pvcs: Array<{
    namespace: string;
    name: string;
    phase: string;
    capacity: number;
    storage_class: string;
    workspace_id: string;
  }> | null;
  pods: {
    total: number;
    running: number;
    pending: number;
    succeeded: number;
    failed: number;
    unknown: number;
  };
}

export type Kind =
  | "daemon"
  | "postgres"
  | "web"
  | "agent"
  | "sandbox"
  | "chat"
  | "mcp"
  | "browser"
  | "platform"
  | "system"
  | "other_install"
  | "other";

export interface KindUsage {
  kind: Kind;
  count: number;
  running: number;
  cpu_cores: number;
  mem_bytes: number;
}

export interface Gate {
  cap: number;
  inflight: number;
  waiting: number;
}

export interface Limit {
  key: string;
  cap: number;
  in_use: number;
}

export interface WorkspaceLoad {
  id: string;
  name: string;
  plan: string;
  concurrency_limit: number;
  running_containers: number;
  running_agents: number;
  cpu_cores: number;
  mem_bytes: number;
  runs_today: number;
  tokens_today: number;
  cost_today: number;
  storage_bytes: number;
}

export interface Postgres {
  database_size: number;
  connections: number;
  max_connections: number;
  version: string;
  largest_tables: Array<{ name: string; bytes: number; rows: number }> | null;
}

export interface Snapshot {
  collected_at: string;
  interval_seconds: number;
  host: Host;
  runtime: { mode: "docker" | "kubernetes" | "none"; docker?: DockerInfo; kube?: KubeInfo };
  kinds: KindUsage[] | null;
  octopus: {
    active_runs: Array<{
      workspace_id: string;
      workspace_name: string;
      agent: string;
      stage: string;
      count: number;
    }> | null;
    active_runs_total: number;
    queue: { agent: Gate; mention: Gate; room?: number };
    limits: Limit[] | null;
    counts: { workspaces: number; users: number; tasks: number; projects: number };
    postgres?: Postgres;
    workspaces: WorkspaceLoad[] | null;
  };
  storage: {
    data_dir: string;
    computed_at?: string;
    total: number;
    dirs: Array<{ name: string; bytes: number }> | null;
    partial: boolean;
  };
  problems: Array<{ source: string; message: string }> | null;
  totals: { total: number; running: number };
}

export interface Container {
  id: string;
  name: string;
  kind: Kind;
  state: string;
  status: string;
  image: string;
  workspace_id: string;
  workspace_name: string;
  task_id: string;
  task_title?: string;
  project_name?: string;
  agent: string;
  stage: string;
  service: string;
  started_at?: string;
  orphan?: boolean;
  cpu_cores: number;
  has_usage: boolean;
  mem_bytes: number;
  mem_limit: number;
  net_rx: number;
  net_tx: number;
  blk_read: number;
  blk_write: number;
  pids: number;
  namespace?: string;
  node?: string;
  runtime_class?: string;
  cpu_request_milli?: number;
  cpu_limit_milli?: number;
  mem_request?: number;
  restarts?: number;
}

export interface Point {
  t: string;
  cpu: number;
  mem: number;
  disk: number;
  rx: number;
  tx: number;
  running: number;
  runs: number;
}

export interface History {
  range: string;
  interval_seconds: number;
  points: Point[] | null;
}

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  let i = 0;
  let v = n;
  while (v >= 1024 && i < BYTE_UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = i === 0 ? 0 : v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return `${v.toLocaleString(locale, { maximumFractionDigits: digits })} ${BYTE_UNITS[i]}`;
}

export function fmtRate(bps: number): string {
  return `${fmtBytes(bps)}/s`;
}

export function fmtPercent(p: number): string {
  if (!Number.isFinite(p)) return "—";
  return `${p.toLocaleString(locale, { maximumFractionDigits: p < 10 ? 1 : 0 })}%`;
}

export function fmtCores(c: number): string {
  return c.toLocaleString(locale, { maximumFractionDigits: c < 1 ? 2 : 1 });
}

const QUANTITY_SUFFIX: Record<string, number> = {
  n: 1e-9,
  u: 1e-6,
  m: 1e-3,
  "": 1,
  k: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  P: 1e15,
  E: 1e18,
  Ki: 2 ** 10,
  Mi: 2 ** 20,
  Gi: 2 ** 30,
  Ti: 2 ** 40,
  Pi: 2 ** 50,
  Ei: 2 ** 60,
};

export function parseQuantity(q: string): number | null {
  const m = /^([+-]?(?:\d+\.?\d*|\.\d+))(?:([eE][+-]?\d+)|(Ki|Mi|Gi|Ti|Pi|Ei|[numkMGTPE]))?$/.exec(
    q.trim(),
  );
  if (!m) return null;
  const [, num, exp = "", suffix = ""] = m;
  if (!Object.hasOwn(QUANTITY_SUFFIX, suffix)) return null;
  const n = Number(num + exp);
  return Number.isFinite(n) ? n * QUANTITY_SUFFIX[suffix] : null;
}

export function fmtQuantity(q: string, res: "cpu" | "memory" | "count"): string {
  const n = parseQuantity(q);
  if (n === null) return q;
  if (res === "memory") return n === 0 ? "0" : fmtBytes(n);
  if (res === "cpu") return fmtCores(n);
  return n.toLocaleString(locale);
}

export type Severity = "ok" | "warn" | "danger";
export function severity(p: number): Severity {
  if (p >= 90) return "danger";
  if (p >= 75) return "warn";
  return "ok";
}

export interface Capacity {
  cpus: number;
  mem: number;
}

// Docker's view wins over /proc: on Docker Desktop it is the VM containers share, not the Mac.
export function capacityOf(s: Snapshot): Capacity {
  const nodes = s.runtime.kube?.nodes ?? [];
  if (nodes.length) {
    return {
      cpus: nodes.reduce((a, n) => a + n.cpu_capacity_milli, 0) / 1000,
      mem: nodes.reduce((a, n) => a + n.mem_capacity, 0),
    };
  }
  const d = s.runtime.docker;
  return {
    cpus: firstPositive(d?.ncpu, s.host.threads, 1),
    mem: firstPositive(d?.mem_total, s.host.memory.total, 0),
  };
}

function firstPositive(...ns: Array<number | undefined>): number {
  return ns.find((n): n is number => n !== undefined && n > 0) ?? 0;
}

// Rounds in binary units so the axis max matches fmtBytes (5 MiB, not 5,000,000 B shown as 4.77
// MiB).
export function niceBytes(max: number): number {
  if (!Number.isFinite(max) || max <= 0) return 1024;
  const unit = Math.pow(1024, Math.max(0, Math.floor(Math.log(max) / Math.log(1024))));
  return niceMax(max / unit) * unit;
}

export function share(part: number, whole: number): number {
  if (!whole || whole <= 0) return 0;
  return Math.min(100, Math.max(0, (part / whole) * 100));
}

export type Group =
  | "agents"
  | "sandboxes"
  | "chats"
  | "browsers"
  | "mcp"
  | "platform"
  | "system"
  | "other_install"
  | "other";

export const GROUP_ORDER: Group[] = [
  "agents",
  "sandboxes",
  "chats",
  "browsers",
  "mcp",
  "platform",
  "system",
  "other_install",
  "other",
];

export function groupOf(kind: Kind): Group {
  switch (kind) {
    case "agent":
      return "agents";
    case "sandbox":
      return "sandboxes";
    case "chat":
      return "chats";
    case "browser":
      return "browsers";
    case "mcp":
      return "mcp";
    case "daemon":
    case "postgres":
    case "web":
    case "platform":
      return "platform";
    case "system":
      return "system";
    case "other_install":
      return "other_install";
    case "other":
      return "other";
  }
}

export interface GroupRow {
  group: Group;
  count: number;
  running: number;
  cpuCores: number;
  memBytes: number;
}

export function groupUsage(kinds: KindUsage[]): GroupRow[] {
  const by = new Map<Group, GroupRow>();
  for (const k of kinds) {
    const g = groupOf(k.kind);
    const row = by.get(g) ?? { group: g, count: 0, running: 0, cpuCores: 0, memBytes: 0 };
    row.count += k.count;
    row.running += k.running;
    row.cpuCores += k.cpu_cores;
    row.memBytes += k.mem_bytes;
    by.set(g, row);
  }
  return GROUP_ORDER.flatMap((g) => {
    const row = by.get(g);
    return row ? [row] : [];
  });
}

export function dataDisk(disks: Disk[] | null): Disk | undefined {
  const list = disks ?? [];
  return list.find((d) => d.roles.includes("data")) ?? list[0];
}

export type SortKey = "name" | "kind" | "workspace" | "task" | "cpu" | "mem" | "uptime" | "state";

function startedMs(c: Container): number {
  const v = c.started_at ? Date.parse(c.started_at) : NaN;
  return Number.isFinite(v) ? v : Infinity;
}

function sortValue(c: Container, key: SortKey): string | number {
  switch (key) {
    case "name":
      return c.name.toLowerCase();
    case "kind":
      return `${GROUP_ORDER.indexOf(groupOf(c.kind))}:${c.kind}`;
    case "workspace":
      return (c.workspace_name || c.workspace_id).toLowerCase();
    case "task":
      return `${c.agent} ${c.task_title ?? c.task_id}`.toLowerCase();
    case "cpu":
      return c.has_usage ? c.cpu_cores : -1;
    case "mem":
      return c.has_usage ? c.mem_bytes : -1;
    case "uptime":
      return startedMs(c);
    case "state":
      return c.state.toLowerCase();
  }
}

export function sortContainers(list: Container[], key: SortKey, desc: boolean): Container[] {
  const dir = desc ? -1 : 1;
  return list
    .map((c, i) => ({ c, i }))
    .sort((a, b) => {
      const va = sortValue(a.c, key);
      const vb = sortValue(b.c, key);
      if (va < vb) return -dir;
      if (va > vb) return dir;
      return a.i - b.i;
    })
    .map((x) => x.c);
}

export type ChartSeries = UiChartSeries<Point>;
export type ChartOpts = TimeChartOpts;
export {
  bindCrosshair,
  chartWindow,
  nearestIndex,
  timeChart,
  timeTicks,
  type ChartModel,
} from "./ui/chart";

export function seriesLegend(series: ChartSeries[]): ChartLegendItem[] {
  return series.map((s) => ({ label: s.label, color: s.color }));
}

export function chartSummary(
  title: string,
  points: Point[],
  series: ChartSeries[],
  fmt: (v: number) => string,
): string {
  const last = points.at(-1);
  if (!last) return t("lib.infra.chart.no_samples", { title });
  return t("lib.infra.chart.summary", {
    title,
    values: series.map((s) => `${s.label} ${fmt(s.value(last))}`).join(", "),
  });
}

export interface CapacitySetting {
  key: string;
  min: number;
  max?: number;
  def: number;
  zeroUnlimited?: boolean;
}

// Bounds and defaults mirror internal/api's validation; change together.
export const CAPACITY_SETTINGS: CapacitySetting[] = [
  { key: "agent.max_containers", min: 0, def: 0, zeroUnlimited: true },
  { key: "mention.max_containers", min: 0, def: 3, zeroUnlimited: true },
  { key: "sandbox.max", min: 1, def: 3 },
  { key: "chat.max_containers", min: 1, def: 5 },
  { key: "agent.max_queue_minutes", min: 1, max: 720, def: 120 },
];

export type CapacityProblem = "required" | "integer" | "range";

export function checkCapacity(
  s: CapacitySetting,
  raw: string,
): { ok: true; value: number } | { ok: false; problem: CapacityProblem } {
  const v = raw.trim();
  if (v === "") return { ok: false, problem: "required" };
  const n = Number(v);
  if (!Number.isInteger(n)) return { ok: false, problem: "integer" };
  if (n < s.min || (s.max !== undefined && n > s.max)) return { ok: false, problem: "range" };
  return { ok: true, value: n };
}

export function storedCapacity(s: CapacitySetting, all: Record<string, unknown>): number {
  const v = all[s.key];
  return typeof v === "number" && Number.isFinite(v) ? v : s.def;
}

// me is null until /auth/me answers, "" with no session (no-login install or API key): the daemon
// then picks the workspace and enforces access.
export function reachableFrom(rowWorkspace: string | undefined, me: string | null): boolean {
  if (me === null) return false;
  if (me === "") return true;
  return !!rowWorkspace && rowWorkspace === me;
}

// Prewarm and Evidence sandboxes label their owner, not a task: "prewarm:p1", or "prewarm-p1" on
// Kubernetes, whose labels cannot hold ':'.
export function labelIsTask(taskID: string): boolean {
  return !/^(prewarm|evidence)[:-]/.test(taskID);
}

export function stopToasts(c: Pick<Container, "name" | "orphan">): {
  pending: string;
  done: string;
  failed: (error: string) => string;
} {
  const name = c.name;
  if (c.orphan)
    return {
      pending: t("infra.containers.removing_orphan", { name }),
      done: t("infra.containers.removed_orphan", { name }),
      failed: (error) => t("infra.containers.remove_orphan_failed", { error }),
    };
  return {
    pending: t("infra.containers.stopping", { name }),
    done: t("infra.containers.stopped", { name }),
    failed: (error) => t("infra.containers.stop_failed", { error }),
  };
}

export function containerOwnerName(taskID: string, title = "", project = ""): string {
  const owner = labelIsTask(taskID) ? taskID : taskID.replace(/^(prewarm|evidence)-/, "$1:");
  return sandboxOwnerName(owner, title, project);
}

// Must match stage.FallbackWorkspaceID.
const FALLBACK_WORKSPACE = "default";

export function workspaceLabel(name: string | undefined, id: string | undefined): string {
  if (name) return name;
  if (!id) return "—";
  if (id === FALLBACK_WORKSPACE) return t("infra.workspace_fallback");
  return id;
}

export function stageLabelOf(id: string, labels: ReadonlyMap<string, string>): string {
  if (id === "mention") return t("infra.stage_mention");
  return labels.get(id) ?? id;
}

export function stateLabel(state: string): string {
  switch (state.toLowerCase()) {
    case "running":
      return t("infra.states.running");
    case "created":
      return t("infra.states.created");
    case "restarting":
      return t("infra.states.restarting");
    case "paused":
      return t("infra.states.paused");
    case "removing":
      return t("infra.states.removing");
    case "exited":
      return t("infra.states.exited");
    case "dead":
      return t("infra.states.dead");
    case "pending":
      return t("infra.states.pending");
    case "succeeded":
      return t("infra.states.succeeded");
    case "failed":
      return t("infra.states.failed");
    case "unknown":
      return t("infra.states.unknown");
    default:
      return state;
  }
}

export function groupColumns(): TableColumn[] {
  return [
    { label: t("infra.usage.columns.what") },
    { label: t("infra.usage.columns.running"), num: true, width: "shrink" },
    { label: t("infra.usage.columns.cpu"), num: true, width: "shrink" },
    { label: t("infra.usage.columns.memory"), num: true, width: "shrink" },
  ];
}

export function containerColumns(): TableColumn[] {
  const col = (label: string, key: SortKey, num = false, shrink = num): TableColumn => ({
    label,
    key,
    sort: "none",
    num,
    width: shrink ? "shrink" : undefined,
  });
  return [
    col(t("infra.containers.columns.name"), "name"),
    col(t("infra.containers.columns.kind"), "kind", false, true),
    col(t("infra.containers.columns.workspace"), "workspace"),
    col(t("infra.containers.columns.task"), "task"),
    col(t("infra.containers.columns.cpu"), "cpu", true),
    col(t("infra.containers.columns.memory"), "mem", true),
    col(t("infra.containers.columns.age"), "uptime", true),
    col(t("infra.containers.columns.state"), "state", false, true),
    { label: t("infra.containers.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}

export function workspaceColumns(): TableColumn[] {
  const num = (label: string): TableColumn => ({ label, num: true, width: "shrink" });
  return [
    { label: t("infra.workspaces.columns.workspace") },
    { label: t("infra.workspaces.columns.plan") },
    num(t("infra.workspaces.columns.containers")),
    num(t("infra.workspaces.columns.agents")),
    num(t("infra.workspaces.columns.cpu")),
    num(t("infra.workspaces.columns.memory")),
    num(t("infra.workspaces.columns.runs_today")),
    num(t("infra.workspaces.columns.storage")),
    num(t("infra.workspaces.columns.tokens_today")),
    num(t("infra.workspaces.columns.cost_today")),
  ];
}

export function quotaColumns(): TableColumn[] {
  return [
    { label: t("infra.quotas.columns.workspace") },
    { label: t("infra.quotas.columns.namespace"), mono: true },
    { label: t("infra.quotas.columns.cpu"), num: true },
    { label: t("infra.quotas.columns.memory"), num: true },
    { label: t("infra.quotas.columns.pods"), num: true, width: "shrink" },
  ];
}
