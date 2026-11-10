import { t } from "../i18n";

export const RUN_LIMIT_KEY = "agent.max_run_minutes";
export const RUN_LIMIT_PATH = `/workspace-settings/${RUN_LIMIT_KEY}`;

export interface RunLimit {
  value: number | null;
  default: number;
  ceiling: number;
  effective: number;
}

export type WorkspaceSettings = Partial<Record<string, RunLimit>>;

export function minutesLabel(n: number): string {
  return n > 0 ? t("settings.run_limit.minutes", { count: n }) : t("settings.run_limit.no_limit");
}

export function runLimitHelp(l: RunLimit): string {
  return t("settings.run_limit.help", {
    default: minutesLabel(l.default),
    ceiling: minutesLabel(l.ceiling),
  });
}

export function runLimitNow(l: RunLimit): string {
  if (l.value === null) {
    return t("settings.run_limit.now_default", { limit: minutesLabel(l.effective) });
  }
  if (l.value > l.ceiling) {
    return t("settings.run_limit.now_capped", {
      value: minutesLabel(l.value),
      limit: minutesLabel(l.effective),
    });
  }
  return t("settings.run_limit.now_own", { limit: minutesLabel(l.effective) });
}

export function parseRunLimit(
  raw: string,
  ceiling: number,
): { minutes: number } | { error: string } {
  const s = raw.trim();
  const n = Number(s);
  if (s === "" || !Number.isInteger(n) || n < 1 || n > ceiling) {
    return { error: t("settings.run_limit.out_of_range", { max: ceiling }) };
  }
  return { minutes: n };
}
