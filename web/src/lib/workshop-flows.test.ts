import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import type { Stage } from "./stages";
import {
  applyWorkshop,
  entryTitle,
  filterEntries,
  flowRevisionBadgeHTML,
  flowTypeRows,
  flowUpdateFor,
  flowWrites,
  offersDefaultBoard,
  stageChangeCounts,
  stageChangeTone,
  updatesOf,
  type FlowView,
  type Plan,
  type PlanItem,
  type WorkshopEntry,
} from "./workshop";

const flowEntry = (over: Partial<WorkshopEntry> = {}): WorkshopEntry => ({
  kind: "flow",
  name: "thulu",
  label: "Thulu flow",
  description: "Backlog to production",
  tags: ["flow"],
  revision: 2,
  status: "available",
  ...over,
});

const item = (over: Partial<PlanItem>): PlanItem => ({
  kind: "flow",
  name: "thulu",
  action: "install",
  resolution: "install",
  options: ["install", "skip"],
  target: "thulu",
  ...over,
});

const plan = (items: PlanItem[]): Plan => ({
  source: "workshop",
  items,
  notices: [],
  blocked: false,
  writes: 0,
});

const stage = (id: string, label: string): Stage =>
  ({ id, label, color: "", position: 0 }) as unknown as Stage;

afterEach(() => vi.restoreAllMocks());

describe("flow entries", () => {
  it("go by their label and match a search on it", () => {
    const e = flowEntry();
    expect(entryTitle(e)).toBe("Thulu flow");
    expect(entryTitle({ name: "reviewer" })).toBe("reviewer");
    expect(filterEntries([e], "flow", "thulu flow").map((x) => x.name)).toEqual(["thulu"]);
    expect(filterEntries([e], "agent", "thulu")).toEqual([]);
  });

  it("count as updates like agents do", () => {
    const list = [flowEntry({ status: "update" }), flowEntry({ name: "other" })];
    expect([...updatesOf(list, "flow")]).toEqual(["thulu"]);
  });
});

describe("flow plans", () => {
  it("offer the default board only to a flow that creates one", () => {
    expect(offersDefaultBoard(plan([item({})]))).toBe(true);
    expect(offersDefaultBoard(plan([item({ resolution: "copy" })]))).toBe(true);
    expect(offersDefaultBoard(plan([item({ action: "update", resolution: "update" })]))).toBe(
      false,
    );
    expect(offersDefaultBoard(plan([item({ resolution: "skip" })]))).toBe(false);
    expect(offersDefaultBoard(plan([item({ kind: "agent", name: "a" })]))).toBe(false);
  });

  it("list the boards the apply writes", () => {
    const p = plan([
      item({ target: "thulu_acme" }),
      item({ kind: "agent", name: "thulu-developer" }),
      item({ name: "kept", resolution: "keep" }),
    ]);
    expect(flowWrites(p).map((it) => it.target)).toEqual(["thulu_acme"]);
  });

  it("count what an update does to the columns", () => {
    const it = item({
      action: "update",
      resolution: "update",
      stages: [
        { id: "backlog", label: "Backlog", change: "same" },
        { id: "qa", label: "QA", change: "add" },
        { id: "in_progress", label: "Dev", agent: "dev", change: "change" },
        { id: "ship", label: "Ship", change: "remove" },
      ],
    });
    expect(stageChangeCounts(it)).toEqual({ add: 1, change: 1, same: 1, remove: 1 });
    expect(stageChangeCounts(item({}))).toEqual({ add: 0, change: 0, same: 0, remove: 0 });
    expect(stageChangeTone("remove")).toBe("danger");
    expect(stageChangeTone("add")).toBe("ok");
  });

  it("send flow_default only when asked", async () => {
    const post = vi.spyOn(api, "post").mockResolvedValue({});
    const input = { items: [{ kind: "flow" as const, name: "thulu" }] };
    const choices = { resolutions: {}, names: {} };
    await applyWorkshop(input, choices);
    await applyWorkshop(input, choices, true);
    expect(post.mock.calls[0][1]).not.toHaveProperty("flow_default");
    expect(post.mock.calls[1][1]).toMatchObject({ flow_default: true });
  });
});

describe("flowTypeRows", () => {
  const view: FlowView = {
    max_kick_backs: 2,
    stages: [
      stage("backlog", "Backlog"),
      stage("in_progress", "Development"),
      stage("testing", "Agentic testing"),
      stage("release", "Release"),
      stage("done", "Done"),
    ],
    types: [
      { id: "chore", label: "Chore", position: 10, skips: ["testing", "in_progress"] },
      {
        id: "release",
        label: "Release",
        position: 20,
        skips: [],
        agents: { release: "thulu-release", in_progress: "" },
      },
    ],
  };

  it("names skipped columns and per-type agents in board order", () => {
    expect(flowTypeRows(view)).toEqual([
      { id: "chore", label: "Chore", skips: ["Development", "Agentic testing"], agents: [] },
      {
        id: "release",
        label: "Release",
        skips: [],
        agents: [
          { stage: "Development", agent: "" },
          { stage: "Release", agent: "thulu-release" },
        ],
      },
    ]);
  });

  it("reads a flow without types", () => {
    expect(flowTypeRows({ ...view, types: null })).toEqual([]);
  });
});

describe("flowRevisionBadgeHTML", () => {
  const entries = [flowEntry({ revision: 2 })];
  it("tells an update from a conflict on an edited board", () => {
    const update = flowRevisionBadgeHTML({ flow: { name: "thulu", revision: 1 } }, entries);
    expect(update).toContain("badge-accent");
    expect(update).not.toContain("badge-warn");
    const conflict = flowRevisionBadgeHTML(
      { flow: { name: "thulu", revision: 1, edited: true } },
      entries,
    );
    expect(conflict).toContain("badge-warn");
    expect(conflict).not.toBe(update);
  });
  it("draws nothing while the board is on the catalog revision, edited or not", () => {
    expect(
      flowRevisionBadgeHTML({ flow: { name: "thulu", revision: 2, edited: true } }, entries),
    ).toBe("");
  });
});

describe("flowUpdateFor", () => {
  const entries = [flowEntry({ revision: 2 })];
  it("flags a board behind the catalog revision", () => {
    expect(flowUpdateFor({ flow: { name: "thulu", revision: 1 } }, entries)?.revision).toBe(2);
  });
  it("leaves current and hand-made boards alone", () => {
    expect(flowUpdateFor({ flow: { name: "thulu", revision: 2 } }, entries)).toBeUndefined();
    expect(flowUpdateFor({}, entries)).toBeUndefined();
    expect(flowUpdateFor({ flow: { name: "gone", revision: 1 } }, entries)).toBeUndefined();
  });
});
