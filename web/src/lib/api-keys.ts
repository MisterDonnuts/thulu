// The catalog mirrors internal/auth/permissions.go.

import { locale, t } from "../i18n";

export interface PermissionChoice {
  value: string;
  label: string;
  hint: string;
}

export const PERM_GROUPS: Array<{ label: string; resource: string }> = [
  { label: t("lib.api_keys.groups.tasks"), resource: "tasks" },
  { label: t("lib.api_keys.groups.flows"), resource: "flows" },
  { label: t("lib.api_keys.groups.projects"), resource: "projects" },
  { label: t("lib.api_keys.groups.agents"), resource: "agents" },
  { label: t("lib.api_keys.groups.runs"), resource: "runs" },
  { label: t("lib.api_keys.groups.credentials"), resource: "credentials" },
  { label: t("lib.api_keys.groups.settings"), resource: "settings" },
  { label: t("lib.api_keys.groups.members"), resource: "members" },
];

export const PERM_EXTRAS: PermissionChoice[] = [
  {
    value: "keys:write",
    label: t("lib.api_keys.extras.mint_keys"),
    hint: t("lib.api_keys.extras.mint_keys_hint"),
  },
  {
    value: "internal:use",
    label: t("lib.api_keys.extras.service_routes"),
    hint: t("lib.api_keys.extras.service_routes_hint"),
  },
];

const WILDCARD = "*";

export const PRESETS: Record<string, string[]> = {
  "read-only": [
    "tasks:read",
    "flows:read",
    "projects:read",
    "agents:read",
    "runs:read",
    "settings:read",
    "members:read",
  ],
  ci: ["tasks:read", "tasks:write", "projects:read", "runs:read"],
  agent: [
    "tasks:read",
    "tasks:write",
    "flows:read",
    "projects:read",
    "runs:read",
    "runs:write",
    "internal:use",
  ],
  full: ["*"],
};

export function permissionChoices(): PermissionChoice[] {
  return [
    ...PERM_GROUPS.flatMap((g) => [
      {
        value: `${g.resource}:read`,
        label: t("lib.api_keys.labels.read", { group: g.label }),
        hint: "",
      },
      {
        value: `${g.resource}:write`,
        label: t("lib.api_keys.labels.write", { group: g.label }),
        hint: "",
      },
    ]),
    ...PERM_EXTRAS,
  ];
}

export function presetSelection(preset: string, values: string[]): string[] {
  const wanted = new Set(PRESETS[preset] ?? []);
  if (wanted.has(WILDCARD)) return [...values];
  return values.filter((v) => wanted.has(v));
}

const ROLES = ["viewer", "developer", "admin", "owner"];

// UI hint only; the daemon enforces the role ceiling.
export function roleWithin(want: string, caller: string | undefined): boolean {
  const c = ROLES.indexOf(caller ?? "");
  return c < 0 || ROLES.indexOf(want) <= c;
}

export function maxKeyDays(ttlSeconds: number | undefined): number | null {
  if (typeof ttlSeconds !== "number" || !(ttlSeconds > 0)) return null;
  return Math.floor(ttlSeconds / 86_400);
}

export function defaultKeyDays(maxDays: number | null): string {
  return maxDays && maxDays > 0 ? String(maxDays) : "";
}

export function expiryProblem(raw: string, maxDays: number | null): string | null {
  const v = raw.trim();
  if (!v) return null;
  const days = Number(v);
  if (!Number.isInteger(days) || days < 1) return t("lib.api_keys.errors.expiry_not_whole");
  if (maxDays !== null && days > maxDays) {
    return maxDays > 0
      ? t("lib.api_keys.errors.expiry_over_cap", { count: maxDays })
      : t("lib.api_keys.errors.expiry_under_day");
  }
  return null;
}

export function expiryHint(maxDays: number | null, known = true): string {
  if (maxDays === null && !known) return t("lib.api_keys.hints.cap_unknown");
  if (maxDays === null) return t("lib.api_keys.hints.no_cap");
  if (maxDays === 0) return t("lib.api_keys.hints.under_day");
  return t("lib.api_keys.hints.cap", { count: maxDays });
}

function expiryDate(expiresAt: string): string {
  try {
    return new Date(expiresAt).toLocaleDateString(locale, { dateStyle: "medium" });
  } catch {
    return expiresAt;
  }
}

export function grantedLine(
  role: string | undefined,
  permissions: string[],
  expiresAt?: string,
): string {
  const granted = permissions.length
    ? permissionSummary(permissions)
    : t("lib.api_keys.granted.nothing");
  const who = roleLabel(role ?? "developer");
  return expiresAt
    ? t("lib.api_keys.granted.expires", { role: who, granted, date: expiryDate(expiresAt) })
    : t("lib.api_keys.granted.never", { role: who, granted });
}

export function roleLabel(role: string | undefined): string {
  switch (role ?? "") {
    case "viewer":
      return t("settings.roles.viewer");
    case "developer":
      return t("settings.roles.developer");
    case "admin":
      return t("settings.roles.admin");
    case "owner":
      return t("settings.roles.owner");
    default:
      return role ?? "";
  }
}

