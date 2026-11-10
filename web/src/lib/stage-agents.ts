import { t } from "../i18n";
import { agentColumns } from "./pickers";
import type { Stage } from "./stages";

type Col = Pick<Stage, "id" | "label" | "initial" | "terminal"> & { virtual?: boolean };

interface Assigned {
  stage_assignments?: Record<string, string> | null;
}

export function staffedColumns(
  tasks: readonly Assigned[],
  templates: ReadonlyArray<Record<string, string> | null | undefined>,
): Set<string> {
  const out = new Set<string>();
  for (const x of tasks) {
    for (const [stage, agent] of Object.entries(x.stage_assignments ?? {})) {
      if (agent) out.add(stage);
    }
  }
  for (const tpl of templates) {
    for (const [stage, agent] of Object.entries(tpl ?? {})) {
      if (agent) out.add(stage);
    }
  }
  return out;
}

export interface NextAgent {
  stage: string;
  label: string;
  agent: string;
}

export function nextAgent<S extends Col>(
  task: Assigned & { stage: string },
  stages: readonly S[],
  staffed: ReadonlySet<string>,
  skips: ReadonlySet<string> = new Set(),
): NextAgent | null {
  const at = stages.findIndex((s) => s.id === task.stage);
  if (at < 0 || stages[at].terminal) return null;
  const next = stages.slice(at + 1).find((s) => !skips.has(s.id) && !s.virtual);
  if (!next || agentColumns([next]).length === 0) return null;
  const agent = task.stage_assignments?.[next.id] ?? "";
  if (!agent && !staffed.has(next.id)) return null;
  return { stage: next.id, label: next.label, agent };
}

export function dropHint(task: Assigned, stage: Col, blocked = false): string {
  if (blocked) return t("lib.stage_agents.drop_blocked");
  if (agentColumns([stage]).length === 0) return "";
  const agent = task.stage_assignments?.[stage.id] ?? "";
  return agent ? t("lib.stage_agents.drop_runs", { agent }) : t("lib.stage_agents.drop_none");
}

export function templateFill(
  assigned: Record<string, string> | null | undefined,
  template: Record<string, string> | null | undefined,
  columns: readonly string[],
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const id of columns) {
    const agent = template?.[id];
    if (agent && !assigned?.[id]) out[id] = agent;
  }
  return out;
}

// Mirrors the daemon's merge: the first project to name a column wins.
export function mergeTemplates(
  templates: ReadonlyArray<Record<string, string> | null | undefined>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const tpl of templates) {
    for (const [stage, agent] of Object.entries(tpl ?? {})) {
      if (agent && !(stage in out)) out[stage] = agent;
    }
  }
  return out;
}

export function newTaskAgentsText(
  projects: ReadonlyArray<{ name: string; stage_template?: Record<string, string> | null }>,
  stages: readonly Col[],
): string {
  if (projects.length === 0) return t("lib.stage_agents.new_no_project");
  const merged = mergeTemplates(projects.map((p) => p.stage_template));
  const list = agentColumns(stages)
    .filter((s) => merged[s.id])
    .map((s) => t("lib.stage_agents.next_runs", { stage: s.label, agent: merged[s.id] }));
  if (list.length === 0) {
    return t("lib.stage_agents.new_none", { projects: projects.map((p) => p.name).join(", ") });
  }
  return t("lib.stage_agents.new_copies", { list: list.join(" · ") });
}

// A blocked card is sent at once so the daemon's refusal shows now, not after an Undo window.
export function holdsForUndo(agent: string, blocked: boolean): boolean {
  return agent !== "" && !blocked;
}
