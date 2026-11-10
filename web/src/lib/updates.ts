import { t } from "../i18n";
import { md } from "./markdown";
import { fmtDate } from "./time";
import {
  badgeHTML,
  buttonHTML,
  calloutHTML,
  codeBlockHTML,
  esc,
  tableRowHTML,
  type BadgeTone,
  type TableColumn,
  type TableLook,
} from "./ui";

export interface UpdateAdvisory {
  id: string;
  severity: string;
  summary: string;
  url?: string;
  fixed_in: string;
}

export interface UpdateRelease {
  version: string;
  channel: string;
  published_at?: string;
  notes?: string;
  notes_url?: string;
  commit?: string;
  migrations: { added: number; contract: number };
  requires: string[];
  manual_steps: string[];
  advisories: UpdateAdvisory[];
  upgrade_from?: string;
  can_upgrade: boolean;
  rollback: { to: string; without_restore: boolean };
  blockers: string[];
  unverified?: string;
  command: string;
}

export interface UpdateStatus {
  version: string;
  dev_build: boolean;
  channel: string;
  mode: "docker" | "kubernetes";
  source: string;
  source_off: boolean;
  source_error?: string;
  channels: string[];
  interval_seconds: number;
  last_check?: string;
  last_error?: string;
  update_available: boolean;
  advisories: UpdateAdvisory[];
  available: UpdateRelease[];
  rejected: Array<{ version: string; reason: string }>;
  rollback_to?: string;
}

export interface KeptVersion {
  version: string;
  current: boolean;
  since?: string;
  until?: string;
  without_restore?: boolean;
  snapshot?: string;
  snapshot_missing?: boolean;
  command?: string;
}

export interface KeptVersions {
  mode: "docker" | "kubernetes";
  versions: KeptVersion[];
  rollback_known: boolean;
}

export interface UpdateHistoryEntry {
  at: string;
  kind: string;
  from: string;
  to: string;
  result?: string;
  mode?: string;
  operator?: string;
  detail?: string;
  image?: string;
  started_at?: string;
  finished_at?: string;
  actor: string;
  actor_type?: string;
}

export function sourceLine(st: UpdateStatus): string {
  if (st.source_error) return t("lib.updates.source.misconfigured", { error: st.source_error });
  if (st.source_off) return t("lib.updates.source.off");
  const h = st.interval_seconds / 3600;
  const every = Number.isInteger(h) ? h : Number(h.toFixed(1));
  const channels = st.channels.join(", ");
  return st.last_check
    ? t("lib.updates.source.checking_last", {
        source: st.source,
        channels,
        count: every,
        date: fmtDate(st.last_check),
      })
    : t("lib.updates.source.checking_never", { source: st.source, channels, count: every });
}

export function severityTone(sev: string): "danger" | "warn" | "muted" {
  const s = sev.toLowerCase();
  if (s === "critical" || s === "high") return "danger";
  if (s === "medium" || s === "moderate") return "warn";
  return "muted";
}

// Must wrap: in a table cell a scrolling line widens the table and pushes the Copy button out of
// view.
export function commandHTML(cmd: string, name = t("lib.updates.command_name")): string {
  return codeBlockHTML({ text: cmd, wrap: true, copy: true, label: name, cls: "upd-cmd" });
}

export function packageButtons(version: string): string {
  return [
    { format: "md", label: t("lib.updates.buttons.download_package") },
    { format: "json", label: t("lib.updates.buttons.json") },
  ]
    .map(({ format, label }) =>
      buttonHTML({
        label,
        size: "sm",
        cls: "upd-pkg",
        attrs: { "data-to": version, "data-format": format },
      }),
    )
    .join("");
}

