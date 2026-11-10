import { locale, t } from "../i18n";
import { parseMCPEntry } from "./mcp-entry";
import type { TableColumn } from "./ui/table";

// Must match the daemon's defaults in internal/settings.
export const SANDBOX_DEFAULTS = {
  max: 3,
  idleEvictSeconds: 30 * 60,
  acquireBlockMs: 2 * 60_000,
} as const;

const PREWARM_PREFIX = "prewarm:";
const EVIDENCE_PREFIX = "evidence:";

export type SandboxOwner =
  | { kind: "task"; id: string }
  | { kind: "prewarm"; id: string }
  | { kind: "evidence"; id: string };

export function sandboxOwner(ownerID: string): SandboxOwner {
  if (ownerID.startsWith(PREWARM_PREFIX))
    return { kind: "prewarm", id: ownerID.slice(PREWARM_PREFIX.length) };
  if (ownerID.startsWith(EVIDENCE_PREFIX))
    return { kind: "evidence", id: ownerID.slice(EVIDENCE_PREFIX.length) };
  return { kind: "task", id: ownerID };
}

export function sandboxOwnerName(ownerID: string, title = "", project = ""): string {
  const o = sandboxOwner(ownerID);
  if (o.kind === "prewarm") return t("sandboxes.owners.prewarm", { project: project || o.id });
  if (o.kind === "evidence") return t("sandboxes.owners.evidence");
  return title || o.id;
}

export function taskHref(taskID: string): string {
  return `/tasks/#task-${encodeURIComponent(taskID)}`;
}

export function settingNumber(v: unknown, fallback: number): number {
  return typeof v === "number" && Number.isFinite(v) ? v : fallback;
}

export function toUnit(value: number, per: number, lang: string = locale): string {
  return (value / per).toLocaleString(lang, { maximumFractionDigits: 1, useGrouping: false });
}

export type FieldProblem =
  | "required"
  | "number"
  | "integer"
  | "min1"
  | "min0"
  | "whole_min0"
  | "refused";

export interface SandboxForm {
  max: string;
  idleMinutes: string;
  waitSeconds: string;
}

export interface SandboxValues {
  max: number;
  idleEvictSeconds: number;
  acquireBlockMs: number;
}

export type SandboxFormCheck =
  | { ok: true; values: SandboxValues }
  | { ok: false; problems: Partial<Record<keyof SandboxForm, FieldProblem>> };

function parseField(raw: string, min: 0 | 1, integer: boolean): number | FieldProblem {
  const s = raw.trim();
  if (s === "") return "required";
  const n = Number(s.replace(",", "."));
  if (!Number.isFinite(n)) return "number";
  if (integer && !Number.isInteger(n)) return "integer";
  if (n < min) return min === 1 ? "min1" : "min0";
  return n;
}

export function checkSandboxForm(f: SandboxForm): SandboxFormCheck {
  const max = parseField(f.max, 1, true);
  const idle = parseField(f.idleMinutes, 0, false);
  const wait = parseField(f.waitSeconds, 0, true);
  const problems: Partial<Record<keyof SandboxForm, FieldProblem>> = {};
  if (typeof max === "string") problems.max = max;
  if (typeof idle === "string") problems.idleMinutes = idle;
  if (typeof wait === "string") problems.waitSeconds = wait;
  if (typeof max === "string" || typeof idle === "string" || typeof wait === "string")
    return { ok: false, problems };
  return {
    ok: true,
    values: {
      max,
      idleEvictSeconds: Math.round(idle * 60),
      acquireBlockMs: Math.round(wait * 1000),
    },
  };
}

export type PriorityCheck =
  | { ok: true; values: Record<string, number> }
  | { ok: false; problems: Record<string, FieldProblem> };

