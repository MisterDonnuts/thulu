import { locale, t } from "../i18n";
import { fmtWhen } from "./metrics-format";
import {
  badgeHTML,
  esc,
  tableHTML,
  type ButtonOptions,
  type TableColumn,
  type TableRow,
} from "./ui";

export interface IngestToken {
  id: string;
  name: string;
  prefix: string;
  status: string;
  // Absent or empty means any address.
  allow_cidrs?: string[];
  created_at: string;
  last_seen_at?: string;
}

export interface IngestSource {
  token_id: string;
  ip: string;
  accepted: number;
  rejected: number;
  last_reject?: string;
  last_seen: string;
}

export function labelReject(reason?: string): string {
  switch (reason ?? "") {
    case "ip_not_allowed":
      return t("lib.ingest.reject.ip_not_allowed");
    case "revoked":
      return t("lib.ingest.reject.revoked");
    case "bad_token":
      return t("lib.ingest.reject.bad_token");
    default:
      return t("lib.ingest.reject.refused");
  }
}

// The setting wins: the browser's origin is often a LAN or tunnel address the app's cloud cannot
// reach.
export function ingestBase(
  setting: string | null | undefined,
  origin: string,
): { base: string; fromSetting: boolean } {
  const v = (setting ?? "").trim().replace(/\/+$/, "");
  return v ? { base: v, fromSetting: true } : { base: origin, fromSetting: false };
}

export function collectorSnippet(raw: string, base: string): string {
  return (
    `OTEL_EXPORTER_OTLP_ENDPOINT=${base}/ingest\n` +
    `OTEL_EXPORTER_OTLP_HEADERS=x-octopus-token=${raw}\n` +
    `OTEL_SERVICE_NAME=<your service>`
  );
}

function tokenColumns(): TableColumn[] {
  return [
    { label: t("lib.ingest.columns.token"), width: "fill" },
    { label: t("lib.ingest.columns.allowed_from") },
    { label: t("lib.ingest.columns.status"), width: "shrink" },
    { label: t("lib.ingest.columns.last_used"), width: "shrink" },
    { label: t("lib.ingest.columns.actions"), hideLabel: true, width: "shrink" },
  ];
}

export function ingestTokensHTML(tokens: IngestToken[]): string {
  const rows = tokens.map((tok): TableRow => {
    const revoked = tok.status !== "active";
    const cidrs = tok.allow_cidrs?.length
      ? tok.allow_cidrs.map((c) => `<code>${esc(c)}</code>`).join(" ")
      : `<span class="muted">${esc(t("lib.ingest.labels.any_address"))}</span>`;
    const actions: ButtonOptions[] = revoked
      ? []
      : [
          {
            iconOnly: true,
            icon: "settings",
            label: t("lib.ingest.buttons.allowed"),
            title: t("lib.ingest.buttons.allowed"),
            attrs: { "data-ing-cidr": tok.id },
          },
          {
            iconOnly: true,
            icon: "trash",
            variant: "danger",
            label: t("lib.ingest.buttons.revoke_aria"),
            title: t("lib.ingest.buttons.revoke"),
            attrs: { "data-ing-revoke": tok.id },
          },
        ];
    return {
      dim: revoked,
      cells: [
        { html: `${esc(tok.name)}<br /><code class="muted">${esc(tok.prefix)}…</code>` },
        { html: cidrs },
        {
          html: revoked
            ? badgeHTML(t("lib.ingest.status.revoked"))
            : badgeHTML(t("lib.ingest.status.active"), "ok"),
        },
        fmtWhen(tok.last_seen_at),
        { actions },
      ],
    };
  });
  return tableHTML(tokenColumns(), rows, { stack: true, empty: t("lib.ingest.empty.tokens") });
}

function sourceColumns(): TableColumn[] {
  return [
    { label: t("lib.ingest.columns.address"), mono: true },
    { label: t("lib.ingest.columns.token") },
    { label: t("lib.ingest.columns.accepted"), num: true },
    { label: t("lib.ingest.columns.refused"), num: true },
    { label: t("lib.ingest.columns.last_seen"), width: "shrink" },
  ];
}

export function ingestSourcesHTML(sources: IngestSource[], tokens: IngestToken[]): string {
  const names = new Map(tokens.map((tok) => [tok.id, tok.name]));
  const rows = sources.map((s): TableRow => {
    const who = names.get(s.token_id) ?? "";
    return {
      tone: s.rejected ? "danger" : undefined,
      cells: [
        s.ip,
        who === "" ? "—" : who,
        s.accepted.toLocaleString(locale),
        s.rejected
          ? { text: s.rejected.toLocaleString(locale), sub: labelReject(s.last_reject) }
          : "0",
        fmtWhen(s.last_seen),
      ],
    };
  });
  return tableHTML(sourceColumns(), rows, { stack: true, empty: t("lib.ingest.empty.sources") });
}

export function parseCIDRs(answer: string): string[] {
  return answer
    .split(",")
    .map((c) => c.trim())
    .filter(Boolean);
}