export function advisoriesHTML(list: UpdateAdvisory[]): string {
  if (list.length === 0) return "";
  const items = list
    .map((a) => {
      const id = a.url
        ? `<a href="${esc(safeHref(a.url))}" target="_blank" rel="noopener">${esc(a.id)}</a>`
        : esc(a.id);
      const sev = badgeHTML(
        a.severity || t("lib.updates.advisories.fallback_severity"),
        severityTone(a.severity),
      );
      return `<li>${sev} ${id}: ${esc(a.summary)} <span class="muted">${esc(t("lib.updates.advisories.fixed_in", { version: a.fixed_in }))}</span></li>`;
    })
    .join("");
  return calloutHTML({
    tone: "danger",
    icon: "alert",
    title: t("lib.updates.advisories.heading"),
    bodyHTML: `<ul class="upd-list">${items}</ul>`,
    attrs: { role: "alert" },
  });
}

function safeHref(u: string): string {
  try {
    const p = new URL(u);
    return p.protocol === "https:" || p.protocol === "http:" ? p.href : "#";
  } catch {
    return "#";
  }
}

export function releaseHTML(r: UpdateRelease, current: string): string {
  const head = `<div class="upd-rel-head"><strong class="mono">${esc(r.version)}</strong> ${badgeHTML(r.channel, "muted")}${
    r.published_at
      ? ` <span class="muted">${esc(t("lib.updates.release.published", { date: fmtDate(r.published_at) }))}</span>`
      : ""
  }</div>`;
  const notes = r.notes?.trim() ? `<div class="upd-notes">${md(r.notes)}</div>` : "";

  if (r.unverified) {
    const warn = calloutHTML({
      tone: "warn",
      icon: "warn",
      body: r.unverified,
      attrs: { role: "note" },
    });
    return `<div class="upd-rel">${head}${warn}${notes}${commandHTML(r.command)}</div>`;
  }

  const facts: string[] = [];
  const mig = r.migrations.added + r.migrations.contract;
  facts.push(
    esc(
      mig === 0
        ? t("lib.updates.release.no_migrations")
        : r.migrations.contract
          ? t("lib.updates.release.migrations_contract", {
              added: r.migrations.added,
              contract: r.migrations.contract,
            })
          : t("lib.updates.release.migrations", { added: r.migrations.added }),
    ),
  );
  if (r.upgrade_from)
    facts.push(esc(t("lib.updates.release.upgrades_from", { version: r.upgrade_from })));
  const back = r.rollback.to || current;
  facts.push(
    esc(
      r.rollback.without_restore
        ? t("lib.updates.release.rollback_no_restore", { version: back })
        : t("lib.updates.release.rollback_snapshot", { version: back }),
    ),
  );
  if (r.requires.length)
    facts.push(
      t("lib.updates.release.requires_html", {
        vars: r.requires.map((k) => `<code>${esc(k)}</code>`).join(", "),
      }),
    );

  const steps = r.manual_steps.length
    ? `<p><strong>${esc(t("lib.updates.release.manual_steps"))}</strong></p><ul class="upd-list">${r.manual_steps.map((s) => `<li>${esc(s)}</li>`).join("")}</ul>`
    : "";
  const blockers = r.blockers.length
    ? calloutHTML({
        tone: "danger",
        icon: "alert",
        body: t("lib.updates.release.blockers", { blockers: r.blockers.join("; ") }),
        attrs: { role: "note" },
      })
    : "";
  const link = r.notes_url
    ? ` <a href="${esc(safeHref(r.notes_url))}" target="_blank" rel="noopener">${esc(t("lib.updates.release.full_notes"))}</a>`
    : "";

  return `<div class="upd-rel">${head}<p class="muted">${facts.join(" ")}${link}</p>${blockers}${steps}${notes}${commandHTML(r.command)}<div class="upd-rel-actions">${packageButtons(r.version)}</div></div>`;
}

