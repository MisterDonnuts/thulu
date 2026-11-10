import { locale, t } from "../i18n";
import { fmtCount, fmtUsdCompact } from "./format";
import { taskHref } from "./links";
import { fmtDate, fmtMs } from "./time";
import { isOptimizerLevel, levelLabel } from "./token-optimizer";
import { badgeHTML, esc, type BadgeTone, type TableCell } from "./ui";

// Daemons from before per-call attribution send cost_share_usd instead of cost_usd.
export function attributedCost(r: { cost_usd?: number; cost_share_usd?: number }): number {
  return r.cost_usd ?? r.cost_share_usd ?? 0;
}

export interface MCPServerRow {
  mcp_server: string;
  invocations: number;
  sessions: number;
  result_tokens: number;
  weighted_tokens: number;
  cost_usd: number;
}

export interface DelegationSummary {
  count: number;
  ok: number;
  failed: number;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
  avg_duration_ms: number;
}

export interface DelegationAgentRow {
  agent: string;
  stage: string;
  count: number;
  cost_usd: number;
}

export interface DelegationRow {
  id: string;
  agent: string;
  stage: string;
  kind: string;
  model: string;
  provider: string;
  files: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  duration_ms: number;
  status: string;
  created_at: string;
  task_id?: string;
}

export interface DelegationsReport {
  summary: DelegationSummary;
  by_agent: DelegationAgentRow[] | null;
  recent: DelegationRow[] | null;
}

export interface OptimizerLevelRow {
  level: string;
  sessions: number;
  cost_usd: number;
  avg_cost_usd: number;
  tools_cost_usd: number;
  delegations_cost_usd: number;
}

const count = (n: number) => (n || 0).toLocaleString(locale);
const id = (s: string) => ({ html: `<span class="key">${esc(s)}</span>` });

export function mcpServerCells(r: MCPServerRow): TableCell[] {
  return [
    id(r.mcp_server),
    count(r.invocations),
    count(r.sessions),
    fmtCount(r.result_tokens || 0),
    fmtUsdCompact(r.cost_usd || 0),
  ];
}

export function delegationTiles(
  s: DelegationSummary | null | undefined,
): Array<{ id: string; value: string; sub: string }> {
  const sum: DelegationSummary = s ?? {
    count: 0,
    ok: 0,
    failed: 0,
    cost_usd: 0,
    input_tokens: 0,
    output_tokens: 0,
    avg_duration_ms: 0,
  };
  return [
    {
      id: "dlg-count",
      value: count(sum.count),
      sub: t("usage.delegations.ok_failed", { ok: count(sum.ok), failed: count(sum.failed) }),
    },
    {
      id: "dlg-cost",
      value: fmtUsdCompact(sum.cost_usd || 0),
      sub: t("usage.delegations.tokens_in_out", {
        input: fmtCount(sum.input_tokens || 0),
        output: fmtCount(sum.output_tokens || 0),
      }),
    },
    { id: "dlg-duration", value: fmtMs(sum.avg_duration_ms || 0), sub: "" },
  ];
}

export function delegationAgentCells(r: DelegationAgentRow): TableCell[] {
  return [id(r.agent), r.stage ? id(r.stage) : "—", count(r.count), fmtUsdCompact(r.cost_usd || 0)];
}

const KIND_KEYS: Record<string, string> = {
  read: "usage.delegations.kinds.read",
  write: "usage.delegations.kinds.write",
};

export function delegationStatusTone(status: string): BadgeTone {
  if (status === "ok") return "ok";
  if (status === "started") return "neutral";
  return "danger";
}

export function delegationRowCells(r: DelegationRow): TableCell[] {
  const run = [r.agent, r.stage].filter(Boolean).join(" · ");
  const task = r.task_id
    ? `<a class="key" href="${esc(taskHref(r.task_id))}">${esc(r.task_id)}</a>`
    : undefined;
  return [
    r.created_at ? fmtDate(r.created_at) : "—",
    { html: run ? esc(run) : "—", subHTML: task },
    Object.hasOwn(KIND_KEYS, r.kind) ? t(KIND_KEYS[r.kind]) : r.kind || "—",
    { html: r.model ? id(r.model).html : "—", sub: r.provider || undefined },
    count(r.files),
    `${fmtCount(r.input_tokens || 0)} / ${fmtCount(r.output_tokens || 0)}`,
    fmtUsdCompact(r.cost_usd || 0),
    fmtMs(r.duration_ms || 0),
    {
      html: badgeHTML(r.status || "—", delegationStatusTone(r.status), { size: "sm", mono: true }),
    },
  ];
}

export function optimizerLevelName(level: string): string {
  if (isOptimizerLevel(level)) return levelLabel(level);
  return level || t("usage.optimizer.no_level");
}

export function sortOptimizerRows(
  rows: OptimizerLevelRow[] | null | undefined,
): OptimizerLevelRow[] {
  const rank = (l: string) => ["off", "suggest", "enforce"].indexOf(l) + 1 || 4;
  return [...(rows ?? [])].sort((a, b) => rank(a.level) - rank(b.level));
}

export function optimizerCells(r: OptimizerLevelRow): TableCell[] {
  return [
    optimizerLevelName(r.level),
    count(r.sessions),
    fmtUsdCompact(r.cost_usd || 0),
    fmtUsdCompact(r.avg_cost_usd || 0),
    fmtUsdCompact(r.tools_cost_usd || 0),
    fmtUsdCompact(r.delegations_cost_usd || 0),
  ];
}
