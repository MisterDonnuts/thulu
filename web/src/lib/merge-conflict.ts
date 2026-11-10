import { t } from "../i18n";

function fileList(files: string[]): string {
  return files.map((f) => `- \`${f.replace(/`/g, "")}\``).join("\n");
}

export function redoBody(body: string, target: string, files: string[]): string {
  const note = `${t("lib.merge_conflict.redo_note", { target, count: files.length })}\n\n${fileList(files)}`;
  return body.trim() ? `${body.trim()}\n\n---\n\n${note}` : note;
}

export function conflictComment(target: string, files: string[]): string {
  return `${t("lib.merge_conflict.comment", { target, count: files.length })}\n\n${fileList(files)}`;
}

// Only on Kubernetes can the agent run git on its branch (Docker mounts .git read-only); a daemon
// that omits agent_can_resolve is treated as unable.
export function agentCanResolve(body: unknown): boolean {
  return (
    typeof body === "object" &&
    body !== null &&
    (body as { agent_can_resolve?: unknown }).agent_can_resolve === true
  );
}