function versionNoteHTML(row: KeptVersion): string {
  if (row.without_restore)
    return `<div class="muted">${esc(t("lib.updates.versions.no_restore"))}</div>`;
  const loss = row.until
    ? t("lib.updates.versions.restore_loses", { date: fmtDate(row.until) })
    : t("lib.updates.versions.restore_loses_undated");
  const missing = row.snapshot_missing
    ? `<div class="muted">${esc(t("lib.updates.versions.no_snapshot"))}</div>`
    : "";
  return `<div class="muted">${esc(loss)}</div>${missing}`;
}

export const UPDATES_TABLE: TableLook = { top: true, stack: true };

// settings.astro styles this class to take a stacked card's whole width.
const WIDE_CELL = "upd-wide";

export function versionsColumns(): TableColumn[] {
  return [
    { label: t("settings.updates.columns.version"), width: "shrink", mono: true },
    { label: t("settings.updates.columns.ran"), width: "shrink" },
    { label: t("settings.updates.columns.command"), width: "fill", cls: WIDE_CELL },
  ];
}

export function versionsRowsHTML(v: KeptVersions): string {
  const cols = versionsColumns();
  return v.versions
    .map((row) => {
      const span = row.since
        ? `${esc(fmtDate(row.since))}${row.until ? ` → ${esc(fmtDate(row.until))}` : ""}`
        : "";
      let action: string;
      if (row.current) action = badgeHTML(t("lib.updates.versions.running"), "ok");
      else if (row.command) {
        action = commandHTML(row.command) + versionNoteHTML(row);
      } else action = "";
      return tableRowHTML(
        cols,
        { cells: [row.version, { html: span }, { html: action }] },
        UPDATES_TABLE,
      );
    })
    .join("");
}

const HISTORY_KIND: Record<string, string> = {
  "system.version_changed": t("lib.updates.history.version_changed"),
  "system.update_reported": t("lib.updates.history.update_reported"),
};

// Must match the results internal/updater records.
const RESULT_LABEL: Record<string, string> = {
  ok: t("lib.updates.history.result_ok"),
  failed: t("lib.updates.history.result_failed"),
  rolled_back: t("lib.updates.history.result_rolled_back"),
};

function versionSpan(e: UpdateHistoryEntry): string {
  if (e.from) return `${esc(e.from)} → ${esc(e.to)}`;
  if (e.kind === "system.version_changed")
    return `<span class="muted">${esc(t("lib.updates.history.first_start"))}</span> → ${esc(e.to)}`;
  return `— → ${esc(e.to)}`;
}

export function historyColumns(): TableColumn[] {
  return [
    { label: t("settings.updates.columns.time"), width: "shrink" },
    { label: t("settings.updates.columns.event"), width: "shrink" },
    { label: t("settings.updates.columns.versions"), width: "shrink", mono: true },
    { label: t("settings.updates.columns.by"), width: "fill", cls: WIDE_CELL },
  ];
}

export function historyRowsHTML(list: UpdateHistoryEntry[]): string {
  const cols = historyColumns();
  return list
    .map((e) => {
      const what = HISTORY_KIND[e.kind] ?? e.kind;
      const result = e.result
        ? ` ${badgeHTML(RESULT_LABEL[e.result] ?? e.result.replace("_", " "), e.result === "ok" ? "ok" : "danger")}`
        : "";
      const who = e.operator
        ? `${esc(e.operator)} <span class="muted">(${esc(e.actor)})</span>`
        : esc(e.actor);
      const failed = !!e.result && e.result !== "ok";
      const detail = e.detail
        ? codeBlockHTML({
            text: e.detail,
            multiline: true,
            maxHeight: "sm",
            tone: failed ? "danger" : "neutral",
            cls: "upd-detail",
          })
        : "";
      const image = e.image ? `<div class="muted mono">${esc(e.image)}</div>` : "";
      return tableRowHTML(
        cols,
        {
          cells: [
            fmtDate(e.at),
            { html: `${esc(what)}${result}` },
            { html: versionSpan(e) },
            { html: `${who}${image}${detail}` },
          ],
          tone: failed ? "danger" : undefined,
        },
        UPDATES_TABLE,
      );
    })
    .join("");
}

