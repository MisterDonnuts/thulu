// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  actionsFor,
  isUnchecked,
  isUp,
  mcpColumns,
  mcpRowHTML,
  sourceOf,
  upCounts,
  upTone,
} from "./mcp-actions";

describe("sourceOf", () => {
  it("only trusts the exact string the daemon sends", () => {
    expect(sourceOf({ source: "registered" })).toBe("registered");
    expect(sourceOf({ source: "env" })).toBe("env");
    expect(sourceOf({ source: "runtime" })).toBe("runtime");
    expect(sourceOf({})).toBe("runtime");
    expect(sourceOf({ source: "Registered" })).toBe("runtime");
    expect(sourceOf({ source: "ENV" })).toBe("runtime");
  });
});

describe("actionsFor", () => {
  it("gives a registered MCP no lifecycle buttons whatever its status", () => {
    expect(actionsFor({ source: "registered", status: "running" })).toEqual(["edit", "delete"]);
    expect(actionsFor({ source: "registered" })).toEqual(["edit", "delete"]);
  });

  it("gives an env MCP no buttons at all: the daemon's deployment config owns it", () => {
    expect(actionsFor({ source: "env", status: "configured" })).toEqual([]);
    expect(actionsFor({ source: "env", status: "configured", health: "unhealthy" })).toEqual([]);
  });

  it("offers restart/stop while the container is up", () => {
    for (const status of ["running", "restarting", "paused"]) {
      expect(actionsFor({ status })).toEqual(["restart", "stop"]);
    }
  });

  it("offers start/restart once it is down", () => {
    for (const status of ["exited", "created", "dead"]) {
      expect(actionsFor({ status })).toEqual(["start", "restart"]);
    }
  });

  it("offers only start when there is no container to act on", () => {
    expect(actionsFor({ status: "missing" })).toEqual(["start"]);
  });

  it("falls back to all three for a status it does not know", () => {
    expect(actionsFor({ status: "sleepwalking" })).toEqual(["start", "restart", "stop"]);
    expect(actionsFor({})).toEqual(["start", "restart", "stop"]);
  });
});

describe("isUp", () => {
  it("counts a running container, whatever its source", () => {
    expect(isUp({ source: "runtime", status: "running" })).toBe(true);
    expect(isUp({ source: "registered", status: "running" })).toBe(true);
    expect(isUp({ source: "runtime", status: "exited" })).toBe(false);
  });

  it("counts a URL-only server only when its check passed", () => {
    expect(isUp({ source: "env", status: "configured", health: "healthy" })).toBe(true);
    expect(isUp({ source: "registered", status: "external", health: "healthy" })).toBe(true);
    expect(isUp({ source: "env", status: "configured", health: "unhealthy" })).toBe(false);
    expect(isUp({ source: "env", status: "configured" })).toBe(false);
  });
});

describe("isUnchecked", () => {
  it("is a URL-only server with no health", () => {
    expect(isUnchecked({ source: "env", status: "configured" })).toBe(true);
    expect(isUnchecked({ source: "registered", status: "external" })).toBe(true);
  });

  it("is not a checked server, nor a container", () => {
    expect(isUnchecked({ source: "env", status: "configured", health: "unhealthy" })).toBe(false);
    expect(isUnchecked({ source: "runtime", status: "exited" })).toBe(false);
  });
});

describe("upCounts", () => {
  it("leaves unchecked servers out of both numbers", () => {
    expect(
      upCounts([
        { source: "env", status: "configured" },
        { source: "registered", status: "external" },
        { source: "runtime", status: "running" },
        { source: "runtime", status: "exited" },
      ]),
    ).toEqual({ up: 1, total: 2, unchecked: 2 });
  });

  it("counts a failed check as down", () => {
    expect(upCounts([{ source: "env", status: "configured", health: "unhealthy" }])).toEqual({
      up: 0,
      total: 1,
      unchecked: 0,
    });
  });
});

describe("upTone", () => {
  it("is ok while one server is up", () => {
    expect(upTone({ up: 1, total: 3 })).toBe("ok");
  });
  it("is danger when every checked server is down", () => {
    expect(upTone({ up: 0, total: 2 })).toBe("danger");
  });
  it("is grey with nothing checked", () => {
    expect(upTone({ up: 0, total: 0 })).toBe("neutral");
  });
});

describe("mcpColumns and mcpRowHTML", () => {
  it("names seven columns, the buttons' only for screen readers", () => {
    const cols = mcpColumns();
    expect(cols).toHaveLength(7);
    expect(cols.map((c) => c.label)).toEqual([
      "Name",
      "Source",
      "Endpoint",
      "Status",
      "Health",
      "Uptime",
      "Actions",
    ]);
    expect(cols[6]).toMatchObject({ hideLabel: true, width: "shrink" });
  });

  it("writes one cell per column with the hooks the updater fills", () => {
    const tpl = document.createElement("tbody");
    tpl.innerHTML = mcpRowHTML();
    const tr = tpl.firstElementChild as HTMLTableRowElement;
    expect(tr.tagName).toBe("TR");
    expect(tr.cells).toHaveLength(7);
    for (const hook of [
      ".mcp-name",
      ".source-cell",
      ".mcp-endpoint",
      ".status-cell",
      ".health-cell",
    ])
      expect(tr.querySelector(hook)).not.toBeNull();
    expect(tr.cells[5].classList.contains("mcp-uptime")).toBe(true);
    expect(tr.cells[5].classList.contains("mono")).toBe(true);
    expect(tr.cells[6].querySelector(".cell-actions.mcp-actions")).not.toBeNull();
    expect(tr.cells[1].dataset.label).toBe("Source");
    expect(tr.cells[6].hasAttribute("data-label")).toBe(false);
  });
});
