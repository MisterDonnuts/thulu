import { t } from "../i18n";
import type { TableColumn } from "./ui/table";

export type DimMetric =
  | "sessions"
  | "tokens"
  | "turns"
  | "cost"
  | "cache"
  | "perSess"
  | "perTurn"
  | "p95";

export const DIM_TABLES = {
  "t-engine": { key: "engine", metrics: ["sessions", "tokens", "cost", "cache"] },
  "t-model": { key: "model", metrics: ["sessions", "tokens", "cost", "perSess", "p95"] },
  "t-agent": { key: "agent", metrics: ["sessions", "turns", "cost", "perTurn"] },
  "t-project": { key: "project", metrics: ["sessions", "tokens", "cost"] },
} as const satisfies Record<string, { key: string; metrics: readonly DimMetric[] }>;

export type DimTableId = keyof typeof DIM_TABLES;

function metricLabel(m: DimMetric): string {
  switch (m) {
    case "sessions":
      return t("usage.columns.sessions");
    case "tokens":
      return t("usage.columns.tokens");
    case "turns":
      return t("usage.columns.turns");
    case "cost":
      return t("usage.columns.cost");
    case "cache":
      return t("usage.columns.cache_pct");
    case "perSess":
      return t("usage.columns.cost_per_session");
    case "perTurn":
      return t("usage.columns.tokens_per_turn");
    case "p95":
      return t("usage.columns.p95_duration");
  }
}

function keyLabel(key: (typeof DIM_TABLES)[DimTableId]["key"]): string {
  switch (key) {
    case "engine":
      return t("usage.columns.engine");
    case "model":
      return t("usage.columns.model");
    case "agent":
      return t("usage.columns.agent");
    case "project":
      return t("usage.columns.project");
  }
}

const num = (label: string): TableColumn => ({ label, num: true });

export function dimColumns(id: DimTableId): TableColumn[] {
  const spec = DIM_TABLES[id];
  return [{ label: keyLabel(spec.key) }, ...spec.metrics.map((m) => num(metricLabel(m)))];
}

export function toolColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.tool") },
    { label: t("usage.columns.kind"), width: "shrink" },
    num(t("usage.columns.calls")),
    num(t("usage.columns.sessions")),
    num(t("usage.columns.result_tokens")),
    num(t("usage.columns.carried")),
    num(t("usage.columns.cost")),
  ];
}

export function skillColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.skill") },
    num(t("usage.columns.calls")),
    num(t("usage.columns.tasks")),
    num(t("usage.columns.result_tokens")),
    num(t("usage.columns.cost")),
  ];
}

export function mcpServerColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.mcp_server") },
    num(t("usage.columns.calls")),
    num(t("usage.columns.sessions")),
    num(t("usage.columns.result_tokens")),
    num(t("usage.columns.cost")),
  ];
}

export function delegationAgentColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.agent") },
    { label: t("usage.columns.stage") },
    num(t("usage.columns.delegations")),
    num(t("usage.columns.cost")),
  ];
}

export function delegationRecentColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.when"), width: "shrink" },
    { label: t("usage.columns.run") },
    { label: t("usage.columns.kind"), width: "shrink" },
    { label: t("usage.columns.model") },
    num(t("usage.columns.files")),
    num(t("usage.columns.tokens_in_out")),
    num(t("usage.columns.cost")),
    num(t("usage.columns.duration")),
    { label: t("usage.columns.status"), width: "shrink" },
  ];
}

export function optimizerColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.level") },
    num(t("usage.columns.sessions")),
    num(t("usage.columns.cost")),
    num(t("usage.columns.cost_per_session")),
    num(t("usage.columns.tools_cost")),
    num(t("usage.columns.delegations_cost")),
  ];
}

export function topTaskColumns(): TableColumn[] {
  return [
    { label: t("usage.columns.task") },
    num(t("usage.columns.sessions")),
    num(t("usage.columns.turns")),
    num(t("usage.columns.tokens")),
    num(t("usage.columns.cost")),
    num(t("usage.columns.duration")),
    { label: t("usage.columns.open"), hideLabel: true, width: "shrink" },
  ];
}