export function packageFilename(from: string, to: string, format: "md" | "json"): string {
  const clean = (s: string) => s.replace(/[^0-9A-Za-z.+-]/g, "_") || "unknown";
  return `octopus-update-${clean(from)}-to-${clean(to)}.${format}`;
}

export function sourceShortName(source: string): string {
  if (source.startsWith("github ")) return "GitHub";
  try {
    return new URL(source).host || source;
  } catch {
    return source;
  }
}

export function checkErrorSummary(error: string, source: string): string {
  const name = sourceShortName(source);
  if (
    /no such host|dial tcp|connection refused|network is unreachable|i\/o timeout|tls handshake timeout|deadline exceeded|connection reset/i.test(
      error,
    )
  ) {
    return t("lib.updates.check_error.offline", { source: name });
  }
  if (/rate limit|\b(403|429)\b/i.test(error)) {
    return t("lib.updates.check_error.rate_limited", { source: name });
  }
  if (/\b404\b|not found/i.test(error))
    return t("lib.updates.check_error.not_found", { source: name });
  return t("lib.updates.check_error.other", { source: name });
}

export function updateBadge(st: UpdateStatus): { label: string; tone: BadgeTone } {
  if (st.source_error) return { label: t("settings.updates.status.misconfigured"), tone: "danger" };
  if (st.update_available)
    return { label: t("settings.updates.status.update_available"), tone: "warn" };
  if (st.source_off) return { label: t("settings.updates.status.lookups_off"), tone: "muted" };
  if (st.last_error) return { label: t("lib.updates.check_error.badge"), tone: "warn" };
  if (st.dev_build) return { label: t("settings.updates.status.dev_build"), tone: "muted" };
  return { label: t("settings.updates.status.up_to_date"), tone: "ok" };
}

export function lastErrorHTML(st: UpdateStatus): string {
  if (!st.last_error) return "";
  return calloutHTML({
    tone: "danger",
    icon: "alert",
    title: t("lib.updates.check_error.title"),
    body: checkErrorSummary(st.last_error, st.source),
    detail: { summary: t("lib.updates.check_error.detail"), text: st.last_error },
    attrs: { role: "note" },
  });
}

export function noReleasesLine(st: UpdateStatus): string {
  if (st.source_off || st.source_error) return t("settings.updates.available.lookups_off");
  if (st.last_error) return t("lib.updates.check_error.unknown_newest");
  if (st.last_check) return t("settings.updates.available.newest");
  return t("settings.updates.available.no_check");
}

const REPORT_WINDOW_MS = 6 * 3600_000;

// `thulu update` posts its result after the restart; a version change with no report nearby means
// the CLI could not post (no OCTOPUS_API_KEY) or something else changed it.
// The first boot logs an empty `from` and is not an update.
export function unreportedChanges(list: UpdateHistoryEntry[]): UpdateHistoryEntry[] {
  const reports = list.filter((e) => e.kind === "system.update_reported");
  return list.filter((e) => {
    if (e.kind !== "system.version_changed" || !e.from) return false;
    const at = new Date(e.at).getTime();
    return !reports.some(
      (r) => r.to === e.to && Math.abs(new Date(r.at).getTime() - at) <= REPORT_WINDOW_MS,
    );
  });
}

export function unreportedNoteHTML(list: UpdateHistoryEntry[]): string {
  const missing = unreportedChanges(list);
  if (missing.length === 0) return "";
  // note_html is inserted as HTML: the versions must be escaped here.
  return calloutHTML({
    tone: "warn",
    icon: "warn",
    bodyHTML: t("lib.updates.unreported.note_html", {
      versions: missing.map((e) => esc(e.to)).join(", "),
      env: "OCTOPUS_API_KEY",
    }),
    attrs: { role: "note" },
  });
}
