import type { Board } from "./stages";

// Dev tokens and operator keys carry no workspace, and the daemon refuses a project it cannot
// place, so infer one from the loaded boards.
export function createWorkspace(
  session: { workspace_id?: string } | null,
  boards: Array<Pick<Board, "default" | "workspace_id">>,
): string {
  if (session?.workspace_id) return "";
  const def = boards.find((b) => b.default)?.workspace_id;
  if (def) return def;
  const all = [...new Set(boards.map((b) => b.workspace_id ?? "").filter(Boolean))];
  return all.length === 1 ? (all[0] ?? "") : "";
}

export function withWorkspace(path: string, ws: string): string {
  if (!ws) return path;
  return `${path}${path.includes("?") ? "&" : "?"}workspace=${encodeURIComponent(ws)}`;
}

// Persisted to storage across the GitHub App install: plain fields only, never a token.
export interface NewProjectDraft {
  name: string;
  repo: string;
  branch: string;
  description: string;
  board: string;
  template: Record<string, string>;
  task: string;
}

export const DRAFT_KEY = "octopus.projects.new-draft";

export function decodeDraft(raw: string | null): NewProjectDraft | null {
  if (!raw) return null;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  const str = (k: string) => {
    const val = o[k];
    return typeof val === "string" ? val : "";
  };
  const template: Record<string, string> = {};
  if (o.template && typeof o.template === "object") {
    for (const [k, val] of Object.entries(o.template as Record<string, unknown>))
      if (typeof val === "string") template[k] = val;
  }
  const d: NewProjectDraft = {
    name: str("name"),
    repo: str("repo"),
    branch: str("branch"),
    description: str("description"),
    board: str("board"),
    template,
    task: validTaskID(str("task")) ? str("task") : "",
  };
  return d;
}

export function draftHasContent(d: NewProjectDraft): boolean {
  return !!(d.name || d.repo || d.description || d.board || Object.keys(d.template).length);
}

export function stashDraft(storage: Pick<Storage, "setItem"> | null, d: NewProjectDraft): void {
  try {
    storage?.setItem(DRAFT_KEY, JSON.stringify(d));
  } catch {
    /* private window or blocked storage */
  }
}

export function takeDraft(
  storage: Pick<Storage, "getItem" | "removeItem"> | null,
): NewProjectDraft | null {
  try {
    const d = decodeDraft(storage?.getItem(DRAFT_KEY) ?? null);
    storage?.removeItem(DRAFT_KEY);
    return d;
  } catch {
    return null;
  }
}

const TASK_ID = /^[A-Za-z0-9_-]{1,64}$/;

function validTaskID(id: string): boolean {
  return TASK_ID.test(id);
}

export function newProjectFromTaskURL(name: string, taskID: string): string {
  const q = new URLSearchParams({ new: "1", name });
  if (validTaskID(taskID)) q.set("task", taskID);
  return `/projects/?${q.toString()}`;
}

export function newProjectContext(search: string): { name: string; task: string } {
  const q = new URLSearchParams(search);
  const task = q.get("task") ?? "";
  return { name: (q.get("name") ?? "").trim(), task: validTaskID(task) ? task : "" };
}

export function otherDefaultBoard(
  picked: Array<{ default_board_id?: string }>,
  current: string,
): string | null {
  const named = [...new Set(picked.map((p) => p.default_board_id ?? "").filter(Boolean))];
  const board = named.length === 1 ? (named[0] ?? "") : "";
  return board && board !== current ? board : null;
}
