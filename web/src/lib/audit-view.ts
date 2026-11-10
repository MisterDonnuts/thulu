import { t } from "../i18n";
import { fmtDate } from "./time";
import { badgeHTML, esc, tableRowHTML, type TableColumn, type TableLook } from "./ui";

export interface AuditRow {
  kind: string;
  actor_id?: string;
  actor_type?: string;
  actor_label?: string;
  actor_email?: string;
  resource_id?: string;
  resource_type?: string;
  summary?: string;
  summary_text?: string;
}

export function auditActor(e: AuditRow): string {
  const label = firstSet(e.actor_label, e.actor_email);
  switch (e.actor_type ?? "") {
    case "user":
      return firstSet(e.actor_email, label, e.actor_id) ?? "—";
    case "api_key":
      return t("lib.audit_view.actor.api_key", { name: firstSet(label, e.actor_id) ?? "?" });
    case "agent":
      return t("lib.audit_view.actor.agent", { run: firstSet(label, e.actor_id) ?? "?" });
    case "system":
      return t("lib.audit_view.actor.system");
    case "dev_mode":
      return t("lib.audit_view.actor.dev_mode");
    default:
      if (e.actor_id === "system") return t("lib.audit_view.actor.system");
      return firstSet(label, e.actor_id) ?? "—";
  }
}

export function auditResourceHref(e: AuditRow): string | null {
  const id = e.resource_id;
  if (!id) return null;
  const type = firstSet(e.resource_type) ?? e.kind.split(".")[0];
  const q = encodeURIComponent(id);
  switch (type) {
    case "task":
      return `/tasks/#task-${q}`;
    case "project":
      return `/project/?id=${q}`;
    case "board":
      return `/tasks/?board=${q}`;
    case "agent":
      return `/agents/?agent=${q}`;
    case "api_key":
      return "/settings/#api";
    case "user":
    case "member":
    case "invitation":
    case "workspace":
      return "/settings/#team";
    case "stage":
      return "/settings/#boards";
    case "webhook":
      return "/settings/#webhooks";
    case "automation":
      return "/automations/";
    case "credential":
      return "/credentials/";
    default:
      return null;
  }
}

export function auditSummary(e: AuditRow): string {
  return firstSet(e.summary_text, e.summary) ?? "—";
}

export const AUDIT_TABLE: TableLook = { top: true, stack: true };

export function auditColumns(): TableColumn[] {
  return [
    { label: t("settings.audit.columns.time"), width: "shrink", mono: true, cls: "audit-time" },
    { label: t("settings.audit.columns.event"), width: "shrink" },
    { label: t("settings.audit.columns.actor"), cls: "audit-actor" },
    { label: t("settings.audit.columns.resource"), cls: "audit-resource" },
    { label: t("settings.audit.columns.summary"), width: "fill", cls: "audit-summary" },
  ];
}

export function auditRowHTML(e: AuditRow & { created_at: string }): string {
  const href = auditResourceHref(e);
  return tableRowHTML(
    auditColumns(),
    {
      cells: [
        fmtDate(e.created_at),
        { html: badgeHTML(e.kind, "neutral", { mono: true }) },
        { text: auditActor(e), attrs: { title: firstSet(e.actor_id) } },
        href
          ? { html: `<a href="${esc(href)}">${esc(e.resource_id ?? "")}</a>` }
          : (firstSet(e.resource_id) ?? "—"),
        auditSummary(e),
      ],
    },
    AUDIT_TABLE,
  );
}

// Older rows store "" for unknown, so "" must count as unset.
function firstSet(...values: Array<string | undefined>): string | undefined {
  return values.find((v) => v !== undefined && v !== "");
}

export interface ChainReport {
  checked: number;
  legacy: number;
  ok: boolean;
  broken_at?: string;
  reason?: string;
  cause?: string;
  broken_created_at?: string;
  head?: string;
}

export function chainLine(r: ChainReport): string {
  const head = r.head ? r.head.slice(0, 12) : "—";
  if (!r.ok) {
    const args = {
      id: r.broken_at ?? "?",
      reason: firstSet(r.cause, r.reason) ?? "",
      count: r.checked,
    };
    return r.broken_created_at
      ? t("lib.audit_view.chain.broken_dated", { ...args, date: fmtDate(r.broken_created_at) })
      : t("lib.audit_view.chain.broken", args);
  }
  const line = t("lib.audit_view.chain.ok", { count: r.checked, head });
  return r.legacy > 0 ? `${line} ${t("lib.audit_view.chain.legacy", { count: r.legacy })}` : line;
}
