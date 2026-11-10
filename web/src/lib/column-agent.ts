import { t } from "../i18n";
import { api } from "./api";
import { agentColumns, agentCovers } from "./pickers";
import type { Stage } from "./stages";

export interface TemplateProject {
  id: string;
  name: string;
  default_board_id?: string;
  stage_template?: Record<string, string> | null;
}

export function boardProjects<P extends TemplateProject>(
  projects: readonly P[],
  boardID: string,
  defaultBoardID: string,
): P[] {
  return projects.filter((p) => {
    const own = p.default_board_id ?? "";
    return own === boardID || (own === "" && boardID === defaultBoardID);
  });
}

export interface ColumnAgent {
  agents: string[];
  with: number;
  projects: number;
}

export function columnAgent(projects: readonly TemplateProject[], stageID: string): ColumnAgent {
  const agents = new Set<string>();
  let with_ = 0;
  for (const p of projects) {
    const a = p.stage_template?.[stageID];
    if (a) {
      agents.add(a);
      with_++;
    }
  }
  return { agents: [...agents].sort(), with: with_, projects: projects.length };
}

export const MIXED = "*";

export function pickerValue(state: ColumnAgent): string {
  if (state.with === 0) return "";
  if (state.agents.length === 1 && state.with === state.projects) return state.agents[0];
  return MIXED;
}

export function templateWith(
  template: Record<string, string> | null | undefined,
  stageID: string,
  agent: string,
): Record<string, string> {
  const out = Object.fromEntries(Object.entries(template ?? {}).filter(([id]) => id !== stageID));
  if (agent) out[stageID] = agent;
  return out;
}

export function columnAgentText(stage: Stage, state: ColumnAgent): string {
  if (stage.initial || stage.terminal) return "";
  if (state.with === 0) return t("lib.column_agent.none");
  if (state.agents.length === 1 && state.with === state.projects) {
    return t("lib.column_agent.one", { agent: state.agents[0] });
  }
  return t("lib.column_agent.some", {
    agents: state.agents.join(", "),
    with: state.with,
    count: state.projects,
  });
}

export function waitsForPerson(state: ColumnAgent): boolean {
  return state.with === 0;
}

export function takesAgent(stage: Pick<Stage, "initial" | "terminal">): boolean {
  return agentColumns([stage]).length > 0;
}

export function agentChoices(
  agents: ReadonlyArray<{ name: string; type?: string; valid_stages?: string[] | null }>,
  stageID: string,
): string[] {
  return agents.filter((a) => agentCovers(a, stageID)).map((a) => (a.type ?? "") || a.name);
}

export interface SaveResult {
  saved: number;
  failed: Array<{ project: string; error: string }>;
}

export async function saveColumnAgent<P extends TemplateProject>(
  projects: readonly P[],
  stageID: string,
  agent: string,
  patch: (id: string, body: { stage_template: Record<string, string> }) => Promise<P> = (
    id,
    body,
  ) => api.patch<P>(`/projects/${encodeURIComponent(id)}`, body),
): Promise<{ result: SaveResult; projects: P[] }> {
  const result: SaveResult = { saved: 0, failed: [] };
  const out = await Promise.all(
    projects.map(async (p) => {
      const next = templateWith(p.stage_template, stageID, agent);
      if (JSON.stringify(next) === JSON.stringify(p.stage_template ?? {})) return p;
      try {
        const saved = await patch(p.id, { stage_template: next });
        result.saved++;
        return { ...p, ...saved, stage_template: saved.stage_template ?? next };
      } catch (e) {
        result.failed.push({ project: p.name, error: e instanceof Error ? e.message : String(e) });
        return p;
      }
    }),
  );
  return { result, projects: out };
}

export interface ColumnAgentPicker {
  state(stageID: string): ColumnAgent;
  choices(stageID: string): string[];
  save(stageID: string, agent: string): Promise<SaveResult>;
  projectsHref: string;
}

interface AgentLike {
  name: string;
  type?: string;
  valid_stages?: string[] | null;
}

export function projectAgentPicker(src: {
  board: () => string;
  defaultBoard: () => string;
  projects: () => TemplateProject[];
  setProjects: (list: TemplateProject[]) => void;
  agents: () => readonly AgentLike[];
  onChange?: () => void;
}): ColumnAgentPicker {
  const mine = () => boardProjects(src.projects(), src.board(), src.defaultBoard());
  return {
    projectsHref: "/projects/",
    state: (stageID) => columnAgent(mine(), stageID),
    choices: (stageID) => agentChoices(src.agents(), stageID),
    async save(stageID, agent) {
      const { result, projects } = await saveColumnAgent(mine(), stageID, agent);
      const byID = new Map(projects.map((p) => [p.id, p]));
      src.setProjects(src.projects().map((p) => byID.get(p.id) ?? p));
      src.onChange?.();
      return result;
    },
  };
}

export function columnToOffer<S extends Pick<Stage, "id" | "initial" | "terminal">>(
  stages: readonly S[],
  projects: readonly TemplateProject[],
): S | undefined {
  const cols = agentColumns(stages);
  return cols.find((s) => columnAgent(projects, s.id).with < projects.length) ?? cols[0];
}

export function validStagesWith(
  valid: readonly string[] | null | undefined,
  stageID: string,
): string[] {
  const list = [...(valid ?? [])];
  if (list.length === 0 || list.includes(stageID)) return list;
  return [...list, stageID];
}
