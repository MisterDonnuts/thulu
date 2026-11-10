import { locale, t } from "../i18n";
import { fmtCount, fmtUsdCompact } from "./format";
import { fmtDate } from "./time";
import { isOptimizerLevel, levelLabel } from "./token-optimizer";
import {
  badgeHTML,
  chartLegendHTML,
  esc,
  factsHTML,
  progressBarHTML,
  sectionHeadHTML,
  tableHTML,
  type Fact,
  type ProgressTone,
  type TableColumn,
  type TableRow,
} from "./ui";

export type CostCategory =
  | "startup"
  | "reasoning"
  | "user_input"
  | "tools"
  | "subagents"
  | "compaction"
  | "delegation"
  | "unattributed";

export interface CategoryCost {
  category: CostCategory | string;
  weighted_tokens: number;
  cost_usd: number;
}

export interface ToolCallCost {
  tool_use_id: string;
  tool_name: string;
  tool_kind?: string;
  mcp_server?: string;
  skill_name?: string;
  turn_index?: number;
  ts?: string;
  result_bytes?: number;
  result_tokens: number;
  args_tokens?: number;
  carried_calls: number;
  weighted_tokens?: number;
  cost_usd: number;
  is_error?: boolean;
  delegation_id?: string;
  delegated_cost_usd?: number;
  input_summary?: string;
}

export interface TopCall extends ToolCallCost {
  session_id: string;
  agent?: string;
  stage?: string;
}

export interface ApiCall {
  idx: number;
  model?: string;
  input_tokens: number;
  cache_read: number;
  cache_creation: number;
  output_tokens: number;
  prompt_size: number;
  compaction?: boolean;
  ts?: string;
}

export interface SessionBreakdown {
  session_id: string;
  attribution_version: number;
  cost_usd: number;
  unattributed_usd: number;
  api_calls: number;
  base_tokens: number;
  compactions: number;
  token_optimizer?: string | { level?: string } | null;
  categories: CategoryCost[] | null;
  tools: ToolCallCost[] | null;
  calls: ApiCall[] | null;
}

export interface TaskBreakdown {
  categories: CategoryCost[] | null;
  cost_usd: number;
  unattributed_usd: number;
  top_calls: TopCall[] | null;
  delegations?: { count: number; cost_usd: number } | null;
}

export const CATEGORIES: Array<{ id: CostCategory; tone: ProgressTone }> = [
  { id: "startup", tone: "viz-1" },
  { id: "user_input", tone: "viz-2" },
  { id: "reasoning", tone: "viz-3" },
  { id: "tools", tone: "viz-4" },
  { id: "subagents", tone: "viz-5" },
  { id: "compaction", tone: "viz-6" },
  { id: "delegation", tone: "viz-7" },
  { id: "unattributed", tone: "viz-8" },
];

const CATEGORY_KEYS: Record<CostCategory, string> = {
  startup: "lib.usage_breakdown.categories.startup",
  reasoning: "lib.usage_breakdown.categories.reasoning",
  user_input: "lib.usage_breakdown.categories.user_input",
  tools: "lib.usage_breakdown.categories.tools",
  subagents: "lib.usage_breakdown.categories.subagents",
  compaction: "lib.usage_breakdown.categories.compaction",
  delegation: "lib.usage_breakdown.categories.delegation",
  unattributed: "lib.usage_breakdown.categories.unattributed",
};

export function categoryLabel(id: string): string {
  return Object.hasOwn(CATEGORY_KEYS, id) ? t(CATEGORY_KEYS[id as CostCategory]) : id;
}

export function hasBreakdown(b: TaskBreakdown | null | undefined): b is TaskBreakdown {
  return !!b && (b.categories?.length ?? 0) > 0;
}

export function orderedCategories(cats: CategoryCost[] | null | undefined): CategoryCost[] {
  const list = (cats ?? []).filter((c) => c.cost_usd > 0);
  const rank = (id: string) => {
    const i = CATEGORIES.findIndex((c) => c.id === id);
    return i < 0 ? CATEGORIES.length : i;
  };
  return [...list].sort((a, b) => rank(a.category) - rank(b.category));
}

function toneOf(id: string): ProgressTone {
  return CATEGORIES.find((c) => c.id === id)?.tone ?? "muted";
}

function colorOf(id: string): string {
  const tone = toneOf(id);
  return tone === "muted" ? "var(--fg-4)" : `var(--${tone})`;
}

