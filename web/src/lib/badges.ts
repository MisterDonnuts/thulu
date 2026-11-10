import { t } from "../i18n";
import { fmtDay } from "./time";
import { badgeHTML, type BadgeOptions, type BadgeTone } from "./ui/badge";

export { badgeHTML, setBadge, type BadgeOptions, type BadgeTone } from "./ui/badge";

export interface StatusEntry {
  tone: BadgeTone;
  label?: string;
  icon?: string;
}

export function statusBadges(
  table: Record<string, StatusEntry>,
  look: Omit<BadgeOptions, "icon"> = {},
): (status: string) => string {
  return (status: string): string => {
    const e = Object.hasOwn(table, status) ? table[status] : undefined;
    return badgeHTML(e?.label ?? status, e?.tone ?? "neutral", { ...look, icon: e?.icon });
  };
}

export const sandboxStatusBadge = statusBadges({
  ready: { tone: "ok", label: t("lib.badges.sandbox.ready") },
  starting: { tone: "warn", label: t("lib.badges.sandbox.starting") },
  idle: { tone: "neutral", label: t("lib.badges.sandbox.idle") },
  evicted: { tone: "danger", label: t("lib.badges.sandbox.evicted") },
  failed: { tone: "danger", label: t("lib.badges.sandbox.failed") },
});

const MCP_STATUS: Record<string, BadgeTone> = {
  running: "ok",
  restarting: "warn",
  paused: "warn",
  starting: "warn",
  created: "neutral",
  exited: "danger",
  dead: "danger",
  missing: "muted",
  unknown: "muted",
  configured: "neutral",
  external: "neutral",
};

const MCP_STATUS_LABEL: Record<string, string> = {
  running: "lib.badges.mcp_status.running",
  restarting: "lib.badges.mcp_status.restarting",
  paused: "lib.badges.mcp_status.paused",
  starting: "lib.badges.mcp_status.starting",
  created: "lib.badges.mcp_status.created",
  exited: "lib.badges.mcp_status.exited",
  dead: "lib.badges.mcp_status.dead",
  missing: "lib.badges.mcp_status.missing",
  unknown: "lib.badges.mcp_status.unknown",
  configured: "lib.badges.mcp_status.configured",
  external: "lib.badges.mcp_status.external",
};

export function mcpStatusBadge(status: string): string {
  const key = MCP_STATUS_LABEL[status];
  return badgeHTML(key ? t(key) : status, MCP_STATUS[status] ?? "neutral");
}

export const runStatusBadge = statusBadges({
  success: { tone: "ok", label: t("lib.badges.run.success") },
  running: { tone: "warn", label: t("lib.badges.run.running") },
  skipped: { tone: "neutral", label: t("lib.badges.run.skipped") },
  failed: { tone: "danger", label: t("lib.badges.run.failed") },
  timeout: { tone: "danger", label: t("lib.badges.run.timeout") },
  canceled: { tone: "neutral", label: t("lib.badges.run.canceled") },
});

export const stepStatusBadge = statusBadges({
  ok: { tone: "ok", label: t("lib.badges.step.ok") },
  skipped: { tone: "neutral", label: t("lib.badges.run.skipped") },
  failed: { tone: "danger", label: t("lib.badges.run.failed") },
  running: { tone: "warn", label: t("lib.badges.run.running") },
});

const MCP_HEALTH: Record<string, BadgeTone> = {
  healthy: "ok",
  starting: "warn",
  unhealthy: "danger",
};

const MCP_HEALTH_LABEL: Record<string, string> = {
  healthy: "lib.badges.mcp_health.healthy",
  starting: "lib.badges.mcp_health.starting",
  unhealthy: "lib.badges.mcp_health.unhealthy",
};

export function mcpHealthBadge(health?: string): string {
  if (!health) return `<span class="muted">—</span>`;
  const key = MCP_HEALTH_LABEL[health];
  return badgeHTML(key ? t(key) : health, MCP_HEALTH[health] ?? "neutral");
}

export function mcpUncheckedBadge(): string {
  return badgeHTML(t("lib.badges.mcp_health.unchecked"), "muted");
}

export function mcpSourceBadge(source: "runtime" | "registered" | "env"): string {
  if (source === "registered")
    return badgeHTML(t("mcps.source.registered"), "accent", { mono: true });
  if (source === "env")
    return badgeHTML(t("mcps.source.env"), "neutral", {
      mono: true,
      edge: "dashed",
      title: t("mcps.source.env_title"),
    });
  return badgeHTML(t("mcps.source.runtime"), "neutral", { mono: true });
}

const CREDENTIAL_STATE: Record<string, BadgeTone> = {
  valid: "ok",
  expiring: "warn",
  expired: "danger",
  invalid: "danger",
  unknown: "muted",
};

export interface CredentialState {
  state: string;
  disabled?: boolean;
  expires_at?: string;
}

const CREDENTIAL_LABEL: Record<string, string> = {
  valid: "credentials.states.valid",
  expiring: "credentials.states.expiring",
  expired: "credentials.states.expired",
  invalid: "credentials.states.invalid",
  unknown: "credentials.states.unknown",
};

export function credentialStateBadge(c: CredentialState): string {
  if (c.disabled) return badgeHTML(t("credentials.states.disabled"), "muted");
  const key = CREDENTIAL_LABEL[c.state];
  let label = key ? t(key) : c.state;
  if (c.state === "expiring" && c.expires_at)
    label = t("credentials.states.expires_on", { date: fmtDay(c.expires_at) });
  if (c.state === "expired" && c.expires_at)
    label = t("credentials.states.expired_on", { date: fmtDay(c.expires_at) });
  return badgeHTML(label, CREDENTIAL_STATE[c.state] ?? "neutral");
}
