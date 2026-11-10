// browser_run_script runs caller JS in this process, so every POST /messages must present the
// session's key: the session id is not a credential.
// per-run (MCP_REQUIRED_KEY): only that key. shared: any run key, keyless only for ?catalog=1
// list-only. MCP_CATALOG_ONLY: only ?catalog=1, list-only.

import { timingSafeEqual } from "node:crypto";

import { t } from "../i18n.js";

export function sameKey(a, b) {
  if (!a || !b) return false;
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && timingSafeEqual(x, y);
}

/**
 * Decides an SSE handshake.
 *
 * @param {{key: string, catalog: boolean, requiredKey?: string, catalogOnly?: boolean}} hs
 * @returns {{ok: true, key: string, listOnly: boolean} | {ok: false, status: number, error: string}}
 */
export function admitHandshake({ key, catalog, requiredKey = "", catalogOnly = false }) {
  if (catalogOnly) {
    if (catalog) return { ok: true, key: "", listOnly: true };
    return { ok: false, status: 403, error: t("errors.handshake_catalog_only") };
  }
  if (requiredKey) {
    if (sameKey(key, requiredKey)) return { ok: true, key, listOnly: false };
    return { ok: false, status: 401, error: t("errors.handshake_wrong_run_key") };
  }
  if (key) return { ok: true, key, listOnly: false };
  if (catalog) return { ok: true, key: "", listOnly: true };
  return { ok: false, status: 401, error: t("errors.handshake_no_key") };
}

// A list-only session has nothing to impersonate, so it accepts any caller.
export function admitMessage(sessionKey, key) {
  if (!sessionKey) return true;
  return sameKey(sessionKey, key);
}

/**
 * The tools a session may see and call. `requested` is the handshake's
 * ?tools= (null when absent), which the caller writes itself; `granted` is
 * MCP_ALLOWED_TOOLS, which the daemon sets on a per-run browser to the tools
 * the run's stage grants (null when the stage grants the whole server). A
 * request can narrow the grant and never widen it: an agent that edits its own
 * .mcp.json still cannot reach browser_run_script when the stage only granted
 * screenshots.
 *
 * @param {string[] | null} requested
 * @param {string[] | null} granted
 * @returns {string[] | null} null means every tool
 */
export function grantedTools(requested, granted) {
  if (!granted) return requested;
  if (!requested) return granted;
  return requested.filter((name) => granted.includes(name));
}