export function permissionSummary(permissions: string[]): string {
  if (permissions.includes(WILDCARD)) return t("lib.api_keys.summary.everything");
  const have = new Set(permissions);
  const known = new Set<string>();
  const read: string[] = [];
  const write: string[] = [];
  for (const g of PERM_GROUPS) {
    known.add(`${g.resource}:read`).add(`${g.resource}:write`);
    if (have.has(`${g.resource}:read`)) read.push(g.label);
    if (have.has(`${g.resource}:write`)) write.push(g.label);
  }
  const parts: string[] = [];
  if (read.length) parts.push(t("lib.api_keys.summary.read", { groups: read.join(", ") }));
  if (write.length) parts.push(t("lib.api_keys.summary.write", { groups: write.join(", ") }));
  for (const extra of PERM_EXTRAS) {
    known.add(extra.value);
    if (have.has(extra.value)) parts.push(extra.label);
  }
  parts.push(...permissions.filter((p) => !known.has(p)));
  return parts.join(" · ") || t("lib.api_keys.summary.none");
}

export function keyExpiry(
  expiresAt: string | undefined,
  now: Date = new Date(),
): { expired: boolean; text: string } {
  if (!expiresAt) return { expired: false, text: t("lib.api_keys.labels.no_expiry") };
  const at = new Date(expiresAt);
  if (Number.isNaN(at.getTime())) return { expired: false, text: expiresAt };
  const date = expiryDate(expiresAt);
  return at.getTime() <= now.getTime()
    ? { expired: true, text: t("lib.api_keys.labels.expired", { date }) }
    : { expired: false, text: t("lib.api_keys.labels.expires", { date }) };
}

export interface KeyRowFacts {
  role?: string;
  permissions?: string[];
  expires_at?: string;
  created_by_email?: string;
  last_used_at?: string;
  revoked_at?: string;
  replaced_by?: string;
  scope_task_id?: string;
}

export type KeyState = "live" | "grace" | "replaced" | "expired" | "revoked" | "task_scoped";

export function keyState(k: KeyRowFacts, now: Date = new Date()): KeyState {
  if (k.revoked_at) return "revoked";
  if (k.scope_task_id) return "task_scoped";
  const expired = keyExpiry(k.expires_at, now).expired;
  if (k.replaced_by) return expired ? "replaced" : "grace";
  return expired ? "expired" : "live";
}

export function keyActions(state: KeyState): { rotate: boolean; revoke: boolean } {
  return { rotate: state === "live", revoke: state === "live" || state === "grace" };
}

function expiryDateTime(at: string): string {
  return new Date(at).toLocaleString(locale, { dateStyle: "medium", timeStyle: "short" });
}

function expiryText(k: KeyRowFacts, now: Date): string {
  const state = keyState(k, now);
  if (state === "grace" && k.expires_at)
    return t("lib.api_keys.labels.grace_until", { date: expiryDateTime(k.expires_at) });
  if (state === "replaced" && k.expires_at)
    return t("lib.api_keys.labels.replaced_on", { date: expiryDateTime(k.expires_at) });
  return keyExpiry(k.expires_at, now).text;
}

export function keyDetails(k: KeyRowFacts, now: Date = new Date()): string {
  const parts = [roleLabel(k.role), permissionSummary(k.permissions ?? []), expiryText(k, now)];
  if (k.created_by_email)
    parts.push(t("lib.api_keys.labels.created_by", { email: k.created_by_email }));
  parts.push(
    t("lib.api_keys.labels.last_used", {
      date: k.last_used_at ? expiryDate(k.last_used_at) : t("lib.api_keys.labels.never_used"),
    }),
  );
  return parts.filter(Boolean).join(" · ");
}

export const ROTATE_GRACE_HOURS = [0, 1, 24, 72, 168];

export function graceLabel(hours: number): string {
  if (hours === 0) return t("lib.api_keys.rotate.grace_none");
  if (hours % 24 === 0) return t("lib.api_keys.rotate.grace_days", { count: hours / 24 });
  return t("lib.api_keys.rotate.grace_hours", { count: hours });
}

export function rotatedLine(oldExpiresAt: string | undefined, now: Date = new Date()): string {
  if (!oldExpiresAt || new Date(oldExpiresAt).getTime() <= now.getTime()) {
    return t("lib.api_keys.rotate.old_revoked");
  }
  return t("lib.api_keys.rotate.old_until", {
    date: new Date(oldExpiresAt).toLocaleString(locale, {
      dateStyle: "medium",
      timeStyle: "short",
    }),
  });
}

let soonFormat: Intl.RelativeTimeFormat | undefined;

export function expiresIn(expiresAt: string | undefined, now: Date = new Date()): string {
  if (!expiresAt) return t("lib.api_keys.labels.no_expiry");
  const ms = new Date(expiresAt).getTime() - now.getTime();
  if (Number.isNaN(ms)) return expiresAt;
  if (ms <= 0) return t("lib.api_keys.run.expired");
  soonFormat ??= new Intl.RelativeTimeFormat(locale, { style: "short", numeric: "always" });
  const min = Math.max(1, Math.round(ms / 60_000));
  const when =
    min < 60
      ? soonFormat.format(min, "minute")
      : min < 48 * 60
        ? soonFormat.format(Math.round(min / 60), "hour")
        : soonFormat.format(Math.round(min / 1440), "day");
  return t("lib.api_keys.run.expires_in", { when });
}
