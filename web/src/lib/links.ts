// Keep the trailing slash: the daemon serves the page at /tasks/ and its API at /tasks.

export function taskHref(id: string, boardId?: string): string {
  const board = boardId ? `?board=${encodeURIComponent(boardId)}` : "";
  return `/tasks/${board}#task-${encodeURIComponent(id)}`;
}

export function boardHref(id: string): string {
  return `/tasks/?board=${encodeURIComponent(id)}`;
}

export function projectHref(id: string): string {
  return `/project/?id=${encodeURIComponent(id)}`;
}

export function agentHref(name: string): string {
  return `/agents/?agent=${encodeURIComponent(name)}`;
}
