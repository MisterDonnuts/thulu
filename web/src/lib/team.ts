import { roleLabel } from "./account";
import { t } from "../i18n";

export function invitationLink(origin: string, invitationID: string): string {
  return `${origin}/login/?invite=${encodeURIComponent(invitationID)}`;
}

export interface Seats {
  used: number;
  limit: number;
}

export function seatsText(s: Seats | null): string {
  if (!s || s.limit <= 0) return "";
  return t("settings.team.members.seats", { used: s.used, limit: s.limit });
}

export function seatsFull(s: Seats | null): boolean {
  return !!s && s.limit > 0 && s.used >= s.limit;
}

export interface LicenseSeats {
  applies: boolean;
  people?: number;
  included?: number;
  licensed?: boolean;
}

export function licenseSeatsText(s: LicenseSeats | null): string {
  if (!s?.applies || !s.included || (s.people ?? 0) <= s.included) return "";
  const people = s.people ?? 0;
  const args = { included: s.included, people, over: people - s.included };
  return s.licensed
    ? t("settings.team.members.license_over", args)
    : t("settings.team.members.license_free", args);
}

export const LICENSE_NOTICE_HIDE_MS = 30 * 24 * 60 * 60 * 1000;

export function licenseNoticeHidden(hiddenAt: number, now: number): boolean {
  return hiddenAt > 0 && now - hiddenAt < LICENSE_NOTICE_HIDE_MS;
}

export interface KeyLite {
  created_by: string;
  revoked_at?: string;
}

export function liveKeysBy(keys: KeyLite[], userID: string): number {
  return keys.filter((k) => k.created_by === userID && !k.revoked_at).length;
}

export function revokeKeysOption(
  live: number | null,
): { label: string; hint: string; name: string } | undefined {
  if (live === 0) return undefined;
  return {
    label:
      live === null
        ? t("settings.team.members.remove_keys_unknown")
        : t("settings.team.members.remove_keys", { count: live }),
    hint: t("settings.team.members.remove_keys_hint"),
    name: "revoke_keys",
  };
}

export interface WaitingEntry {
  email: string;
  workspaces: string[];
  invited_at: string;
  requested?: boolean;
  name?: string;
}

export function waitingMeta(e: WaitingEntry, date: string): string {
  if (e.requested) {
    return e.name
      ? t("settings.team.beta.requested_named", { name: e.name, date })
      : t("settings.team.beta.requested", { date });
  }
  return t("settings.team.beta.waiting_meta", { workspaces: e.workspaces.join(", ") });
}

export interface LoginConfig {
  oidc: { name: string; issuer: string } | null;
  github: boolean;
  allowed_domains: string[];
  allowed_groups: string[];
  groups_claim: string;
  signup_mode: string;
  allowed_emails: string[];
  personal_workspaces: boolean;
  max_owned_workspaces: number;
  default_workspace: string;
  default_role: string;
  group_role_map: string;
  autolink: boolean;
}

export interface ConfigLine {
  label: string;
  value: string;
  env: string;
}

export function loginConfigLines(c: LoginConfig): ConfigLine[] {
  const none = t("settings.team.login.none");
  const list = (xs: string[]) => (xs.length ? xs.join(", ") : t("settings.team.login.anyone"));
  const providers = [
    c.oidc ? `${c.oidc.name} (${c.oidc.issuer})` : "",
    c.github ? "GitHub" : "",
  ].filter(Boolean);
  return [
    {
      label: t("settings.team.login.provider"),
      value: providers.join(", ") || none,
      env: "OCTOPUS_OIDC_ISSUER, OCTOPUS_GITHUB_CLIENT_ID",
    },
    {
      label: t("settings.team.login.domains"),
      value: list(c.allowed_domains),
      env: "OCTOPUS_SSO_ALLOWED_DOMAINS",
    },
    {
      label: t("settings.team.login.groups"),
      value: c.allowed_groups.length
        ? t("settings.team.login.groups_value", {
            groups: c.allowed_groups.join(", "),
            claim: c.groups_claim,
          })
        : t("settings.team.login.anyone"),
      env: "OCTOPUS_SSO_ALLOWED_GROUPS, OCTOPUS_OIDC_GROUPS_CLAIM",
    },
    {
      label: t("settings.team.login.signup"),
      value:
        c.signup_mode !== "allowlist"
          ? t("settings.team.login.signup_open")
          : c.allowed_emails.length
            ? t("settings.team.login.signup_allowlist", { count: c.allowed_emails.length })
            : t("settings.team.login.signup_list"),
      env: "OCTOPUS_SIGNUP_MODE, OCTOPUS_SSO_ALLOWED_EMAILS",
    },
    {
      label: t("settings.team.login.landing"),
      value: c.default_workspace
        ? t("settings.team.login.landing_default", {
            slug: c.default_workspace,
            role: roleLabel(c.default_role),
          })
        : c.personal_workspaces
          ? t("settings.team.login.landing_personal")
          : t("settings.team.login.landing_wait"),
      env: "OCTOPUS_SSO_DEFAULT_WORKSPACE, OCTOPUS_SSO_DEFAULT_ROLE, OCTOPUS_ALLOW_PERSONAL_WORKSPACES",
    },
    {
      label: t("settings.team.login.max_owned"),
      value:
        c.max_owned_workspaces > 0
          ? String(c.max_owned_workspaces)
          : t("settings.team.login.no_limit"),
      env: "OCTOPUS_MAX_OWNED_WORKSPACES",
    },
    {
      label: t("settings.team.login.group_roles"),
      value: c.group_role_map || none,
      env: "OCTOPUS_OIDC_GROUP_ROLE_MAP",
    },
    {
      label: t("settings.team.login.autolink"),
      value: c.autolink ? t("settings.team.login.on") : t("settings.team.login.off"),
      env: "OCTOPUS_ALLOW_SSO_AUTOLINK",
    },
  ];
}