export function categoryBarHTML(cats: CategoryCost[] | null | undefined): string {
  const list = orderedCategories(cats);
  if (!list.length) return "";
  const parts = list.map((c) => ({
    value: c.cost_usd,
    tone: toneOf(c.category),
    label: t("lib.usage_breakdown.part", {
      category: categoryLabel(c.category),
      cost: fmtUsdCompact(c.cost_usd),
    }),
  }));
  const bar = progressBarHTML(parts, { size: "md", cls: "ub-bar" });
  const legend = chartLegendHTML(
    list.map((c) => ({
      label: categoryLabel(c.category),
      color: colorOf(c.category),
      swatch: "square" as const,
      value: fmtUsdCompact(c.cost_usd),
    })),
  );
  return `<div class="ub-cats">${bar}${legend}</div>`;
}

function toolCell(r: ToolCallCost): { html: string; subHTML?: string } {
  const from = r.mcp_server
    ? t("lib.usage_breakdown.from_mcp", { server: r.mcp_server })
    : r.skill_name
      ? t("lib.usage_breakdown.from_skill", { skill: r.skill_name })
      : "";
  const sub = [r.input_summary, from].filter(Boolean).join(" · ");
  return {
    html: `<span class="mono">${esc(r.tool_name)}</span>`,
    subHTML: sub ? esc(sub) : undefined,
  };
}

function costCell(r: ToolCallCost): { html: string } {
  const marks = [
    r.delegation_id
      ? badgeHTML(t("lib.usage_breakdown.delegated"), "accent", {
          size: "sm",
          title: t("lib.usage_breakdown.delegated_title", {
            id: r.delegation_id,
            cost: fmtUsdCompact(r.delegated_cost_usd ?? 0),
          }),
        })
      : "",
    r.is_error ? badgeHTML(t("lib.usage_breakdown.error"), "danger", { size: "sm" }) : "",
  ].filter(Boolean);
  return { html: [esc(fmtUsdCompact(r.cost_usd)), ...marks].join(" ") };
}

function runOf(r: TopCall): string {
  return [r.agent, r.stage].filter(Boolean).join(" · ");
}

export function topCallColumns(): TableColumn[] {
  return [
    { label: t("lib.usage_breakdown.columns.tool"), width: "title" },
    { label: t("lib.usage_breakdown.columns.run") },
    { label: t("lib.usage_breakdown.columns.result_tokens"), num: true },
    { label: t("lib.usage_breakdown.columns.carried"), num: true },
    { label: t("lib.usage_breakdown.columns.cost"), num: true },
    { label: t("lib.usage_breakdown.columns.open"), hideLabel: true, width: "shrink" },
  ];
}

export function topCallRows(calls: TopCall[] | null | undefined): TableRow[] {
  return [...(calls ?? [])]
    .sort((a, b) => b.cost_usd - a.cost_usd)
    .slice(0, 10)
    .map((r) => ({
      cells: [
        toolCell(r),
        runOf(r) || "—",
        fmtCount(r.result_tokens || 0),
        (r.carried_calls || 0).toLocaleString(locale),
        costCell(r),
        {
          actions: [
            {
              label: t("lib.usage_breakdown.open_run", { run: runOf(r) || r.session_id }),
              icon: "arrow-up-right",
              iconOnly: true,
              variant: "ghost",
              attrs: { "data-breakdown-session": r.session_id },
            },
          ],
        },
      ],
    }));
}

// sessions counts only the runs that reported tokens; run_ids lists every run the task started.
// A run still booting, or one whose engine never reported usage, has an id and no session, so
// "no runs yet" goes by run_ids and such a run shows no usage block at all.
export type TaskUsageView = "no_runs" | "no_usage" | "usage";

export function taskUsageView(u: {
  sessions?: number | null;
  run_ids?: string[] | null;
}): TaskUsageView {
  if ((u.sessions ?? 0) > 0) return "usage";
  return (u.run_ids ?? []).length > 0 ? "no_usage" : "no_runs";
}

export function taskBreakdownHTML(b: TaskBreakdown | null | undefined): string {
  if (!hasBreakdown(b)) return "";
  const dl = b.delegations;
  const delegations =
    dl && dl.count > 0
      ? `<p class="ub-note">${esc(
          t("lib.usage_breakdown.delegations", {
            count: dl.count,
            cost: fmtUsdCompact(dl.cost_usd),
          }),
        )}</p>`
      : "";
  const unattributed =
    b.unattributed_usd > 0
      ? `<p class="ub-note">${esc(
          t("lib.usage_breakdown.unattributed_note", {
            cost: fmtUsdCompact(b.unattributed_usd),
            total: fmtUsdCompact(b.cost_usd),
          }),
        )}</p>`
      : "";
  const rows = topCallRows(b.top_calls);
  const top = rows.length
    ? sectionHeadHTML({ title: t("lib.usage_breakdown.top_calls"), level: 4 }) +
      tableHTML(topCallColumns(), rows, {
        compact: true,
        stack: true,
        label: t("lib.usage_breakdown.top_calls"),
      })
    : "";
  return `<div class="ub-task">${sectionHeadHTML({ title: t("lib.usage_breakdown.title"), level: 4 })}${categoryBarHTML(b.categories)}${delegations}${unattributed}${top}</div>`;
}

