import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface Route {
  status: number;
  body?: unknown;
}
let routes: Record<string, Route>;
let calls: string[];
let location: {
  protocol: string;
  hostname: string;
  port: string;
  origin: string;
  href: string;
  pathname: string;
};

function daemon(overrides: Record<string, Route>) {
  routes = {
    "/auth/config": { status: 200, body: { dev_mode: false } },
    "/auth/refresh": { status: 401, body: { error: "no refresh token" } },
    "/auth/me": { status: 401, body: { error: "not authenticated" } },
    ...overrides,
  };
}

async function load() {
  vi.resetModules();
  const auth = await import("./auth");
  const api = await import("./api");
  const wrapper = await import("./daemon-fetch");
  return { ...auth, ...api, ...wrapper };
}

async function loadWithWrapper() {
  const mods = await load();
  vi.stubGlobal("fetch", mods.daemonFetch(globalThis.fetch));
  return mods;
}

beforeEach(() => {
  calls = [];
  location = {
    protocol: "http:",
    hostname: "127.0.0.1",
    port: "8585",
    origin: "http://127.0.0.1:8585",
    href: "http://127.0.0.1:8585/settings/",
    pathname: "/settings/",
  };
  vi.stubGlobal("window", { location });
  vi.stubGlobal("fetch", (url: string, init?: { method?: string }) => {
    const path = new URL(url).pathname;
    calls.push(`${init?.method ?? "GET"} ${path}`);
    const r = routes[path] ?? { status: 404, body: { error: "not found" } };
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      json: () => Promise.resolve(r.body ?? {}),
      text: () => Promise.resolve(JSON.stringify(r.body ?? {})),
    });
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("isSessionEndpoint", () => {
  it("matches the session endpoints by path, and nothing else", async () => {
    const { isSessionEndpoint } = await load();
    for (const u of [
      "http://127.0.0.1:8585/auth/me",
      "/auth/config",
      "/auth/refresh",
      "http://127.0.0.1:8585/auth/logout?x=1",
    ]) {
      expect(isSessionEndpoint(u), u).toBe(true);
    }
    for (const u of [
      "/auth/sessions",
      "/auth/api-keys",
      "/auth/workspaces/w1/members",
      "/boards",
      "/x/auth/me",
    ]) {
      expect(isSessionEndpoint(u), u).toBe(false);
    }
  });
});

describe("recoverFromUnauthorized", () => {
  const cases: Array<{
    name: string;
    devMode: boolean;
    refresh: number;
    path?: string;
    want: boolean;
    wantHref: string;
    wantCalls: string[];
  }> = [
    {
      name: "no-login install: stays on the page and does not try a refresh",
      devMode: true,
      refresh: 401,
      want: false,
      wantHref: "http://127.0.0.1:8585/settings/",
      wantCalls: ["GET /auth/config"],
    },
    {
      name: "logins on, refresh works: retry",
      devMode: false,
      refresh: 200,
      want: true,
      wantHref: "http://127.0.0.1:8585/settings/",
      wantCalls: ["GET /auth/config", "POST /auth/refresh"],
    },
    {
      name: "logins on, session gone: go to /login/",
      devMode: false,
      refresh: 401,
      want: false,
      wantHref: "/login/",
      wantCalls: ["GET /auth/config", "POST /auth/refresh"],
    },
    {
      name: "already on /login/: do not reload it",
      devMode: false,
      refresh: 401,
      path: "/login/",
      want: false,
      wantHref: "http://127.0.0.1:8585/settings/",
      wantCalls: ["GET /auth/config", "POST /auth/refresh"],
    },
  ];

  for (const c of cases) {
    it(c.name, async () => {
      daemon({
        "/auth/config": { status: 200, body: { dev_mode: c.devMode, dev_session: c.devMode } },
        "/auth/refresh": { status: c.refresh },
      });
      if (c.path) location.pathname = c.path;
      const { recoverFromUnauthorized } = await load();

      expect(await recoverFromUnauthorized()).toBe(c.want);
      expect(location.href).toBe(c.wantHref);
      expect(calls).toEqual(c.wantCalls);
    });
  }

  it("no-login install, browser without the dev link: go to /login/, which says how to get in", async () => {
    daemon({ "/auth/config": { status: 200, body: { dev_mode: true, dev_session: false } } });
    const { recoverFromUnauthorized } = await load();

    expect(await recoverFromUnauthorized()).toBe(false);
    expect(location.href).toBe("/login/");
  });
});

describe("api client on 401 and 403", () => {
  it("on a no-login install, a 401 fails that request and leaves the page where it is", async () => {
    daemon({
      "/auth/config": { status: 200, body: { dev_mode: true, dev_session: true } },
      "/auth/sessions": { status: 401, body: { error: "not authenticated" } },
    });
    const { api, ApiError } = await load();

    const err = await api.get("/auth/sessions").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, message: "not authenticated" });
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
    expect(calls).not.toContain("POST /auth/refresh");
  });

  it("with logins on, a lapsed session refreshes and retries once", async () => {
    let first = true;
    daemon({ "/auth/refresh": { status: 200 } });
    vi.stubGlobal("fetch", (url: string, init?: { method?: string }) => {
      const path = new URL(url).pathname;
      calls.push(`${init?.method ?? "GET"} ${path}`);
      let r = routes[path] ?? { status: 200, body: [] };
      if (path === "/webhooks") {
        r = first ? { status: 401, body: { error: "token expired" } } : { status: 200, body: [] };
        first = false;
      }
      return Promise.resolve({
        ok: r.status >= 200 && r.status < 300,
        status: r.status,
        json: () => Promise.resolve(r.body ?? {}),
      });
    });
    const { api } = await load();

    expect(await api.get("/webhooks")).toEqual([]);
    expect(calls).toEqual([
      "GET /webhooks",
      "GET /auth/config",
      "POST /auth/refresh",
      "GET /webhooks",
    ]);
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
  });

  it("with logins on, a session that cannot be refreshed goes to /login/", async () => {
    daemon({ "/boards": { status: 401, body: { error: "not authenticated" } } });
    const { api, ApiError } = await load();

    await expect(api.get("/boards")).rejects.toBeInstanceOf(ApiError);
    expect(location.href).toBe("/login/");
  });

  it("a 403 is the widget's error and never navigates", async () => {
    daemon({ "/auth/api-keys": { status: 403, body: { error: "admin or API key required" } } });
    const { api } = await load();

    await expect(api.get("/auth/api-keys")).rejects.toMatchObject({
      status: 403,
      message: "admin or API key required",
    });
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
    expect(calls).toEqual(["GET /auth/api-keys"]);
  });
});

