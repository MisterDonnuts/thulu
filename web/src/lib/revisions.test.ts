// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "./api";
import { renderHistory, restoreMode, revisionAction, type Revision } from "./revisions";

const rev = (over: Partial<Revision>): Revision => ({
  id: 1,
  kind: "agent",
  owner: "developer",
  path: "prompts/main.md",
  action: "updated",
  created_at: "2026-10-05T10:00:00Z",
  ...over,
});

describe("restoreMode", () => {
  it("resets to what a revision wrote", () => {
    expect(restoreMode(rev({ sha256: "abc" }))).toBe("reset");
  });

  it("can only undo a deletion, which wrote nothing", () => {
    expect(restoreMode(rev({ action: "deleted", prev_sha256: "abc" }))).toBe("revert");
  });
});

describe("revisionAction", () => {
  it("labels each action and falls back to an edit", () => {
    const labels = ["created", "updated", "deleted", "restored", "weird"].map((action) =>
      revisionAction(rev({ action })),
    );
    expect(new Set(labels.slice(0, 4)).size).toBe(4);
    expect(labels[4]).toBe(labels[1]);
  });
});

describe("renderHistory", () => {
  afterEach(() => vi.restoreAllMocks());

  // /skills/ defines no `.btn.primary` of its own: anything but the shared button classes renders
  // Back native and Restore neutral.
  it("draws Back and Restore with the shared button classes", async () => {
    vi.spyOn(api, "get").mockResolvedValue([rev({ id: 2, sha256: "b" }), rev({ sha256: "a" })]);
    vi.spyOn(api, "getText").mockResolvedValue("body");
    const host = document.createElement("div");
    const noop = () => undefined;
    await renderHistory({
      host,
      base: "/skills/notes",
      path: "SKILL.md",
      onBack: noop,
      onRestored: noop,
      toast: noop,
    });
    expect(host.querySelector("[data-rev-back]")?.className).toBe("btn btn-sm");
    host.querySelector<HTMLButtonElement>('[data-rev="1"]')?.click();
    await vi.waitFor(() => expect(host.querySelector("[data-rev-restore]")).not.toBeNull());
    expect(host.querySelector("[data-rev-restore]")?.className).toBe("btn btn-primary btn-sm");
  });
});
