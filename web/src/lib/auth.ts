// server.port in astro.config.mjs, which is strict. On any other port the daemon serves the
// dashboard itself: `thulu serve -addr`, a remapped publish, a port-forward, a proxy on 443.
export const DEV_SERVER_PORT = "4321";

// Same origin as the page in production. Only the Astro dev server needs the daemon's :8585.
export function apiBase(): string {
  if (typeof window === "undefined") return "http://localhost:8585";
  const { protocol, hostname, port, origin } = window.location;
  if (port !== DEV_SERVER_PORT) return origin;
  return `${protocol}//${hostname}:8585`;
}

// Compare origins, not a ":8585" substring: behind a proxy the daemon answers on the default port.
export function isDaemonURL(url: string): boolean {
  try {
    return new URL(url, window.location.href).origin === apiBase();
  } catch {
    return false;
  }
}

const CSRF_COOKIE = "octopus_csrf";

function readCookie(name: string): string | null {
  if (typeof document === "undefined") return null;
  const prefix = name + "=";
  for (const part of document.cookie.split(";")) {
    const c = part.trim();
    if (c.startsWith(prefix)) return decodeURIComponent(c.slice(prefix.length));
  }
  return null;
}

function csrfToken(): string | null {
  return readCookie(CSRF_COOKIE);
}

export function csrfHeaders(): Record<string, string> {
  const t = csrfToken();
  return t ? { "X-CSRF-Token": t } : {};
}

let refreshPromise: Promise<boolean> | null = null;

// The daemon rotates the refresh token on each use and tabs share the cookie, so refreshes are
// serialized across tabs and a tab that waited reuses the refresh another tab just finished.
const REFRESH_LOCK = "octopus-refresh";
const REFRESHED_KEY = "octopus_refreshed";
const LEASE_KEY = "octopus_refresh_lease";
const FRESH_MS = 5_000;
const LEASE_MS = 8_000;
const LEASE_POLL_MS = 50;

const tabId = Math.random().toString(36).slice(2);