export function runOptimizerLevel(v: SessionBreakdown["token_optimizer"]): string {
  if (!v) return "";
  return typeof v === "string" ? v : (v.level ?? "");
}

export function toolCallColumns(): TableColumn[] {
  return [
    { label: t("lib.usage_breakdown.columns.tool"), width: "title" },
    { label: t("lib.usage_breakdown.columns.turn"), num: true },
    { label: t("lib.usage_breakdown.columns.result_tokens"), num: true },
    { label: t("lib.usage_breakdown.columns.carried"), num: true },
    { label: t("lib.usage_breakdown.columns.cost"), num: true },
  ];
}

export function apiCallColumns(): TableColumn[] {
  return [
    { label: "#", num: true, width: "shrink" }, // i18n-ignore the call's ordinal, a symbol
    { label: t("lib.usage_breakdown.columns.model"), mono: true },
    { label: t("lib.usage_breakdown.columns.input"), num: true },
    { label: t("lib.usage_breakdown.columns.cache_read"), num: true },
    { label: t("lib.usage_breakdown.columns.cache_write"), num: true },
    { label: t("lib.usage_breakdown.columns.output"), num: true },
    { label: t("lib.usage_breakdown.columns.prompt"), num: true },
    { label: t("lib.usage_breakdown.columns.time"), width: "shrink" },
  ];
}

export function sessionBreakdownHTML(b: SessionBreakdown): string {
  if (!b.attribution_version) {
    return `<p class="muted">${esc(t("lib.usage_breakdown.not_attributed"))}</p>`;
  }
  const level = runOptimizerLevel(b.token_optimizer);
  const facts: Fact[] = [
    { label: t("lib.usage_breakdown.facts.cost"), value: fmtUsdCompact(b.cost_usd) },
    {
      label: t("lib.usage_breakdown.facts.api_calls"),
      value: (b.api_calls || 0).toLocaleString(locale),
    },
    { label: t("lib.usage_breakdown.facts.base_tokens"), value: fmtCount(b.base_tokens || 0) },
    {
      label: t("lib.usage_breakdown.facts.compactions"),
      value: (b.compactions || 0).toLocaleString(locale),
    },
    {
      label: t("lib.usage_breakdown.facts.unattributed"),
      value: fmtUsdCompact(b.unattributed_usd || 0),
    },
    {
      label: t("lib.usage_breakdown.facts.optimizer"),
      value: isOptimizerLevel(level) ? levelLabel(level) : level || "—",
    },
  ];
  const tools = [...(b.tools ?? [])].sort((x, y) => y.cost_usd - x.cost_usd);
  const toolRows: TableRow[] = tools.map((r) => ({
    cells: [
      toolCell(r),
      r.turn_index ?? "—",
      fmtCount(r.result_tokens || 0),
      (r.carried_calls || 0).toLocaleString(locale),
      costCell(r),
    ],
    tone: r.is_error ? "danger" : undefined,
  }));
  const callRows: TableRow[] = (b.calls ?? []).map((c) => ({
    cells: [
      c.idx,
      c.model?.length ? c.model : "—",
      fmtCount(c.input_tokens || 0),
      fmtCount(c.cache_read || 0),
      fmtCount(c.cache_creation || 0),
      fmtCount(c.output_tokens || 0),
      fmtCount(c.prompt_size || 0),
      {
        html:
          (c.ts ? esc(fmtDate(c.ts)) : "—") +
          (c.compaction
            ? ` ${badgeHTML(t("lib.usage_breakdown.compaction"), "warn", { size: "sm" })}`
            : ""),
      },
    ],
  }));
  return (
    factsHTML(facts, { layout: "inline", boxed: true }) +
    categoryBarHTML(b.categories) +
    sectionHeadHTML({ title: t("lib.usage_breakdown.tool_calls"), level: 4, count: tools.length }) +
    tableHTML(toolCallColumns(), toolRows, {
      compact: true,
      stack: true,
      empty: t("lib.usage_breakdown.no_tools"),
    }) +
    sectionHeadHTML({
      title: t("lib.usage_breakdown.api_calls"),
      level: 4,
      count: callRows.length,
    }) +
    tableHTML(apiCallColumns(), callRows, {
      compact: true,
      stack: true,
      empty: t("lib.usage_breakdown.no_calls"),
    })
  );
}
