// What the chat looks up while a person writes: the agents that can chat and the tasks a # can
// name. Both are cached briefly; every chat on the page shares them.

import { t } from "../../i18n";
import { api } from "../api";
import type { ChatAgent } from "../chat-drawer";
import { parseMCPEntry } from "../mcp-entry";
import type { Board, Stage } from "../stages";
import { taskAnchor } from "../test-cases";
import { chatInlineTaskHTML, chatTaskChipHTML, esc, type ChatTaskRef } from "../ui";

export interface ChatAgentInfo extends ChatAgent {
  engine?: string;
  model?: string;
  effort?: string;
}

export interface TaskPick extends ChatTaskRef {
  title: string;
  status: string;
  terminal: boolean;
  boardID: string;
}

const AGENTS_TTL = 60_000;
const TASKS_TTL = 30_000;

const agentCache = new Map<string, { at: number; list: Promise<ChatAgentInfo[]> }>();

export function loadChatAgents(workspace = "", force = false): Promise<ChatAgentInfo[]> {
  const hit = agentCache.get(workspace);
  if (hit && !force && Date.now() - hit.at < AGENTS_TTL) return hit.list;
  const q = workspace ? `?workspace=${encodeURIComponent(workspace)}` : "";
  const list = api
    .get<ChatAgentInfo[] | null>("/chats/agents" + q)
    .then((l) => l ?? [])
    .catch(() => [] as ChatAgentInfo[]);
  agentCache.set(workspace, { at: Date.now(), list });
  return list;
}

let tasksAt = 0;
let tasksLoad: Promise<TaskPick[]> | null = null;
const tasksByID = new Map<string, TaskPick>();
const taskListeners = new Set<() => void>();

interface TaskRow {
  id: string;
  title: string;
  board_id?: string;
  stage: string;
  status: string;
}

export function loadTaskPicks(force = false): Promise<TaskPick[]> {
  if (tasksLoad && !force && Date.now() - tasksAt < TASKS_TTL) return tasksLoad;
  tasksAt = Date.now();
  tasksLoad = (async () => {
    // /tasks and /stages answer a list (null when empty); /boards answers {boards, default}.
    const [tasks, boards, stages] = await Promise.all([
      api.get<TaskRow[] | null>("/tasks").catch(() => null),
      api.get<{ boards: Board[] | null } | null>("/boards").catch(() => null),
      api.get<Stage[] | null>("/stages").catch(() => null),
    ]);
    const boardName = new Map((boards?.boards ?? []).map((b) => [b.id, b.label]));
    const stage = new Map((stages ?? []).map((s) => [s.id, s]));
    const picks = (tasks ?? []).map((row) => {
      const st = stage.get(row.stage);
      return {
        id: row.id,
        title: row.title || row.id,
        board: boardName.get(row.board_id ?? "") ?? "",
        boardID: row.board_id ?? "",
        stage: st?.label ?? row.stage,
        status: row.status,
        terminal: !!st?.terminal,
        href: taskAnchor(row.id, row.board_id ?? ""),
      };
    });
    tasksByID.clear();
    for (const p of picks) tasksByID.set(p.id, p);
    for (const fn of taskListeners) fn();
    return picks;
  })();
  return tasksLoad;
}

export function onTasksLoaded(fn: () => void): () => void {
  taskListeners.add(fn);
  return () => taskListeners.delete(fn);
}

export function taskPick(id: string): TaskPick | undefined {
  return tasksByID.get(id);
}

export function taskRef(id: string): ChatTaskRef {
  return tasksByID.get(id) ?? { id, href: taskAnchor(id, "") };
}

export function taskChip(id: string): string {
  return chatTaskChipHTML(taskRef(id));
}

// Tasks waiting on someone first, then the open ones, the finished last.
export function rankTasks(picks: readonly TaskPick[], query: string): TaskPick[] {
  const q = query.trim().toLowerCase().replace(/^#/, "");
  const rank = (p: TaskPick) =>
    p.status === "blocked" ? 0 : p.status === "running" ? 1 : p.terminal ? 3 : 2;
  return picks
    .filter((p) => !q || p.id.startsWith(q) || p.title.toLowerCase().includes(q))
    .sort((a, b) => rank(a) - rank(b));
}

// The agent's ids written as code become links to the task once the task list is in.
export function upgradeTaskIDs(root: ParentNode): void {
  root.querySelectorAll("code").forEach((code) => {
    if (code.closest("pre")) return;
    const id = code.textContent.trim();
    if (!/^[0-9a-f]{16}$/.test(id)) return;
    const p = tasksByID.get(id);
    if (!p) return;
    const tpl = document.createElement("template");
    tpl.innerHTML = chatInlineTaskHTML(p);
    code.replaceWith(tpl.content);
  });
}

const THULU_WRITE = ["task_create", "subtask_create", "task_update", "task_create_on_board"];

export function mcpTagsHTML(entries: readonly string[] | null | undefined): string {
  return (entries ?? [])
    .map((raw) => {
      const e = parseMCPEntry(raw.trim());
      let what = e.server;
      if (e.server === "thulu-browser") what = t("chat.mcp.browser");
      else if (e.server === "octopus-api") {
        const tools = e.tools ?? [];
        const write =
          e.scope === "write" || e.scope === "admin" || tools.some((x) => THULU_WRITE.includes(x));
        what = write
          ? t("chat.mcp.thulu_write")
          : tools.includes("task_add_comment")
            ? t("chat.mcp.thulu_comment")
            : t("chat.mcp.thulu_read");
      }
      return `<span class="cx-tag">${esc(what)}</span>`;
    })
    .join("");
}

const LAST_AGENT_KEY = "octopus.chat.lastAgent";

export function lastAgent(): string {
  try {
    return localStorage.getItem(LAST_AGENT_KEY) ?? "";
  } catch {
    return "";
  }
}

export function rememberAgent(type: string): void {
  try {
    localStorage.setItem(LAST_AGENT_KEY, type);
  } catch {
    // Private windows refuse storage; the next chat just starts with the first agent.
  }
}

export function agentReady(a: ChatAgentInfo): boolean {
  return a.prompt_ready && a.llm_ready;
}

// chatTitle is what a row and the header show. A chat keeps the title it was opened with
// ("Agent" or "Agent · task") until its first message renames it; the avatar already names the
// agent, so the prefix goes and only the task is left.
export function chatTitle(c: { title?: string; agent_type: string }): string {
  const title = (c.title ?? "").trim();
  const agent = c.agent_type.charAt(0).toUpperCase() + c.agent_type.slice(1);
  if (title.startsWith(agent + " · ")) return title.slice(agent.length + 3);
  return title || agent;
}
