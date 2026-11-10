import { afterEach, describe, expect, it, vi } from "vitest";

import {
  checkSummary,
  checkTone,
  connectableServers,
  credentialColumns,
  decisionColumns,
  fieldInputs,
  groupTools,
  helpHTML,
  installColumns,
  introspectNote,
  OAUTH_DISCONNECTED,
  OAUTH_PROVIDER,
  OAUTH_RECONNECT_REQUIRED,
  oauthOutcome,
  oauthStatusText,
  PROVIDER_UNREACHABLE,
  listCredentials,
  permissionsUndiscovered,
  reachableByGitHub,
  toolGroup,
  type Credential,
  type ToolRow,
} from "./credentials";

function answer(status: number, body: unknown) {
  vi.stubGlobal("fetch", () =>
    Promise.resolve({
      ok: status < 400,
      status,
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listCredentials", () => {
  it("unwraps the list from the daemon's object", async () => {
    answer(200, {
      credentials: [
        { id: "c1", name: "gh", provider: "github", kind: "pat", state: "valid" },
        { id: "c2", name: "aws", provider: "aws", kind: "keys", state: "valid" },
      ],
    });
    const list = await listCredentials();
    expect(list.map((c) => c.id)).toEqual(["c1", "c2"]);
  });

  it("reads null as an empty workspace", async () => {
    answer(200, { credentials: null });
    expect(await listCredentials()).toEqual([]);
  });

  it("lets a 403 through so the page can tell 'not allowed' from 'none'", async () => {
    answer(403, { error: "forbidden" });
    await expect(listCredentials()).rejects.toMatchObject({ status: 403 });
  });
});

const row = (tool: string, requires: string[], summary = ""): ToolRow => ({
  server: "thulu-github",
  tool,
  summary,
  requires,
  decision: { allowed: true, reason: "ok" },
});

describe("toolGroup", () => {
  it("puts Actions first, then writes, and calls the rest reads", () => {
    expect(toolGroup(row("run_dispatch", ["github:actions:write"]))).toBe("actions");
    expect(toolGroup(row("run_logs", ["github:actions:read", "github:repo:read"]))).toBe("actions");
    expect(toolGroup(row("pr_create", ["github:repo:write"]))).toBe("write");
    expect(toolGroup(row("pr_view", ["github:repo:read"]))).toBe("read");
    expect(toolGroup(row("logs_search", ["aws:logs:StartQuery"]))).toBe("read");
    expect(toolGroup({ ...row("x", []), requires: undefined })).toBe("read");
  });
});

describe("groupTools", () => {
  const rows = [
    row("pr_create", ["github:repo:write"], "Open a pull request"),
    row("pr_view", ["github:repo:read"], "Read a pull request"),
    row("run_dispatch", ["github:actions:write"], "Start a workflow"),
  ];
  it("keeps the group order and drops empty groups", () => {
    expect(groupTools(rows).map((g) => g.id)).toEqual(["read", "write", "actions"]);
    expect(groupTools(rows.slice(0, 2)).map((g) => g.id)).toEqual(["read", "write"]);
  });
  it("filters on name and summary, ignoring case", () => {
    expect(groupTools(rows, "WORKFLOW").flatMap((g) => g.rows.map((r) => r.tool))).toEqual([
      "run_dispatch",
    ]);
    expect(groupTools(rows, "pr_").flatMap((g) => g.rows.map((r) => r.tool))).toEqual([
      "pr_view",
      "pr_create",
    ]);
    expect(groupTools(rows, "nothing")).toEqual([]);
  });
});

describe("permissionsUndiscovered", () => {
  const base: Credential = {
    id: "c",
    name: "c",
    provider: "github",
    kind: "token",
    state: "valid",
  };
  it("is true for a valid key with tools and no permissions found", () => {
    expect(permissionsUndiscovered({ ...base, tools: [row("pr_view", [])] })).toBe(true);
  });
  it("is false once permissions are known, for an invalid key, or with no tools", () => {
    expect(
      permissionsUndiscovered({
        ...base,
        capabilities: ["github:repo:read"],
        tools: [row("a", [])],
      }),
    ).toBe(false);
    expect(permissionsUndiscovered({ ...base, state: "invalid", tools: [row("a", [])] })).toBe(
      false,
    );
    expect(permissionsUndiscovered(base)).toBe(false);
  });
});

describe("checkSummary when the provider did not answer", () => {
  const rotated: Credential = {
    id: "c1",
    name: "gh",
    provider: "github",
    kind: "token",
    state: "unknown",
    introspect_error: PROVIDER_UNREACHABLE,
  };

  it("says the key was saved but not checked, never that it validated", () => {
    expect(checkSummary(rotated, "GitHub")).toBe(
      "gh saved but not checked: GitHub did not answer. Try Re-check later.",
    );
    expect(checkTone(rotated)).toBe("err");
  });

  it("does not take an earlier verdict for a fresh one", () => {
    const stale = { ...rotated, state: "valid", capabilities: ["github:repo:read"] };
    expect(checkSummary(stale, "GitHub")).toMatch(/not checked/);
    expect(checkTone(stale)).toBe("err");
  });

  it("shows a sentence an older daemon stored as it is", () => {
    const old = { ...rotated, introspect_error: "could not reach github: dial tcp: i/o timeout" };
    expect(checkSummary(old)).toBe(
      "gh saved but not checked: could not reach github: dial tcp: i/o timeout",
    );
  });

  it("still validates a key the provider answered for", () => {
    const ok: Credential = { ...rotated, state: "valid", introspect_error: undefined };
    expect(checkSummary(ok)).toBe("gh validated — capabilities not discovered");
    expect(checkTone(ok)).toBe("ok");
    const rejected: Credential = {
      ...rotated,
      state: "invalid",
      introspect_error: "Bad credentials",
    };
    expect(checkSummary(rejected, "GitHub")).toBe("gh was rejected by GitHub — Bad credentials");
    expect(checkTone(rejected)).toBe("err");
  });
});

describe("introspectNote", () => {
  const base: Credential = {
    id: "c1",
    name: "gh",
    provider: "github",
    kind: "token",
    state: "valid",
  };
  it("words the code the daemon stores, with the provider's name", () => {
    expect(introspectNote({ ...base, introspect_error: PROVIDER_UNREACHABLE }, "GitHub")).toBe(
      "Could not reach GitHub. Try Re-check later.",
    );
  });
  it("passes the provider's own message through, and says nothing without one", () => {
    expect(introspectNote({ ...base, state: "invalid", introspect_error: "Bad credentials" })).toBe(
      "Bad credentials",
    );
    expect(introspectNote(base)).toBe("");
  });
});

describe("fieldInputs", () => {
  const kind = {
    id: "token",
    label: "Token",
    fields: [
      { name: "token", label: "Token", secret: true, placeholder: "ghp_…", help: "Scope `repo`." },
      { name: "note", label: "Note", optional: true, multiline: true },
    ],
  };

  it("writes a field per input, the label naming the control and the help tied to it", () => {
    const html = fieldInputs(kind, "f-");
    expect(html).toContain('<label class="field-label" for="f-token">Token</label>');
    expect(html).toContain(
      '<input type="password" autocomplete="off" id="f-token" placeholder="ghp_…" spellcheck="false" aria-describedby="f-token-help">',
    );
    expect(html).toContain(
      '<p class="field-help" id="f-token-help">Scope <code class="code-inline">repo</code>.</p>',
    );
  });

  it("marks an optional input and leaves one without help undescribed", () => {
    const html = fieldInputs(kind, "r-");
    expect(html).toContain('for="r-note">Note <span class="field-optional">');
    expect(html).toContain(
      '<textarea class="mono" rows="4" id="r-note" placeholder="" spellcheck="false">',
    );
    expect(html.match(/class="field"/g)).toHaveLength(2);
  });

  it("writes nothing without a kind", () => {
    expect(fieldInputs(undefined, "f-")).toBe("");
  });
});

describe("helpHTML", () => {
  it("shows backticked names as code and escapes the rest", () => {
    expect(helpHTML("Needs `repo`; add `workflow` <now>.")).toBe(
      'Needs <code class="code-inline">repo</code>; add <code class="code-inline">workflow</code> &lt;now&gt;.',
    );
  });
  it("leaves a lone backtick as text", () => {
    expect(helpHTML("a ` b")).toBe("a ` b");
  });
});

describe("reachableByGitHub", () => {
  it("refuses loopback, private and local names", () => {
    for (const o of [
      "http://127.0.0.1:8585",
      "http://localhost:8585",
      "http://10.0.0.4",
      "http://172.20.1.1",
      "http://10.0.0.5:8585",
      "http://100.100.1.1",
      "http://[::1]:8585",
      "http://[fd00::1]",
      "http://octopus.local",
      "http://devbox",
      "not a url",
    ])
      expect(reachableByGitHub(o), o).toBe(false);
  });
  it("accepts a public address", () => {
    for (const o of [
      "https://octopus.acme.dev",
      "http://203.0.113.10:8585",
      "https://[2606:50c0::1]",
    ])
      expect(reachableByGitHub(o), o).toBe(true);
  });
});

describe("credential tables", () => {
  it("give every table its own column count, the actions header hidden", () => {
    expect(credentialColumns()).toHaveLength(6);
    expect(decisionColumns()).toHaveLength(6);
    expect(installColumns()).toHaveLength(4);
    for (const cols of [credentialColumns(), installColumns()])
      expect(cols[cols.length - 1].hideLabel).toBe(true);
  });

  it("name the re-check cell, the hook a phone uses to leave it out", () => {
    expect(credentialColumns()[5].cls).toBe("cred-check");
  });

  it("shrinks only the decision's time and verdict, leaving the ids room", () => {
    expect(decisionColumns().map((c) => c.width)).toEqual([
      "shrink",
      "shrink",
      undefined,
      undefined,
      undefined,
      undefined,
    ]);
  });
});

describe("OAuth connections", () => {
  const conn = (state: string, introspect_error?: string): Credential => ({
    id: "c1",
    name: "slack",
    provider: OAUTH_PROVIDER,
    kind: "mcp",
    state,
    introspect_error,
  });

  it("offers servers with no credential or an OAuth one, never a typed key", () => {
    const typed: Credential = { ...conn("valid"), provider: "http_header" };
    const names = connectableServers([
      { name: "slack", transport: "http", credential_ref: "slack", credential: conn("valid") },
      { name: "linear", transport: "http", credential_ref: "linear" },
      { name: "jira", transport: "http", credential_ref: "jira", credential: typed },
    ]).map((s) => s.name);
    expect(names).toEqual(["slack", "linear"]);
  });

  it("words the connection's state", () => {
    expect(oauthStatusText(undefined)).toBe("Not connected");
    expect(oauthStatusText(conn("valid"))).toBe("Connected");
    expect(oauthStatusText(conn("invalid", OAUTH_DISCONNECTED))).toBe("Disconnected");
    expect(oauthStatusText(conn("invalid", OAUTH_RECONNECT_REQUIRED))).toBe("Needs a reconnect");
    expect(introspectNote(conn("invalid", OAUTH_RECONNECT_REQUIRED))).toContain("Reconnect");
  });

  it("reads the callback's outcome and nothing else from the URL", () => {
    expect(oauthOutcome(new URLSearchParams("tab=x"))).toBeNull();
    const ok = oauthOutcome(new URLSearchParams("mcp_oauth=connected&credential=c9"));
    expect(ok).toMatchObject({ ok: true, credential: "c9" });
    const denied = oauthOutcome(
      new URLSearchParams("mcp_oauth=error&reason=denied&detail=access_denied"),
    );
    expect(denied?.ok).toBe(false);
    expect(denied?.message).toContain("access_denied");
    const odd = oauthOutcome(new URLSearchParams("mcp_oauth=error&reason=constructor"));
    expect(odd?.message).toBe("The connection could not be saved. Try again.");
  });
});
