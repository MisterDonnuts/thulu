import { t } from "../i18n";
import { api } from "./api";
import {
  attrsHTML,
  esc,
  fieldControlAttrs,
  fieldHTML,
  type TableColumn,
  type TableLook,
} from "./ui";

export interface FieldSpec {
  name: string;
  label: string;
  secret?: boolean;
  optional?: boolean;
  multiline?: boolean;
  placeholder?: string;
  help?: string;
}

export interface KindSpec {
  id: string;
  label: string;
  help?: string;
  fields: FieldSpec[];
}

export interface ProviderSpec {
  id: string;
  label: string;
  help?: string;
  kinds: KindSpec[];
  connect?: boolean;
}

export interface Grant {
  subject_type: "project" | "environment" | "workspace";
  subject_id?: string;
}

export interface Credential {
  id: string;
  name: string;
  provider: string;
  kind: string;
  hint?: string;
  identity?: string;
  state: string;
  capabilities?: string[];
  introspect_error?: string;
  validated_at?: string;
  expires_at?: string;
  disabled?: boolean;
  grants?: Grant[];
  tools?: ToolRow[];
}

export interface ToolRow {
  server: string;
  tool: string;
  summary?: string;
  requires?: string[];
  decision: Decision;
}

export interface Decision {
  allowed: boolean;
  reason: string;
  detail?: string;
  missing?: string[];
}

// Shared by the <Table> in credentials.astro and the rows its script writes with tableRowHTML();
// cls values are page hooks (phones hide cred-check).

export const CRED_TABLE: TableLook = { stack: true };

export function credentialColumns(): TableColumn[] {
  return [
    { label: t("credentials.columns.name"), width: "shrink" },
    { label: t("credentials.columns.provider"), width: "shrink" },
    { label: t("credentials.columns.identity") },
    { label: t("credentials.columns.state"), width: "shrink" },
    { label: t("credentials.columns.access"), width: "fill" },
    {
      label: t("credentials.columns.actions"),
      width: "shrink",
      hideLabel: true,
      cls: "cred-check",
    },
  ];
}

// Only when and verdict shrink: a filling reason squeezed the tool and key ids until they broke at
// hyphens, and a shrunk tool cell overflows the phone's stacked card.
export function decisionColumns(): TableColumn[] {
  return [
    { label: t("credentials.columns.when"), width: "shrink", cls: "d-when" },
    { label: t("credentials.columns.verdict"), width: "shrink" },
    { label: t("credentials.columns.tool"), mono: true },
    { label: t("credentials.columns.credential") },
    { label: t("credentials.columns.task") },
    { label: t("credentials.columns.reason"), cls: "dec-reason" },
  ];
}

export function installColumns(): TableColumn[] {
  return [
    { label: t("credentials.columns.organization"), width: "fill" },
    { label: t("credentials.columns.repositories"), width: "shrink" },
    { label: t("credentials.columns.state"), width: "shrink" },
    { label: t("credentials.columns.actions"), width: "shrink", hideLabel: true },
  ];
}

export async function fetchProviders(): Promise<ProviderSpec[]> {
  try {
    const resp = await api.get<{ providers: ProviderSpec[] | null }>("/credential-providers");
    return resp.providers ?? [];
  } catch {
    return [];
  }
}

export async function listCredentials(): Promise<Credential[]> {
  const resp = await api.get<{ credentials: Credential[] | null }>("/credentials");
  return resp.credentials ?? [];
}

export function providerOptions(providers: ProviderSpec[]): string {
  return providers.map((p) => `<option value="${esc(p.id)}">${esc(p.label)}</option>`).join("");
}

export function kindOptions(provider: ProviderSpec | undefined): string {
  return (provider?.kinds ?? [])
    .map((k) => `<option value="${esc(k.id)}">${esc(k.label)}</option>`)
    .join("");
}