function storage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function readJSON<T>(key: string): T | null {
  try {
    const raw = storage()?.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function writeJSON(key: string, value: unknown): void {
  try {
    storage()?.setItem(key, JSON.stringify(value));
  } catch {
    // Private mode or a full quota: the daemon's refresh grace window still covers us.
  }
}

function refreshedByAnotherTab(): boolean {
  const last = readJSON<{ tab: string; at: number }>(REFRESHED_KEY);
  return !!last && last.tab !== tabId && Date.now() - last.at < FRESH_MS;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// A 429 means "not now", not "signed out": session requests wait it out a couple of times instead
// of reading it as a lost session and sending the browser to /login/.
const RATE_RETRIES = 2;
const RATE_WAIT_MAX_MS = 5_000;

export function retryAfterMs(r: Response): number {
  const s = Number(r.headers.get("Retry-After"));
  return Number.isFinite(s) && s > 0 ? Math.min(s * 1000, RATE_WAIT_MAX_MS) : 1000;
}

export async function fetchPastRateLimit(url: string, init?: RequestInit): Promise<Response> {
  let r = await fetch(url, init);
  for (let i = 0; r.status === 429 && i < RATE_RETRIES; i++) {
    await sleep(retryAfterMs(r));
    r = await fetch(url, init);
  }
  return r;
}

// localStorage has no compare-and-set, so two tabs can take the lease at once;
// the daemon's refresh grace window makes that harmless.
async function withLease<T>(fn: () => Promise<T>): Promise<T> {
  if (!storage()) return fn();
  const deadline = Date.now() + LEASE_MS;
  for (;;) {
    const lease = readJSON<{ tab: string; until: number }>(LEASE_KEY);
    if (!lease || lease.tab === tabId || lease.until < Date.now() || Date.now() > deadline) break;
    await sleep(LEASE_POLL_MS);
  }
  writeJSON(LEASE_KEY, { tab: tabId, until: Date.now() + LEASE_MS });
  try {
    return await fn();
  } finally {
    if (readJSON<{ tab: string }>(LEASE_KEY)?.tab === tabId) {
      try {
        storage()?.removeItem(LEASE_KEY);
      } catch {
        // Expires on its own.
      }
    }
  }
}

function withRefreshLock<T>(fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator === "undefined" ? undefined : navigator.locks;
  if (locks?.request) return locks.request(REFRESH_LOCK, fn) as Promise<T>;
  return withLease(fn);
}

async function postRefresh(): Promise<boolean> {
  try {
    const r = await fetchPastRateLimit(`${apiBase()}/auth/refresh`, {
      method: "POST",
      credentials: "include",
    });
    return r.ok;
  } catch {
    return false;
  }
}

async function doRefresh(): Promise<boolean> {
  return withRefreshLock(async () => {
    if (refreshedByAnotherTab()) return true;
    const ok = await postRefresh();
    if (ok) writeJSON(REFRESHED_KEY, { tab: tabId, at: Date.now() });
    return ok;
  });
}

export function isAuthenticated(): boolean {
  return !!csrfToken();
}

export interface AuthConfig {
  dev_mode?: boolean;
  dev_session?: boolean;
  // 0 means no cap.
  api_key_max_ttl_seconds?: number;
}

let configPromise: Promise<AuthConfig> | null = null;

export function authConfig(): Promise<AuthConfig> {
  configPromise ??= fetch(`${apiBase()}/auth/config`, { credentials: "include" })
    .then((r) => (r.ok ? (r.json() as Promise<AuthConfig>) : {}))
    .catch(() => ({}));
  return configPromise;
}

// The dev session cookie is HttpOnly, so only the daemon can tell a browser that opened
// `thulu open`'s link from one that did not.
export function authDisabled(): Promise<boolean> {
  return authConfig().then((cfg) => cfg.dev_mode === true && cfg.dev_session === true);
}

// The redirect waits for the dev-mode check: on a no-login install an immediate redirect
// bounces every page to a /login/ with no provider. Callers ignore the return value.
export function requireAuth(): boolean {
  if (isAuthenticated()) return true;
  void authDisabled().then((open) => {
    if (open) return;
    window.location.href = "/login/?next=" + encodeURIComponent(window.location.pathname);
  });
  return false;
}

// A no-login install's dev session also sets the CSRF cookie, but /auth/me and /auth/workspaces
// answer it 401.
export async function hasLoginSession(): Promise<boolean> {
  if (!isAuthenticated()) return false;
  return !(await authDisabled());
}

export interface SessionInfo {
  user?: { id?: string; email?: string; name?: string; created_at?: string };
  workspace_id?: string;
  role?: string;
  // Absent from older daemons.
  operator?: boolean;
  // 0 means no cap.
  api_key_max_ttl_seconds?: number;
  identity_providers?: string[];
  // Absent from older daemons.
  can_create_workspace?: boolean;
  create_workspace_reason?: "" | "reserved" | "cap";
  create_workspace_max?: number;
  workspace_name?: string;
}

let sessionPromise: Promise<SessionInfo | null> | null = null;

// Raw fetch, not `api`: a 401 here is an answer, not a reason to navigate.
// The Layout.astro fetch wrapper skips it for the same reason (isSessionEndpoint).
export function sessionInfo(): Promise<SessionInfo | null> {
  sessionPromise ??= (async () => {
    if (!(await hasLoginSession())) return null;
    const get = () => fetch(`${apiBase()}/auth/me`, { credentials: "include" });
    try {
      let r = await get();
      if (r.status === 401 && isAuthenticated() && (await refreshOnUnauthorized())) r = await get();
      return r.ok ? ((await r.json()) as SessionInfo) : null;
    } catch {
      return null;
    }
  })();
  return sessionPromise;
}

export async function logout(): Promise<string> {
  try {
    const r = await fetch(`${apiBase()}/auth/logout`, {
      method: "POST",
      credentials: "include",
    });
    const body = r.ok ? ((await r.json()) as { logout_url?: unknown }) : {};
    if (typeof body.logout_url === "string") {
      const u = new URL(body.logout_url);
      if (u.protocol === "https:" || u.protocol === "http:") return u.href;
    }
  } catch {
    // Fire-and-forget — cookies are cleared server-side; a failed call just
    // leaves them to expire.
  }
  return "/login/";
}

// The CSRF cookie only says a session existed once: an expired refresh, or another daemon that
// answered on the same host name, leaves one behind. The login page asks the daemon before it
// sends the browser on, or the dashboard sends it straight back. "unknown" is a daemon that is
// unreachable or still rate limiting.
export type SessionCheck = "ok" | "none" | "unknown";

export async function verifySession(): Promise<SessionCheck> {
  if (!isAuthenticated()) return "none";
  const me = () => fetchPastRateLimit(`${apiBase()}/auth/me`, { credentials: "include" });
  try {
    let r = await me();
    if (r.status === 401 && (await refreshOnUnauthorized())) r = await me();
    if (r.ok) return "ok";
    return r.status === 401 ? "none" : "unknown";
  } catch {
    return "unknown";
  }
}

// Same name and path the daemon writes it with; the HttpOnly session cookies are the daemon's to clear.
export function dropStaleCSRF(): void {
  document.cookie = `${CSRF_COOKIE}=; Max-Age=0; Path=/`;
}

// The last guard against a login ↔ dashboard loop: a tab the login page sent on three times in
// 30 s stays on the login page.
const BOUNCE_KEY = "octopus_login_bounces";
const BOUNCE_WINDOW_MS = 30_000;
const BOUNCE_MAX = 3;

export function takeLoginBounce(now = Date.now()): boolean {
  let recent: number[] = [];
  try {
    const raw = sessionStorage.getItem(BOUNCE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (Array.isArray(parsed)) {
      recent = parsed.filter(
        (t): t is number => typeof t === "number" && now - t < BOUNCE_WINDOW_MS,
      );
    }
  } catch {
    // Without storage there is no count, and the session check above still holds.
  }
  if (recent.length >= BOUNCE_MAX) return false;
  try {
    sessionStorage.setItem(BOUNCE_KEY, JSON.stringify([...recent, now]));
  } catch {
    // Same as above.
  }
  return true;
}

export async function refreshOnUnauthorized(): Promise<boolean> {
  refreshPromise ??= doRefresh().finally(() => {
    refreshPromise = null;
  });
  return refreshPromise;
}

// The fetch wrappers return these 401s untouched: recovering from /auth/refresh or /auth/logout is
// circular,
// and /auth/me and /auth/config 401s are answers sessionInfo() and authDisabled() read
// (/auth/config would wait on itself).
const SESSION_ENDPOINTS = new Set(["/auth/refresh", "/auth/logout", "/auth/me", "/auth/config"]);

export function isSessionEndpoint(url: string): boolean {
  try {
    return SESSION_ENDPOINTS.has(new URL(url, window.location.href).pathname);
  } catch {
    return false;
  }
}

// On a no-login install (OCTOPUS_DEV_MODE) /login/ sends the browser straight back home,
// so the 401 stays with the request that got it.
export async function recoverFromUnauthorized(): Promise<boolean> {
  if (await authDisabled()) return false;
  if (await refreshOnUnauthorized()) return true;
  if (!window.location.pathname.startsWith("/login")) {
    window.location.href = "/login/";
  }
  return false;
}