describe("daemonFetch (the Layout.astro wrapper)", () => {
  it("on a no-login install, sessionInfo() does not ask and the page stays", async () => {
    daemon({ "/auth/config": { status: 200, body: { dev_mode: true, dev_session: true } } });
    const { sessionInfo } = await loadWithWrapper();
    const { mayWriteInstallWide } = await import("./operator");

    expect(await sessionInfo()).toBeNull();
    expect(await mayWriteInstallWide()).toBe(true);
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
    expect(calls).toEqual([]);
  });

  it("asks /auth/me when logins are on", async () => {
    daemon({ "/auth/me": { status: 200, body: { id: "u1" } } });
    vi.stubGlobal("document", { cookie: "octopus_csrf=abc" });
    const { sessionInfo } = await loadWithWrapper();

    expect(await sessionInfo()).toEqual({ id: "u1" });
    expect(calls).toEqual(["GET /auth/config", "GET /auth/me"]);
  });

  it("asks /auth/me once the install turns out to have logins", async () => {
    daemon({ "/auth/me": { status: 200, body: { role: "admin", api_key_max_ttl_seconds: 60 } } });
    vi.stubGlobal("document", { cookie: "octopus_csrf=abc" });
    const { sessionInfo } = await loadWithWrapper();

    expect((await sessionInfo())?.role).toBe("admin");
    expect(calls).toEqual(["GET /auth/config", "GET /auth/me"]);
  });

  it("reads the key cap from /auth/config, once per page", async () => {
    daemon({
      "/auth/config": {
        status: 200,
        body: { dev_mode: true, dev_session: true, api_key_max_ttl_seconds: 2592000 },
      },
    });
    const { authConfig, authDisabled } = await loadWithWrapper();

    expect((await authConfig()).api_key_max_ttl_seconds).toBe(2592000);
    expect(await authDisabled()).toBe(true);
    expect(calls).toEqual(["GET /auth/config"]);
  });

  it("in a no-login install's dev session, sessionInfo() asks only whether logins exist", async () => {
    daemon({ "/auth/config": { status: 200, body: { dev_mode: true, dev_session: true } } });
    vi.stubGlobal("document", { cookie: "octopus_csrf=abc" });
    const { sessionInfo, hasLoginSession } = await loadWithWrapper();

    expect(await hasLoginSession()).toBe(false);
    expect(await sessionInfo()).toBeNull();
    expect(calls).toEqual(["GET /auth/config"]);
  });

  it("with a session cookie, lets sessionInfo() read a 401 from /auth/me without leaving the page", async () => {
    daemon({});
    vi.stubGlobal("document", { cookie: "octopus_csrf=abc" });
    const { sessionInfo } = await loadWithWrapper();

    expect(await sessionInfo()).toBeNull();
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
    expect(calls).toEqual(["GET /auth/config", "GET /auth/me", "POST /auth/refresh"]);
  });

  it("with a session cookie, reads the session from /auth/me", async () => {
    daemon({ "/auth/me": { status: 200, body: { role: "owner", operator: false } } });
    vi.stubGlobal("document", { cookie: "octopus_csrf=abc" });
    const { sessionInfo } = await loadWithWrapper();

    expect(await sessionInfo()).toEqual({ role: "owner", operator: false });
  });

  const cases: Array<{ name: string; devMode: boolean; status: number; wantHref: string }> = [
    {
      name: "no-login install: a widget's 401 stays with the widget",
      devMode: true,
      status: 401,
      wantHref: "http://127.0.0.1:8585/settings/",
    },
    {
      name: "logins on: a 401 nothing can refresh goes to /login/",
      devMode: false,
      status: 401,
      wantHref: "/login/",
    },
    {
      name: "logins on: a 403 never navigates",
      devMode: false,
      status: 403,
      wantHref: "http://127.0.0.1:8585/settings/",
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      daemon({
        "/auth/config": { status: 200, body: { dev_mode: c.devMode, dev_session: c.devMode } },
        "/auth/sessions": { status: c.status, body: { error: "nope" } },
      });
      await loadWithWrapper();

      const r = await fetch(`${location.origin}/auth/sessions`);
      expect(r.status).toBe(c.status);
      expect(location.href).toBe(c.wantHref);
    });
  }

  it("leaves requests to other origins alone", async () => {
    daemon({});
    await loadWithWrapper();

    await fetch("https://example.com/auth/sessions");
    expect(calls).toEqual(["GET /auth/sessions"]);
    expect(location.href).toBe("http://127.0.0.1:8585/settings/");
  });
});

