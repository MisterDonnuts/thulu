import { t } from "../i18n";
import { relTime } from "./activity";
import { badgeHTML, emptyRowHTML, esc, tableRowHTML, type BadgeTone, type TableColumn } from "./ui";

// The deploy watches of a project, on its settings tab. The release agent registers them (or an admin
// through the API); the daemon owns the automation each one generates, so the page only lists them
// and deletes them.

export interface DeployWatch {
  id: string;
  project_id: string;
  source: string;
  target: string;
  interval_seconds?: number;
  automation_id?: string;
  created_by?: string;
  last_state?: string;
  last_version?: string;
  last_error?: string;
  last_checked_at?: string;
  last_changed_at?: string;
}

export function deployWatchColumns(): TableColumn[] {
  return [
    { label: t("project.deploy_watches.columns.source"), width: "shrink" },
    { label: t("project.deploy_watches.columns.target"), width: "fill", mono: true },
    { label: t("project.deploy_watches.columns.interval"), width: "shrink" },
    { label: t("project.deploy_watches.columns.last_seen"), width: "shrink" },
    { label: t("project.deploy_watches.columns.actions"), width: "shrink", hideLabel: true },
  ];
}

export function sourceLabel(source: string): string {
  switch (source) {
    case "github_workflow":
      return t("project.deploy_watches.sources.github_workflow");
    case "github_deployment":
      return t("project.deploy_watches.sources.github_deployment");
    case "cloud":
      return t("project.deploy_watches.sources.cloud");
    case "http":
      return t("project.deploy_watches.sources.http");
  }
  return source;
}

// GitHub sources have no interval: their webhook fires them.
export function intervalLabel(seconds: number | undefined): string {
  if (!seconds) return t("project.deploy_watches.on_event");
  if (seconds % 3600 === 0)
    return t("project.deploy_watches.every_hours", { count: seconds / 3600 });
  return t("project.deploy_watches.every_minutes", { count: Math.round(seconds / 60) });
}

interface Seen {
  label: string;
  tone: BadgeTone;
  sub: string;
}

export function lastSeen(w: DeployWatch, now = Date.now()): Seen {
  // Thulu never reads a GitHub source: its webhook opens the release task.
  if (w.source === "github_workflow" || w.source === "github_deployment") {
    return { label: t("project.deploy_watches.state.webhook"), tone: "muted", sub: "" };
  }
  const when = w.last_checked_at ? relTime(w.last_checked_at, now) : "";
  if (w.last_error) {
    return { label: t("project.deploy_watches.state.error"), tone: "danger", sub: w.last_error };
  }
  if (w.last_state === "success") {
    return {
      label: t("project.deploy_watches.state.success"),
      tone: "ok",
      sub: [w.last_version, when].filter(Boolean).join(" · "),
    };
  }
  if (w.last_state === "failure") {
    return {
      label: t("project.deploy_watches.state.failure"),
      tone: "warn",
      sub: [w.last_version, when].filter(Boolean).join(" · "),
    };
  }
  return { label: t("project.deploy_watches.state.waiting"), tone: "muted", sub: "" };
}

export function deployWatchRowsHTML(watches: DeployWatch[], now = Date.now()): string {
  const cols = deployWatchColumns();
  if (!watches.length) {
    return emptyRowHTML(t("project.deploy_watches.empty"), { cols: cols.length });
  }
  return watches
    .map((w) => {
      const seen = lastSeen(w, now);
      return tableRowHTML(
        cols,
        {
          id: w.id,
          cells: [
            sourceLabel(w.source),
            {
              text: w.target,
              sub: w.created_by
                ? t("project.deploy_watches.created_by", { who: w.created_by })
                : undefined,
            },
            intervalLabel(w.interval_seconds),
            {
              html: badgeHTML(seen.label, seen.tone),
              subHTML: seen.sub ? esc(seen.sub) : undefined,
            },
            {
              actions: [
                {
                  label: t("project.deploy_watches.delete"),
                  icon: "trash",
                  iconOnly: true,
                  variant: "ghost",
                  attrs: { "data-dw-del": w.id },
                },
              ],
            },
          ],
        },
        { compact: true, stack: true },
      );
    })
    .join("");
}
