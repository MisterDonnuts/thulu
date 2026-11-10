import { t } from "../i18n";
import type { Stage } from "./stages";

export function autoScrollDelta(
  clientX: number,
  bounds: { left: number; right: number },
  margin = 100,
  step = 14,
): number {
  if (clientX < bounds.left + margin) return -step;
  if (clientX > bounds.right - margin) return step;
  return 0;
}

export function nextStageInFlow(
  flow: Stage[],
  currentStage: string,
  direction: -1 | 1,
): string | null {
  const idx = flow.findIndex((s) => s.id === currentStage);
  if (idx < 0) return null;
  const next = idx + direction;
  if (next < 0 || next >= flow.length) return null;
  return flow[next].id;
}

export interface BlockedPayload {
  error?: string;
  blocked_by?: Array<{ title?: string }>;
  blocking_approvals?: Array<{ key?: string; label: string; status?: string }>;
  // Sent by daemons from before approvals.
  blocking_flags?: Array<{ kind: string; label: string }>;
}

const DEP_PREVIEW = 3;

export function blockedMoveMessage(payload: BlockedPayload): string {
  const deps = payload.blocked_by ?? [];
  if (deps.length > 0) {
    const titles = deps
      .slice(0, DEP_PREVIEW)
      .map((d) => `"${d.title}"`)
      .join(", ");
    return deps.length > DEP_PREVIEW
      ? t("lib.dnd.blocked.waiting_more", { titles, count: deps.length - DEP_PREVIEW })
      : t("lib.dnd.blocked.waiting", { titles });
  }
  const approvals = payload.blocking_approvals ?? [];
  if (approvals.length > 0)
    return t("lib.dnd.blocked.approvals", { labels: approvals.map((a) => a.label).join(", ") });
  const flags = payload.blocking_flags ?? [];
  if (flags.length > 0) return flags.map((f) => `${f.kind}: ${f.label}`).join(", ");
  const err = payload.error ?? "";
  return err || t("lib.dnd.blocked.transition");
}
