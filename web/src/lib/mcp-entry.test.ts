import { describe, it, expect } from "vitest";
import {
  parseMCPEntry,
  formatMCPEntry,
  grantedTools,
  entryForSelection,
  entryForSave,
  bareGrantKnown,
  bareGrantSummary,
  serverTickSelection,
  allToolsToggle,
  allToolsState,
  stageGrant,
  toolGroups,
  type ToolDef,
} from "./mcp-entry";

const github: ToolDef = {
  name: "thulu-github",
  default_scope: "write",
  tools: [
    { name: "pr_view", scope: "read" },
    { name: "run_logs", scope: "read" },
    { name: "pr_create", scope: "write" },
    { name: "run_cancel", scope: "write" },
    { name: "run_dispatch", scope: "deploy" },
    { name: "deployment_approve", scope: "deploy" },
  ],
};

const api: ToolDef = {
  name: "octopus-api",
  default_scope: "read",
  tools: [
    { name: "task_list", scope: "read" },
    { name: "task_create", scope: "write" },
    { name: "task_create_on_board", scope: "board" },
  ],
};

const opaque: ToolDef = { name: "ticket-reader", tools: [] };

const unscoped: ToolDef = {
  name: "thulu-browser",
  source: "builtin",
  tools: [{ name: "screenshot" }, { name: "get_text" }],
};

const probed: ToolDef = {
  name: "octopus-api",
  source: "runtime",
  tools: [{ name: "task_list" }, { name: "task_create" }, { name: "task_delete" }],
};

const ticked = (def: ToolDef, entry: string) => [...grantedTools(def, parseMCPEntry(entry))].sort();

const resaved = (def: ToolDef, entry: string) => {
  const parsed = parseMCPEntry(entry);
  const selection = [...grantedTools(def, parsed)];
  return formatMCPEntry(entryForSelection(def, def.name, parsed.scope, selection));
};

describe("parseMCPEntry", () => {
  it("reads a bare server", () => {
    expect(parseMCPEntry("thulu-github")).toEqual({
      server: "thulu-github",
      scope: null,
      tools: null,
    });
  });

  it("reads a scope keyword", () => {
    expect(parseMCPEntry("octopus-api@write").scope).toBe("write");
  });

  it("reads a scope and named tools together", () => {
    expect(parseMCPEntry("thulu-github@write+run_dispatch")).toEqual({
      server: "thulu-github",
      scope: "write",
      tools: ["run_dispatch"],
    });
  });

  it("still accepts a comma-separated list from a hand-edited compose", () => {
    expect(parseMCPEntry("thulu-github@pr_view,run_logs").tools).toEqual(["pr_view", "run_logs"]);
  });
});

describe("grantedTools", () => {
  it("does not tick what no scope grants", () => {
    expect(ticked(github, "thulu-github")).toEqual([
      "pr_create",
      "pr_view",
      "run_cancel",
      "run_logs",
    ]);
  });

  it("ticks the named extras on top of the scope", () => {
    expect(ticked(github, "thulu-github@write+run_dispatch")).toContain("run_dispatch");
  });

  it("uses the server's own default for a bare entry", () => {
    expect(ticked(api, "octopus-api")).toEqual(["task_list"]);
    expect(ticked(api, "octopus-api@write")).toEqual(["task_create", "task_list"]);
  });

  it("ticks only what an explicit list names", () => {
    expect(ticked(github, "thulu-github@pr_view")).toEqual(["pr_view"]);
  });
});