export function checkPriorities(raw: Record<string, string>): PriorityCheck {
  const values: Record<string, number> = {};
  const problems: Record<string, FieldProblem> = {};
  for (const [stage, text] of Object.entries(raw)) {
    const s = text.trim();
    const n = Number(s.replace(",", "."));
    if (s === "") problems[stage] = "required";
    else if (!Number.isFinite(n)) problems[stage] = "number";
    else if (!Number.isInteger(n)) problems[stage] = "integer";
    else values[stage] = n;
  }
  return Object.keys(problems).length > 0 ? { ok: false, problems } : { ok: true, values };
}

export type SandboxField = keyof SandboxForm | "priorities";

const SETTING_FIELDS: Partial<Record<string, SandboxField>> = {
  "sandbox.max": "max",
  "sandbox.idle_evict_seconds": "idleMinutes",
  "sandbox.acquire_block_ms": "waitSeconds",
  "sandbox.priorities": "priorities",
};

const REFUSAL_PROBLEMS: Partial<Record<string, FieldProblem>> = {
  "api.settings.errors.sandbox_max_positive": "min1",
  "api.settings.errors.non_negative_int": "whole_min0",
  "api.settings.errors.priorities_shape": "integer",
};

export function refusedField(body: unknown): { field: SandboxField; problem: FieldProblem } | null {
  if (!body || typeof body !== "object") return null;
  const { setting, code } = body as { setting?: unknown; code?: unknown };
  const field = typeof setting === "string" ? SETTING_FIELDS[setting] : undefined;
  if (!field) return null;
  const problem = typeof code === "string" ? REFUSAL_PROBLEMS[code] : undefined;
  return { field, problem: problem ?? "refused" };
}

export interface AgentMCPs {
  name: string;
  mcp_by_stage?: Record<string, string[]>;
}

export function anyAgentUsesSandbox(agents: AgentMCPs[]): boolean {
  return agents.some((a) =>
    Object.values(a.mcp_by_stage ?? {}).some((entries) =>
      entries.some((e) => parseMCPEntry(e).server === "thulu-sandbox"),
    ),
  );
}

export interface TaskSandbox {
  task_id: string;
  owner_id: string;
  status: string;
  idle_seconds: number;
  host_ports: Record<string, number>;
  urls?: Record<string, string>;
  publishes_ports: boolean;
}

export function portLinks(sb: TaskSandbox): Array<{ port: number; url: string }> {
  if (!sb.publishes_ports) return [];
  return Object.entries(sb.urls ?? {})
    .map(([port, url]) => ({ port: Number(port), url }))
    .filter((p) => Number.isFinite(p.port) && /^https?:\/\//.test(p.url))
    .sort((a, b) => a.port - b.port);
}

export function killMessage(owner: string, publishesPorts: boolean): string {
  const msg = t("lib.sandboxes.kill_message", { owner });
  return publishesPorts ? msg : `${msg} ${t("lib.sandboxes.kill_kube_note")}`;
}

// Each column's `cls` is the hook the row updater finds the cell by.
export function sandboxColumns(): TableColumn[] {
  return [
    { label: t("sandboxes.columns.task"), cls: "owner" },
    { label: t("sandboxes.columns.container"), mono: true, width: "shrink", cls: "ctn" },
    { label: t("sandboxes.columns.status"), width: "shrink", cls: "status-cell" },
    { label: t("sandboxes.columns.idle"), num: true, mono: true, width: "shrink", cls: "idle" },
    { label: t("sandboxes.columns.ports"), mono: true, cls: "ports" },
    { label: t("sandboxes.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}

export function queueColumns(): TableColumn[] {
  return [
    { label: t("sandboxes.columns.position"), num: true, width: "shrink", cls: "pos" },
    { label: t("sandboxes.columns.task"), cls: "owner" },
    { label: t("sandboxes.columns.stage"), cls: "stage" },
    { label: t("sandboxes.columns.priority"), num: true, mono: true, width: "shrink", cls: "prio" },
    { label: t("sandboxes.columns.waiting"), mono: true, width: "shrink", cls: "wait" },
    { label: t("sandboxes.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}
