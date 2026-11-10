import { t } from "../i18n";
import type { Grant, Credential as ManagedCredential } from "./credentials";

// Only providers whose credentials the daemon reads; prometheus and other are stored but never
// used.
export const CLOUD_PROVIDERS = new Set(["aws", "gcp"]);

export const NEW_CRED = "__new__";

export function parseSelectors(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const row = line.trim();
    if (!row) continue;
    const i = row.indexOf("=");
    if (i <= 0) continue;
    out[row.slice(0, i).trim()] = row.slice(i + 1).trim();
  }
  return out;
}

export function formatSelectors(m: Record<string, string> | undefined): string {
  return Object.entries(m ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export function credentialForEnv(
  creds: ManagedCredential[],
  envID: string,
): ManagedCredential | undefined {
  return creds.find((c) =>
    (c.grants ?? []).some((g) => g.subject_type === "environment" && g.subject_id === envID),
  );
}

export function credLabel(c: ManagedCredential): string {
  const bits = [c.provider];
  if (c.disabled) bits.push(t("lib.env_form.labels.switched_off"));
  else if (c.state && c.state !== "valid") bits.push(c.state);
  return `${c.name} — ${bits.join(", ")}`;
}

export function grantsWithout(c: ManagedCredential, envID: string): Grant[] {
  return (c.grants ?? []).filter(
    (g) => !(g.subject_type === "environment" && g.subject_id === envID),
  );
}

export function grantsWith(c: ManagedCredential | undefined, envID: string): Grant[] {
  return [...(c?.grants ?? []), { subject_type: "environment", subject_id: envID }];
}

// Keys are the daemon's catalog codes from internal/project/environment.go, minus
// "project.errors.";
// rename them together.
const DAEMON_ERRORS = "project.errors.";
const ERROR_FIELDS: Record<string, string> = {
  env_name_invalid: "env-name",
  env_display_name_too_long: "env-display-name",
  env_system_invalid: "env-system",
  env_provider_invalid: "env-provider",
  env_url_invalid: "env-url",
  env_selector_key_blank: "env-selectors",
};

export function envErrorField(body: unknown): string | undefined {
  const code = (body as { code?: unknown } | null | undefined)?.code;
  if (typeof code !== "string" || !code.startsWith(DAEMON_ERRORS)) return undefined;
  return Object.hasOwn(ERROR_FIELDS, code.slice(DAEMON_ERRORS.length))
    ? ERROR_FIELDS[code.slice(DAEMON_ERRORS.length)]
    : undefined;
}