describe("entryForSelection round-trips", () => {
  it("keeps a bare entry bare", () => {
    expect(resaved(github, "thulu-github")).toBe("thulu-github");
    expect(resaved(api, "octopus-api")).toBe("octopus-api");
  });

  it("keeps a scope that isn't the default", () => {
    expect(resaved(github, "thulu-github@read")).toBe("thulu-github@read");
    expect(resaved(api, "octopus-api@write")).toBe("octopus-api@write");
  });

  it("collapses a redundant scope to the bare name", () => {
    expect(resaved(github, "thulu-github@write")).toBe("thulu-github");
  });

  it("keeps a deploy grant across a save", () => {
    expect(resaved(github, "thulu-github@write+run_dispatch")).toBe(
      "thulu-github@write+run_dispatch",
    );
    expect(resaved(api, "octopus-api@write+task_create_on_board")).toBe(
      "octopus-api@write+task_create_on_board",
    );
  });

  it("says a full-ladder selection with an extra as scope+extra, not as a frozen list", () => {
    const everything = ["pr_view", "run_logs", "pr_create", "run_cancel", "run_dispatch"];
    expect(formatMCPEntry(entryForSelection(github, "thulu-github", null, everything))).toBe(
      "thulu-github@write+run_dispatch",
    );
  });

  it("falls back to an explicit list when the selection drops a scoped tool", () => {
    expect(formatMCPEntry(entryForSelection(github, "thulu-github", "write", ["pr_view"]))).toBe(
      "thulu-github@pr_view",
    );
  });

  it("grants the whole of an unscoped server, and keeps it on save", () => {
    expect(ticked(unscoped, "thulu-browser")).toEqual(["get_text", "screenshot"]);
    expect(resaved(unscoped, "thulu-browser")).toBe("thulu-browser");
  });

  it("still honours an explicit list on an unscoped server", () => {
    expect(ticked(unscoped, "thulu-browser@screenshot")).toEqual(["screenshot"]);
    expect(resaved(unscoped, "thulu-browser@screenshot")).toBe("thulu-browser@screenshot");
  });

  it("leaves a server with no scope metadata exactly as it was", () => {
    expect(formatMCPEntry(entryForSelection(opaque, "ticket-reader", null, []))).toBe(
      "ticket-reader",
    );
    expect(formatMCPEntry(entryForSelection(undefined, "ticket-reader", "write", []))).toBe(
      "ticket-reader@write",
    );
  });
});

describe("a server whose bare grant the catalog cannot read", () => {
  it("knows the bare grant only from a descriptor", () => {
    expect(bareGrantKnown(api)).toBe(true);
    expect(bareGrantKnown(unscoped)).toBe(true);
    expect(bareGrantKnown(probed)).toBe(false);
    expect(bareGrantKnown(undefined)).toBe(false);
    expect(bareGrantKnown({ ...api, default_scope: undefined, source: "store" })).toBe(false);
  });

  it("does not render a bare entry as every tool granted", () => {
    expect(ticked(probed, "octopus-api")).toEqual([]);
  });

  it("still renders an explicit grant", () => {
    expect(ticked(probed, "octopus-api@task_list")).toEqual(["task_list"]);
    expect(ticked(probed, "octopus-api@write")).toEqual([
      "task_create",
      "task_delete",
      "task_list",
    ]);
  });

  it("keeps an untouched bare entry instead of dropping it on save", () => {
    const loaded = parseMCPEntry("octopus-api");
    const saved = entryForSave(probed, "octopus-api", loaded, [...grantedTools(probed, loaded)]);
    expect(saved && formatMCPEntry(saved)).toBe("octopus-api");
  });

  it("drops a server whose explicit tools were all unticked", () => {
    expect(entryForSave(probed, "octopus-api", parseMCPEntry("octopus-api@task_list"), [])).toBe(
      null,
    );
    expect(entryForSave(unscoped, "thulu-browser", parseMCPEntry("thulu-browser"), [])).toBe(null);
  });

  it("saves selecting everything as an explicit list, never the bare name", () => {
    const all = ["task_list", "task_create", "task_delete"];
    expect(formatMCPEntry(entryForSelection(probed, "octopus-api", null, all))).toBe(
      "octopus-api@task_list+task_create+task_delete",
    );
  });

  it("keeps a scope the entry was loaded with when everything stays ticked", () => {
    expect(resaved(probed, "octopus-api@write")).toBe("octopus-api@write");
  });

  it("saves a scoped descriptor with no default as its top scope, not bare", () => {
    const noDefault: ToolDef = { ...github, default_scope: undefined, source: "store" };
    const ladder = ["pr_view", "run_logs", "pr_create", "run_cancel"];
    expect(formatMCPEntry(entryForSelection(noDefault, "thulu-github", null, ladder))).toBe(
      "thulu-github@write",
    );
  });

  it("with the descriptor, saving every tool still writes the minimal grant", () => {
    const all = ["task_list", "task_create"];
    expect(formatMCPEntry(entryForSelection(api, "octopus-api", null, all))).toBe(
      "octopus-api@write",
    );
    expect(
      formatMCPEntry(
        entryForSelection(unscoped, "thulu-browser", null, ["screenshot", "get_text"]),
      ),
    ).toBe("thulu-browser");
  });
});

const registered: ToolDef = {
  name: "ticket-reader",
  source: "registered",
  tools: [{ name: "ticket_get" }, { name: "ticket_search" }, { name: "customer_get" }],
};

