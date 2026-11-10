import { t } from "../i18n";
import { tableRowHTML, type TableColumn } from "./ui/table";

export type MCPAction = "start" | "stop" | "restart" | "edit" | "delete" | "connect";

export type MCPSource = "runtime" | "registered" | "env";

export interface MCPActionInput {
  source?: string;
  status?: string;
  health?: string;
}

export function sourceOf(sv: MCPActionInput): MCPSource {
  if (sv.source === "registered") return "registered";
  if (sv.source === "env") return "env";
  return "runtime";
}

export function actionsFor(sv: MCPActionInput): MCPAction[] {
  const src = sourceOf(sv);
  if (src === "registered") return ["edit", "delete"];
  if (src === "env") return [];
  switch (sv.status ?? "") {
    case "running":
    case "restarting":
    case "paused":
      return ["restart", "stop"];
    case "exited":
    case "created":
    case "dead":
      return ["start", "restart"];
    case "missing":
      return ["start"];
    default:
      return ["start", "restart", "stop"];
  }
}

export function isUp(sv: MCPActionInput): boolean {
  if (sv.status === "running") return true;
  if (sv.status === "configured" || sv.status === "external") return sv.health === "healthy";
  return false;
}

// Workspaces outside the operator's get no health for URL-only servers, so no health means
// unchecked, not down.
export function isUnchecked(sv: MCPActionInput): boolean {
  return (sv.status === "configured" || sv.status === "external") && !sv.health;
}

export function upCounts(list: MCPActionInput[]): { up: number; total: number; unchecked: number } {
  let up = 0;
  let unchecked = 0;
  for (const sv of list) {
    if (isUnchecked(sv)) unchecked++;
    else if (isUp(sv)) up++;
  }
  return { up, total: list.length - unchecked, unchecked };
}

export function upTone(c: { up: number; total: number }): "ok" | "danger" | "neutral" {
  if (c.up > 0) return "ok";
  return c.total > 0 ? "danger" : "neutral";
}

export function mcpColumns(): TableColumn[] {
  return [
    { label: t("mcps.columns.name"), width: "shrink" },
    { label: t("mcps.columns.source"), width: "shrink" },
    { label: t("mcps.columns.endpoint"), width: "fill" },
    { label: t("mcps.columns.status"), width: "shrink" },
    { label: t("mcps.columns.health"), width: "shrink" },
    { label: t("mcps.columns.uptime"), width: "shrink", mono: true, cls: "mcp-uptime" },
    { label: t("mcps.columns.actions"), width: "shrink", hideLabel: true, cls: "actions-cell" },
  ];
}

export function mcpRowHTML(): string {
  return tableRowHTML(
    mcpColumns(),
    {
      cells: [
        { html: '<span class="mcp-name"></span>' },
        { html: '<span class="source-cell"></span>' },
        { html: '<span class="mcp-endpoint"></span>' },
        { html: '<span class="status-cell"></span>' },
        { html: '<span class="health-cell"></span>' },
        "",
        { html: '<span class="cell-actions mcp-actions"></span>' },
      ],
    },
    { stack: true },
  );
}
