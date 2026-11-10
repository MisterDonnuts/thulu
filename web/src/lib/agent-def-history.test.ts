// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { defChanges, defReason, defRows, renderDefHistory } from "./agent-def-history";
import { api } from "./api";

describe("defReason", () => {
  it("words each reason and reads an unknown one as a settings save", () => {
    const labels = ["settings", "workshop", "restore"].map(defReason);
    expect(new Set(labels).size).toBe(3);
    expect(defReason("weird")).toBe(defReason("settings"));
  });
});

describe("defRows", () => {
  it("drops empty fields", () => {
    expect(defRows({ name: "a" })).toEqual([]);
  });

  it("shows stages by their board name and per-stage maps sorted", () => {
    const names: Record<string, string> = { dev: "Desarrollo", qa: "Pruebas" };
    const rows = defRows(
      {
        name: "a",
        valid_stages: ["dev", "qa"],
        engine: "claude",
        skills_by_stage: { qa: ["review"], dev: ["style", "lint"], empty: [] },
      },
      (id) => names[id] ?? id,
    );
    const values = rows.map(([, v]) => v);
    expect(values).toContain("Desarrollo, Pruebas");
    expect(values).toContain("claude");
    expect(values).toContain("Desarrollo: style, lint · Pruebas: review");
  });
});

describe("defChanges", () => {
  const names: Record<string, string> = { dev: "En curso", qa: "Pruebas" };
  const stageName = (id: string) => names[id] ?? id;

  it("lists only the fields that differ, with stages by name", () => {
    const changes = defChanges(
      { name: "a", engine: "claude", skills_by_stage: { dev: ["git-flow"] } },
      { name: "a", engine: "claude", skills_by_stage: { dev: ["git-flow", "lint"] } },
      stageName,
    );
    expect(changes).toHaveLength(1);
    expect(changes[0].before).toBe("En curso: git-flow");
    expect(changes[0].after).toBe("En curso: git-flow, lint");
  });

  it("reads a field that appeared or went away as empty on the other side", () => {
    const changes = defChanges({ name: "a" }, { name: "a", valid_stages: ["qa"] }, stageName);
    expect(changes.map((c) => [c.before, c.after])).toEqual([["", "Pruebas"]]);
  });

  it("is empty for the same configuration", () => {
    const def = { name: "a", valid_stages: ["dev"], mcp_by_stage: { dev: ["thulu-github"] } };
    expect(defChanges(def, { ...def })).toEqual([]);
  });
});

describe("renderDefHistory", () => {
  afterEach(() => vi.restoreAllMocks());

  it("draws Back and Restore with the shared button classes", async () => {
    vi.spyOn(api, "get").mockResolvedValue([
      {
        id: 1,
        name: "a",
        reason: "settings",
        created_at: "2026-10-01T00:00:00Z",
        definition: { name: "a" },
      },
    ]);
    const host = document.createElement("div");
    const noop = () => undefined;
    await renderDefHistory({ host, agent: "a", onBack: noop, onRestored: noop, toast: noop });
    expect(host.querySelector("[data-rev-back]")?.className).toBe("btn btn-sm");
    host.querySelector<HTMLButtonElement>('[data-rev="0"]')?.click();
    expect(host.querySelector("[data-rev-restore]")?.className).toBe("btn btn-primary btn-sm");
  });

  it("compares the newest entry with what is saved now", async () => {
    vi.spyOn(api, "get").mockResolvedValue([
      {
        id: 2,
        name: "a",
        reason: "workshop",
        created_at: "2026-10-02T00:00:00Z",
        definition: { name: "a", model: "sonnet" },
      },
      {
        id: 1,
        name: "a",
        reason: "settings",
        created_at: "2026-10-01T00:00:00Z",
        definition: { name: "a", model: "haiku" },
      },
    ]);
    const host = document.createElement("div");
    const noop = () => undefined;
    await renderDefHistory({
      host,
      agent: "a",
      current: { name: "a", model: "opus" },
      onBack: noop,
      onRestored: noop,
      toast: noop,
    });
    const changed = () =>
      Array.from(host.querySelectorAll(".def-changes dd")).map((d) => d.textContent);
    host.querySelector<HTMLButtonElement>('[data-rev="0"]')?.click();
    expect(changed()).toEqual([expect.stringMatching(/sonnet.*opus/)]);
    host.querySelector<HTMLButtonElement>('[data-rev="1"]')?.click();
    expect(changed()).toEqual([expect.stringMatching(/haiku.*sonnet/)]);
  });
});
