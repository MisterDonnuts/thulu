import { t } from "../i18n";
import { agentColumns } from "./pickers";
import type { Stage, TaskType } from "./stages";

export const TYPE_TEMPLATES: Array<{ id: string; label: string; icon: string; color: string }> = [
  {
    id: "feature",
    label: t("lib.board_types.templates.feature"),
    icon: "feature",
    color: "#56ccf2",
  },
  { id: "bugfix", label: t("lib.board_types.templates.bugfix"), icon: "bug", color: "#e5484d" },
  { id: "chore", label: t("lib.board_types.templates.chore"), icon: "chore", color: "#8a92a6" },
  { id: "spike", label: t("lib.board_types.templates.spike"), icon: "spike", color: "#bb86fc" },
];

export function isLockedStage(st: Pick<Stage, "initial" | "terminal">): boolean {
  return st.initial || st.terminal;
}

export function toggleSkip(skips: string[], stageID: string, walks: boolean): string[] {
  if (walks) return skips.filter((s) => s !== stageID);
  return skips.includes(stageID) ? skips : [...skips, stageID];
}

export function newType(
  tpl: { id: string; label: string; icon?: string; color?: string },
  count: number,
): TaskType {
  return {
    id: tpl.id,
    label: tpl.label,
    icon: tpl.icon,
    color: tpl.color,
    position: (count + 1) * 10,
    skips: [],
  };
}

export function validateNewType(id: string, label: string, existing: TaskType[]): string | null {
  if (!id || !label) return t("lib.board_types.errors.id_label_required");
  if (existing.some((tt) => tt.id === id)) return t("lib.board_types.errors.duplicate", { id });
  return null;
}

// The select value for "same agent as the column": not a valid agent name, and not "" (no agent).
export const TYPE_AGENT_COLUMN = "*column";

// The columns where a type may name its own agent: the ones an agent runs in and the type walks.
// Must match the daemon, which refuses agents on a skipped, start or finishing column.
export function typeAgentStages<S extends Pick<Stage, "id" | "initial" | "terminal">>(
  stages: readonly S[],
  tt: Pick<TaskType, "skips">,
): S[] {
  return agentColumns(stages).filter((st) => !tt.skips.includes(st.id));
}

export function typeAgentValue(tt: Pick<TaskType, "agents">, stageID: string): string {
  const agents = tt.agents ?? {};
  return stageID in agents ? agents[stageID] : TYPE_AGENT_COLUMN;
}

export function withTypeAgent(
  agents: Record<string, string> | null | undefined,
  stageID: string,
  value: string,
): Record<string, string> {
  const out = Object.fromEntries(Object.entries(agents ?? {}).filter(([id]) => id !== stageID));
  if (value !== TYPE_AGENT_COLUMN) out[stageID] = value;
  return out;
}

// Skipping a column also drops the type's agent there, or the daemon refuses the list.
export function walkStage(tt: TaskType, stageID: string, walks: boolean): TaskType {
  const skips = toggleSkip(tt.skips, stageID, walks);
  if (walks || !tt.agents || !(stageID in tt.agents)) return { ...tt, skips };
  return { ...tt, skips, agents: withTypeAgent(tt.agents, stageID, TYPE_AGENT_COLUMN) };
}
