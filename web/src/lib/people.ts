import { t } from "../i18n";

export function memberLabel(m: { name?: string; email?: string; user_id?: string }): string {
  return [m.name, m.email, m.user_id].find((v) => !!v) ?? "";
}

export function memberInitials(m: { name?: string; email?: string; user_id?: string }): string {
  const src = memberLabel(m);
  const parts = src.split(/[\s@.]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return src.slice(0, 2).toUpperCase();
}

export function deviceLabel(ua: string): string {
  if (!ua) return t("lib.people.device.api_client");
  // Order matters: Edge and Opera both claim Chrome, and Chrome claims Safari.
  const browser = /Edg\//.test(ua)
    ? t("lib.people.browser.edge")
    : /OPR\/|Opera/.test(ua)
      ? t("lib.people.browser.opera")
      : /Chrome\/|CriOS\//.test(ua)
        ? t("lib.people.browser.chrome")
        : /Firefox\/|FxiOS\//.test(ua)
          ? t("lib.people.browser.firefox")
          : /Safari\//.test(ua)
            ? t("lib.people.browser.safari")
            : "";
  // iPhone/iPad before macOS (their UA says "like Mac OS X"); Android before Linux.
  const os = /iPhone|iPad|iPod/.test(ua)
    ? t("lib.people.os.ios")
    : /Mac OS X|Macintosh/.test(ua)
      ? t("lib.people.os.macos")
      : /Windows/.test(ua)
        ? t("lib.people.os.windows")
        : /Android/.test(ua)
          ? t("lib.people.os.android")
          : /Linux/.test(ua)
            ? t("lib.people.os.linux")
            : "";
  if (browser && os) return t("lib.people.device.browser_on_os", { browser, os });
  return browser || os || t("lib.people.device.unknown");
}
