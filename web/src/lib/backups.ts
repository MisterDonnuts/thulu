import { t } from "../i18n";
import { fmtBytes } from "./health";
import { fmtDate } from "./time";
import { badgeHTML, emptyRowHTML, esc, factsHTML } from "./ui";
import { commandHTML } from "./updates";

export interface BackupStatus {
  found: boolean;
  archive?: string;
  backup_dir?: string;
  size_bytes?: number;
  finished_at?: string;
  kept?: number;
  keep?: number;
  source?: string;
  remote?: string;
  remote_state?: string;
  restore_command?: string;
  needs_database_url?: boolean;
}

// A day and a half: most installs run backups from a daily cron.
const STALE_MS = 36 * 3600_000;

export function offsiteLine(b: BackupStatus): {
  text: string;
  tone: "ok" | "warn" | "danger" | "muted";
} {
  switch (b.remote_state ?? "") {
    case "ok":
      return { text: t("lib.backups.offsite.ok", { remote: b.remote ?? "" }), tone: "ok" };
    case "failed":
      return { text: t("lib.backups.offsite.failed", { remote: b.remote ?? "" }), tone: "danger" };
    case "pending":
      return {
        text: t("lib.backups.offsite.unfinished", { remote: b.remote ?? "" }),
        tone: "warn",
      };
    default:
      return { text: t("lib.backups.offsite.none"), tone: "warn" };
  }
}

export function backupStale(b: BackupStatus, now: Date = new Date()): boolean {
  if (!b.finished_at) return false;
  return now.getTime() - new Date(b.finished_at).getTime() > STALE_MS;
}

export function backupHTML(b: BackupStatus, now: Date = new Date()): string {
  if (!b.found) {
    // Catalog text is trusted HTML (it carries <code>); never interpolate untrusted values into it.
    return emptyRowHTML(undefined, { as: "div", html: t("lib.backups.none_html") });
  }
  const off = offsiteLine(b);
  const when = b.finished_at ? fmtDate(b.finished_at) : "?";
  const stale = backupStale(b, now) ? ` ${badgeHTML(t("lib.backups.stale"), "warn")}` : "";
  const facts = factsHTML(
    [
      { label: t("lib.backups.facts.last"), valueHTML: `${esc(when)}${stale}` },
      { label: t("lib.backups.facts.archive"), value: b.archive ?? "", mono: true },
      { label: t("lib.backups.facts.size"), value: fmtBytes(b.size_bytes ?? 0) },
      {
        label: t("lib.backups.facts.kept"),
        value: t("lib.backups.kept", { count: b.kept ?? 0, keep: b.keep ?? 0 }),
      },
      { label: t("lib.backups.facts.offsite"), valueHTML: badgeHTML(off.text, off.tone) },
    ],
    { cls: "upd-facts" },
  );
  if (!b.restore_command) return facts;
  const note = b.needs_database_url
    ? `<p class="muted">${esc(t("lib.backups.restore.needs_database_url"))}</p>`
    : "";
  return `${facts}<p class="muted">${esc(t("lib.backups.restore.lede"))}</p>${commandHTML(b.restore_command, t("lib.backups.restore.command_name"))}${note}`;
}
