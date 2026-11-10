// public/boot/fetch-credentials.js applies credentials: include before any module loads; keep both
// in sync.

import { csrfHeaders, isDaemonURL, isSessionEndpoint, recoverFromUnauthorized } from "./auth";

const CSRF_HEADER = "X-CSRF-Token";

export function daemonFetch(orig: typeof fetch): typeof fetch {
  async function patched(
    input: RequestInfo | URL,
    init?: RequestInit,
    retry = true,
  ): Promise<Response> {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (!isDaemonURL(url)) {
      return orig(input, init);
    }

    const method = (
      init?.method ?? (input instanceof Request ? input.method : "GET")
    ).toUpperCase();
    const nextInit: RequestInit = { ...init, credentials: "include" };
    if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
      const headers = new Headers(init?.headers);
      const csrf = csrfHeaders()["X-CSRF-Token"];
      if (csrf && !headers.has(CSRF_HEADER)) headers.set(CSRF_HEADER, csrf);
      nextInit.headers = headers;
    }

    // 401s from session endpoints are left to their callers (isSessionEndpoint); recovering on them
    // would recurse into the refresh.
    const resp = await orig(input, nextInit);
    if (
      resp.status === 401 &&
      retry &&
      !isSessionEndpoint(url) &&
      (await recoverFromUnauthorized())
    ) {
      return patched(input, nextInit, false);
    }
    return resp;
  }
  return (input, init) => patched(input, init);
}
