import { t } from "../i18n";

export const RUN_CASES_MCPS = ["thulu-browser"] as const;

export interface RunAgentSource {
  type: string;
  name: string;
  valid_stages?: string[];
  mcp_by_stage?: Record<string, string[]>;
}

export interface RunAgent {
  type: string;
  name: string;
  stage: "mention" | "chat";
  missing: string[];
}

export type RunCasesBlock = "" | "no_agent" | "no_browser";

// Mirrors the daemon's types.AnswersMentions and AnswersMentionsByChat; change together.
export function answeringStage(a: Pick<RunAgentSource, "valid_stages">): "mention" | "chat" | "" {
  const stages = a.valid_stages ?? [];
  if (stages.length === 0 || stages.includes("mention")) return "mention";
  return stages.includes("chat") ? "chat" : "";
}

const serverName = (entry: string): string => entry.split("@")[0].trim();

export function runAgents(agents: RunAgentSource[]): RunAgent[] {
  const out: RunAgent[] = [];
  for (const a of agents) {
    const stage = answeringStage(a);
    if (!stage) continue;
    const wired = (a.mcp_by_stage?.[stage] ?? []).map(serverName);
    out.push({
      type: a.type,
      name: a.name || a.type,
      stage,
      missing: RUN_CASES_MCPS.filter((s) => !wired.includes(s)),
    });
  }
  return out;
}

export function runCasesBlock(agents: RunAgent[]): RunCasesBlock {
  if (agents.length === 0) return "no_agent";
  return agents.some((a) => a.missing.length === 0) ? "" : "no_browser";
}

export function runCasesReason(block: RunCasesBlock): string {
  if (block === "no_agent") return t("lib.test_case_run.reasons.no_agent");
  if (block === "no_browser") return t("lib.test_case_run.reasons.no_browser");
  return "";
}

export function defaultRunAgent(agents: RunAgent[], assigned: string): string {
  const ready = agents.filter((a) => a.missing.length === 0);
  return (
    ready.find((a) => a.type === assigned || a.name === assigned)?.type ??
    ready.at(0)?.type ??
    agents.at(0)?.type ??
    ""
  );
}

export function missingLine(a: RunAgent | undefined): string {
  if (!a || a.missing.length === 0) return "";
  return t("lib.test_case_run.missing", {
    agent: a.name,
    servers: a.missing.join(", "),
    stage:
      a.stage === "chat" ? t("lib.test_case_run.stage_chat") : t("lib.test_case_run.stage_mention"),
  });
}

export interface RunCasesRequest {
  agent: string;
  url: string;
  compare: string;
  keys: string[];
}

export function runCasesComment(r: RunCasesRequest): string {
  const vars = { agent: r.agent, keys: r.keys.join(", "), url: r.url, compare: r.compare };
  const compare = !!r.compare.trim();
  const ask = compare
    ? t("lib.test_case_run.comment_compare", vars)
    : t("lib.test_case_run.comment", vars);
  const how = compare
    ? t("lib.test_case_run.instructions_compare")
    : t("lib.test_case_run.instructions");
  return `${ask}\n\n<details><summary>${t("lib.test_case_run.instructions_summary")}</summary>\n\n${how}\n\n</details>`;
}
