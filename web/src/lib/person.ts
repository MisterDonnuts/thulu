import { t } from "../i18n";

// Must match DevModeAuthorName in internal/auth/author.go.
export const DEV_AUTHOR = "user";

export function personName(name: string): string {
  return name === DEV_AUTHOR ? t("lib.activity.someone") : name;
}