describe("logout", () => {
  const cases: Array<{ name: string; route: Route; want: string }> = [
    {
      name: "goes to the provider's sign-out page when the daemon returns one",
      route: {
        status: 200,
        body: { status: "ok", logout_url: "https://id.example/end_session?client_id=c" },
      },
      want: "https://id.example/end_session?client_id=c",
    },
    {
      name: "goes to /login/ without one",
      route: { status: 200, body: { status: "ok" } },
      want: "/login/",
    },
    {
      name: "never follows a non-http URL",
      route: { status: 200, body: { logout_url: "javascript:alert(1)" } },
      want: "/login/",
    },
    { name: "goes to /login/ when the call fails", route: { status: 500 }, want: "/login/" },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      daemon({ "/auth/logout": c.route });
      const { logout } = await load();
      expect(await logout()).toBe(c.want);
      expect(calls).toEqual(["POST /auth/logout"]);
    });
  }
});

describe("refresh across tabs", () => {
  async function twoTabs() {
    vi.resetModules();
    const a = await import("./auth");
    vi.resetModules();
    const b = await import("./auth");
    return { a, b };
  }

  function memoryStorage(): Storage {
    const m = new Map<string, string>();
    return {
      get length() {
        return m.size;
      },
      clear: () => m.clear(),
      getItem: (k: string) => m.get(k) ?? null,
      key: (i: number) => [...m.keys()][i] ?? null,
      removeItem: (k: string) => void m.delete(k),
      setItem: (k: string, v: string) => void m.set(k, v),
    };
  }

  function fakeLocks() {
    const tails = new Map<string, Promise<unknown>>();
    return {
      request: (name: string, cb: () => Promise<unknown>) => {
        const prev = tails.get(name) ?? Promise.resolve();
        const run = prev.then(cb, cb);
        tails.set(
          name,
          run.catch(() => undefined),
        );
        return run;
      },
    };
  }

  function slowRefresh(status: number) {
    vi.stubGlobal("fetch", (url: string, init?: { method?: string }) => {
      const path = new URL(url).pathname;
      calls.push(`${init?.method ?? "GET"} ${path}`);
      return new Promise((resolve) =>
        setTimeout(() => resolve({ ok: status >= 200 && status < 300, status }), 20),
      );
    });
  }

  for (const withLocks of [true, false]) {
    const how = withLocks ? "navigator.locks" : "the localStorage lease";
    it(`two tabs refreshing at once post the refresh token once (${how})`, async () => {
      vi.stubGlobal("localStorage", memoryStorage());
      vi.stubGlobal("navigator", withLocks ? { locks: fakeLocks() } : {});
      slowRefresh(200);
      const { a, b } = await twoTabs();

      const both = await Promise.all([a.refreshOnUnauthorized(), b.refreshOnUnauthorized()]);
      expect(both).toEqual([true, true]);
      expect(calls.filter((c) => c === "POST /auth/refresh")).toHaveLength(1);
    });
  }

  it("a tab's own earlier refresh does not stand in for a new one", async () => {
    vi.stubGlobal("localStorage", memoryStorage());
    vi.stubGlobal("navigator", { locks: fakeLocks() });
    slowRefresh(200);
    const { a } = await twoTabs();

    expect(await a.refreshOnUnauthorized()).toBe(true);
    expect(await a.refreshOnUnauthorized()).toBe(true);
    expect(calls.filter((c) => c === "POST /auth/refresh")).toHaveLength(2);
  });

  it("a failed refresh in one tab leaves the other to try its own", async () => {
    vi.stubGlobal("localStorage", memoryStorage());
    vi.stubGlobal("navigator", { locks: fakeLocks() });
    slowRefresh(401);
    const { a, b } = await twoTabs();

    expect(await a.refreshOnUnauthorized()).toBe(false);
    expect(await b.refreshOnUnauthorized()).toBe(false);
    expect(calls.filter((c) => c === "POST /auth/refresh")).toHaveLength(2);
  });

  it("refreshes without localStorage or locks at all", async () => {
    vi.stubGlobal("navigator", {});
    slowRefresh(200);
    const { a } = await twoTabs();

    expect(await a.refreshOnUnauthorized()).toBe(true);
    expect(calls).toEqual(["POST /auth/refresh"]);
  });
});
