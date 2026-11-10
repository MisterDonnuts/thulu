import { t } from "../i18n";
import { roleWithin } from "./api-keys";

export interface SettingsSection {
  title: string;
  subtitle: string;
  needsLogin?: boolean;
  minRole?: "admin";
  operatorOnly?: boolean;
}

export const DEFAULT_SECTION = "boards";

export const SETTINGS_SECTIONS: Record<string, SettingsSection> = {
  team: {
    title: t("lib.settings_sections.team.title"),
    subtitle: t("lib.settings_sections.team.subtitle"),
    needsLogin: true,
  },
  boards: {
    title: t("lib.settings_sections.boards.title"),
    subtitle: t("lib.settings_sections.boards.subtitle"),
  },
  llm: {
    title: t("lib.settings_sections.llm.title"),
    subtitle: t("lib.settings_sections.llm.subtitle"),
  },
  webhooks: {
    title: t("lib.settings_sections.webhooks.title"),
    subtitle: t("lib.settings_sections.webhooks.subtitle"),
    minRole: "admin",
  },
  api: {
    title: t("lib.settings_sections.api.title"),
    subtitle: t("lib.settings_sections.api.subtitle"),
    minRole: "admin",
  },
  health: {
    title: t("lib.settings_sections.health.title"),
    subtitle: t("lib.settings_sections.health.subtitle"),
  },
  updates: {
    title: t("lib.settings_sections.updates.title"),
    subtitle: t("lib.settings_sections.updates.subtitle"),
    minRole: "admin",
    operatorOnly: true,
  },
  audit: {
    title: t("lib.settings_sections.audit.title"),
    subtitle: t("lib.settings_sections.audit.subtitle"),
    minRole: "admin",
  },
  language: {
    title: t("lib.settings_sections.language.title"),
    subtitle: t("lib.settings_sections.language.subtitle"),
  },
  account: {
    title: t("lib.settings_sections.account.title"),
    subtitle: t("lib.settings_sections.account.subtitle"),
    needsLogin: true,
  },
};

export function resolveSection(hash: string, loginDisabled: boolean): string {
  const name = hash.startsWith("#") ? hash.slice(1) : hash;
  // hasOwn, not a truthy lookup: `#toString` would match Object.prototype and pass for a section.
  if (!Object.hasOwn(SETTINGS_SECTIONS, name)) return DEFAULT_SECTION;
  if (SETTINGS_SECTIONS[name].needsLogin && loginDisabled) return DEFAULT_SECTION;
  return name;
}

export type SectionDenial = "role" | "operator";

// A session that omits role/operator (no-login install, older daemon) is let in; the server still
// enforces every read.
export function sectionDenial(
  name: string,
  session: { role?: string; operator?: boolean } | null | undefined,
): SectionDenial | null {
  if (!Object.hasOwn(SETTINGS_SECTIONS, name)) return null;
  const section = SETTINGS_SECTIONS[name];
  if (section.minRole && !roleWithin(section.minRole, session?.role)) return "role";
  if (section.operatorOnly && session?.operator === false) return "operator";
  return null;
}