export function helpHTML(text: string): string {
  return text
    .split(/(`[^`\n]+`)/)
    .map((part) =>
      part.length > 2 && part.startsWith("`") && part.endsWith("`")
        ? `<code class="code-inline">${esc(part.slice(1, -1))}</code>`
        : esc(part),
    )
    .join("");
}

export function fieldInputs(kind: KindSpec | undefined, idPrefix: string): string {
  return (kind?.fields ?? [])
    .map((f) => {
      const id = `${idPrefix}${f.name}`;
      const attrs = attrsHTML({
        id,
        placeholder: f.placeholder ?? "",
        spellcheck: "false",
        ...fieldControlAttrs({ for: id, help: Boolean(f.help) }),
      });
      const control = f.multiline
        ? `<textarea class="mono" rows="4"${attrs}></textarea>`
        : `<input type="${f.secret ? "password" : "text"}" autocomplete="off"${attrs}>`;
      return fieldHTML({
        label: f.label,
        for: id,
        optional: f.optional,
        helpHTML: f.help ? helpHTML(f.help) : undefined,
        control,
      });
    })
    .join("");
}

export function readPayload(kind: KindSpec | undefined, idPrefix: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of kind?.fields ?? []) {
    const el = document.getElementById(`${idPrefix}${f.name}`) as
      HTMLInputElement | HTMLTextAreaElement | null;
    const v = (el?.value ?? "").trim();
    if (v) out[f.name] = v;
  }
  return out;
}

// Call when closing a form: a password left in a hidden modal stays in the DOM for the rest of the
// session.
export function clearFields(host: HTMLElement): void {
  host.innerHTML = "";
}

export interface CreateInput {
  name: string;
  provider: string;
  kind: string;
  payload: Record<string, string>;
  grants: Grant[];
}

export function createCredential(input: CreateInput): Promise<Credential> {
  return api.post<Credential>("/credentials", input);
}

// Code the daemon stores in introspect_error when the provider was unreachable; must match the
// daemon's value.
export const PROVIDER_UNREACHABLE = "provider_unreachable";

export function checkUnanswered(c: Credential): boolean {
  if (!c.introspect_error) return false;
  if (c.introspect_error === PROVIDER_UNREACHABLE) return true;
  // Older daemons stored their own sentence instead of the code; with no valid/invalid verdict it
  // means the check did not run.
  return c.state !== "valid" && c.state !== "invalid";
}

export function introspectNote(c: Credential, providerLabel = c.provider): string {
  if (!c.introspect_error) return "";
  if (c.introspect_error === OAUTH_RECONNECT_REQUIRED)
    return t("lib.credentials.check.oauth_reconnect_required");
  if (c.introspect_error === OAUTH_DISCONNECTED)
    return t("lib.credentials.check.oauth_disconnected");
  return c.introspect_error === PROVIDER_UNREACHABLE
    ? t("lib.credentials.check.unreachable_note", { provider: providerLabel })
    : c.introspect_error;
}

export function checkTone(c: Credential): "ok" | "err" {
  return c.state === "invalid" || checkUnanswered(c) ? "err" : "ok";
}

export function checkSummary(c: Credential, providerLabel = c.provider): string {
  if (checkUnanswered(c)) {
    return c.introspect_error === PROVIDER_UNREACHABLE
      ? t("lib.credentials.check.unanswered", { name: c.name, provider: providerLabel })
      : t("lib.credentials.check.unanswered_because", { name: c.name, error: c.introspect_error });
  }
  if (c.state === "invalid") {
    return c.introspect_error
      ? t("lib.credentials.check.rejected_because", {
          name: c.name,
          provider: providerLabel,
          error: c.introspect_error,
        })
      : t("lib.credentials.check.rejected", { name: c.name, provider: providerLabel });
  }
  const caps = c.capabilities?.length ?? 0;
  // Zero discovered capabilities means the provider would not enumerate them and the key is treated
  // as unrestricted, so never say "0 permissions".
  if (c.identity) {
    return caps
      ? t("lib.credentials.check.validated_as_caps", {
          name: c.name,
          identity: c.identity,
          count: caps,
        })
      : t("lib.credentials.check.validated_as_no_caps", { name: c.name, identity: c.identity });
  }
  return caps
    ? t("lib.credentials.check.validated_caps", { name: c.name, count: caps })
    : t("lib.credentials.check.validated_no_caps", { name: c.name });
}

export type ToolGroupID = "read" | "write" | "actions";

export const TOOL_GROUPS: ToolGroupID[] = ["read", "write", "actions"];

export function toolGroup(row: ToolRow): ToolGroupID {
  const req = row.requires ?? [];
  if (req.some((p) => p.includes(":actions:"))) return "actions";
  if (req.some((p) => p.endsWith(":write"))) return "write";
  return "read";
}

export function groupTools(
  rows: ToolRow[],
  query = "",
): Array<{ id: ToolGroupID; rows: ToolRow[] }> {
  const q = query.trim().toLowerCase();
  const match = (r: ToolRow) =>
    !q || `${r.server}/${r.tool} ${r.summary ?? ""}`.toLowerCase().includes(q);
  return TOOL_GROUPS.map((id) => ({
    id,
    rows: rows.filter((r) => match(r) && toolGroup(r) === id),
  })).filter((g) => g.rows.length > 0);
}

export function permissionsUndiscovered(c: Credential): boolean {
  return c.state !== "invalid" && (c.capabilities?.length ?? 0) === 0 && (c.tools?.length ?? 0) > 0;
}

export function reachableByGitHub(origin: string): boolean {
  let host: string;
  try {
    host = new URL(origin).hostname.toLowerCase();
  } catch {
    return false;
  }
  if (host.startsWith("[")) host = host.slice(1, -1);
  if (host.includes(":")) {
    return !(
      host === "::1" ||
      /^f[cd][0-9a-f]{0,2}:/.test(host) ||
      /^fe[89ab][0-9a-f]?:/.test(host)
    );
  }
  const v4 = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    return !(
      a === 127 ||
      a === 10 ||
      a === 0 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      (a === 169 && b === 254) ||
      (a === 100 && b >= 64 && b <= 127)
    );
  }
  if (!host.includes(".")) return false;
  return !/\.(localhost|local|internal|lan|home\.arpa)$/.test(host);
}

export const OAUTH_PROVIDER = "oauth-connection";
// Must match the introspect_error codes the daemon stores for OAuth connections.
export const OAUTH_RECONNECT_REQUIRED = "oauth_reconnect_required";
export const OAUTH_DISCONNECTED = "oauth_disconnected";

export function isOAuth(c: Pick<Credential, "provider">): boolean {
  return c.provider === OAUTH_PROVIDER;
}

export interface OAuthServer {
  name: string;
  transport: string;
  credential_ref: string;
  url?: string;
  credential?: Credential;
}

export interface OAuthServers {
  servers: OAuthServer[];
  redirect_uri: string;
  client_metadata_url?: string;
}

export function fetchOAuthServers(): Promise<OAuthServers> {
  return api.get<OAuthServers>("/credentials/oauth/servers");
}

export function connectableServers(list: OAuthServer[]): OAuthServer[] {
  return list.filter((s) => !s.credential || isOAuth(s.credential));
}

export interface StartOAuthInput {
  server?: string;
  credential_id?: string;
  client_id?: string;
  client_secret?: string;
  scope?: string;
  grants?: Grant[];
  return?: "credentials" | "mcps";
}

// The daemon sets a cookie the callback checks, so the flow must finish in the browser that started
// it.
export async function startOAuth(input: StartOAuthInput): Promise<void> {
  const r = await api.post<{ authorize_url: string }>("/credentials/oauth/start", input);
  window.location.assign(r.authorize_url);
}

export function oauthStatusText(c: Credential | undefined): string {
  if (!c) return t("lib.credentials.oauth.not_connected");
  if (c.state === "valid") return t("lib.credentials.oauth.connected");
  if (c.introspect_error === OAUTH_DISCONNECTED) return t("lib.credentials.oauth.disconnected");
  return t("lib.credentials.oauth.reconnect");
}

export function oauthOutcome(
  q: URLSearchParams,
): { ok: boolean; message: string; credential?: string } | null {
  const outcome = q.get("mcp_oauth");
  if (outcome === "connected")
    return {
      ok: true,
      message: t("lib.credentials.oauth.done"),
      credential: q.get("credential") ?? "",
    };
  if (outcome !== "error") return null;
  const reasons: Record<string, string> = {
    state: t("lib.credentials.oauth.errors.state"),
    denied: t("lib.credentials.oauth.errors.denied"),
    token: t("lib.credentials.oauth.errors.token"),
    unavailable: t("lib.credentials.oauth.errors.unavailable"),
  };
  const reason = q.get("reason") ?? "";
  let message = Object.hasOwn(reasons, reason)
    ? reasons[reason]
    : t("lib.credentials.oauth.errors.failed");
  const code = q.get("detail");
  if (code) message += " " + t("lib.credentials.oauth.errors.code", { code });
  return { ok: false, message };
}