describe("a registered MCP", () => {
  it("reads a bare name as the whole server", () => {
    expect(bareGrantKnown(registered)).toBe(true);
    expect(ticked(registered, "ticket-reader")).toEqual([
      "customer_get",
      "ticket_get",
      "ticket_search",
    ]);
  });

  it("saves every tool ticked as the bare name, so a tool added later still arrives", () => {
    const all = ["ticket_get", "ticket_search", "customer_get"];
    expect(formatMCPEntry(entryForSelection(registered, "ticket-reader", null, all))).toBe(
      "ticket-reader",
    );
    expect(resaved(registered, "ticket-reader")).toBe("ticket-reader");
  });

  it("still writes a narrowed pick as a list", () => {
    expect(
      formatMCPEntry(entryForSelection(registered, "ticket-reader", null, ["ticket_get"])),
    ).toBe("ticket-reader@ticket_get");
  });
});

describe("serverTickSelection", () => {
  it("ticks what a scope reaches and leaves the named-only tools off", () => {
    expect(serverTickSelection(github)).toEqual(["pr_view", "run_logs", "pr_create", "run_cancel"]);
    expect(serverTickSelection(api)).toEqual(["task_list"]);
  });

  it("ticks every tool of a server that is one grant", () => {
    expect(serverTickSelection(unscoped)).toEqual(["screenshot", "get_text"]);
    expect(serverTickSelection(undefined)).toEqual([]);
  });

  it("saves as the bare name, which grants the default scope", () => {
    const saved = entryForSave(github, "thulu-github", undefined, serverTickSelection(github));
    expect(saved && formatMCPEntry(saved)).toBe("thulu-github");
    const savedApi = entryForSave(api, "octopus-api", undefined, serverTickSelection(api));
    expect(savedApi && formatMCPEntry(savedApi)).toBe("octopus-api");
  });
});

describe("allToolsToggle", () => {
  it("clears a partial pick instead of ticking everything", () => {
    expect(allToolsToggle(github, ["pr_view", "run_dispatch"])).toEqual([]);
  });

  it("from nothing, selects what a scope reaches and never the deploy tools", () => {
    expect(allToolsToggle(github, [])).toEqual(["pr_view", "run_logs", "pr_create", "run_cancel"]);
    expect(allToolsToggle(api, [])).toEqual(["task_list", "task_create"]);
  });
});

describe("allToolsState", () => {
  it("is full once every tool a scope reaches is ticked, by-name ones or not", () => {
    const scoped = ["pr_view", "run_logs", "pr_create", "run_cancel"];
    expect(allToolsState(github, scoped)).toBe("all");
    expect(allToolsState(github, [...scoped, "run_dispatch"])).toBe("all");
  });

  it("is mixed for a partial pick and empty for none", () => {
    expect(allToolsState(github, ["pr_view"])).toBe("some");
    expect(allToolsState(github, ["run_dispatch"])).toBe("some");
    expect(allToolsState(github, [])).toBe("none");
  });

  it("follows every tool on a server with no scopes", () => {
    expect(allToolsState(unscoped, ["screenshot", "get_text"])).toBe("all");
    expect(allToolsState(unscoped, ["screenshot"])).toBe("some");
  });
});

describe("stageGrant", () => {
  it("is null for a server the stage does not name", () => {
    expect(stageGrant(null)).toBeNull();
  });

  it("reads the Deploy template as read plus the named tools", () => {
    expect(stageGrant(parseMCPEntry("thulu-github@read+run_dispatch+run_watch"))).toEqual({
      kind: "scope",
      scope: "read",
      extras: ["run_dispatch", "run_watch"],
    });
  });

  it("tells a bare name from a list picked one by one", () => {
    expect(stageGrant(parseMCPEntry("thulu-github"))).toEqual({ kind: "bare" });
    expect(stageGrant(parseMCPEntry("thulu-browser@screenshot"))).toEqual({
      kind: "list",
      tools: ["screenshot"],
    });
  });
});

describe("toolGroups", () => {
  it("puts the ladder first and marks the groups that must be named", () => {
    const groups = toolGroups(github).map((g) => [g.scope, g.byName, g.tools.length]);
    expect(groups).toEqual([
      ["read", false, 2],
      ["write", false, 2],
      ["deploy", true, 2],
    ]);
  });

  it("keeps tools with no scope in one group at the end", () => {
    expect(toolGroups(unscoped).map((g) => [g.scope, g.byName])).toEqual([["", false]]);
  });
});

describe("bareGrantSummary", () => {
  it("names the default scope, the whole server, or says it cannot tell", () => {
    expect(bareGrantSummary(api)).toBe("read");
    expect(bareGrantSummary(github)).toBe("write");
    expect(bareGrantSummary(unscoped)).toBe("all");
    expect(bareGrantSummary(registered)).toBe("all");
    expect(bareGrantSummary(probed)).toBe("unknown");
  });
});
