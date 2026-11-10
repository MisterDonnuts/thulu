import { t } from "../i18n";
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
