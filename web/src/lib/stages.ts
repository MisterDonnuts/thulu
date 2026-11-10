// Stage behavior belongs to the board: read flags by (board, stage), never by stage id alone.

import type { RequiredApproval } from "./required-approvals";
import { tagHTML, type TagOptions } from "./ui/tag";

export interface Stage {
  id: string;
  label: string;
  color: string;
  position: number;
  auto_advance: boolean;
  blocked_by_flags: boolean;
  initial: boolean;
  terminal: boolean;
  ingest_flags: boolean;
  push_branch: boolean;
  open_pr: boolean;
  wip_group: string;
  virtual: boolean;
  required_approvals?: RequiredApproval[] | null;
  on_changes_requested?: string;
  changes_kick_back_to?: string;
}

export interface Board {
  id: string;
  label: string;
  description?: string;
  position: number;
  default?: boolean;
  workspace_id?: string;
  stage_count?: number;
  task_count?: number;
  open_count?: number;
}

export interface BoardsResponse {
  boards: Board[];
  default: string;
}

export interface TaskType {
  id: string;
  label: string;
  icon?: string;
  color?: string;
  position: number;
  default?: boolean;
  skips: string[];
  task_count?: number;
}

export function taskTypeOf(types: TaskType[], typeID: string | undefined): TaskType | undefined {
  return typeID ? types.find((t) => t.id === typeID) : undefined;
}

// Untyped and unknown types walk the whole board, matching the backend's fallback.
export function typeAllowsStage(
  types: TaskType[],
  typeID: string | undefined,
  stageID: string,
): boolean {
  const t = taskTypeOf(types, typeID);
  return !t?.skips.includes(stageID);
}

export function stageIndex(stages: Stage[]): Map<string, Stage> {
  return new Map(stages.map((s) => [s.id, s]));
}

export function isTerminal(idx: Map<string, Stage>, stageID: string): boolean {
  return idx.get(stageID)?.terminal ?? false;
}

export function boardStageKey(boardID: string, stageID: string): string {
  return `${boardID || "default"}/${stageID}`;
}

export function crossBoardIndex(boards: Record<string, Stage[]>): Map<string, Stage> {
  const idx = new Map<string, Stage>();
  for (const [boardID, stages] of Object.entries(boards)) {
    for (const s of stages) idx.set(boardStageKey(boardID, s.id), s);
  }
  return idx;
}

export function stageOf(
  idx: Map<string, Stage>,
  boardID: string,
  stageID: string,
): Stage | undefined {
  return idx.get(boardStageKey(boardID, stageID));
}

export type StagePillOptions = Pick<
  TagOptions,
  "size" | "button" | "pressed" | "href" | "title" | "id" | "cls" | "attrs"
>;

export function stagePillHTML(
  stageID: string,
  stage: Pick<Stage, "label" | "color"> | null | undefined,
  opts: StagePillOptions = {},
): string {
  const label = stage?.label ?? "";
  return tagHTML(label === "" ? stageID : label, { ...opts, color: stage?.color, dot: true });
}
