import { describe, expect, it } from "vitest";
import {
  COL_W_DEFAULT,
  COL_W_MAX,
  COL_W_MIN,
  clampColumnWidth,
  columnCount,
  columnEmptyLabel,
  columnShellHTML,
  matchesTaskSearch,
  matchesTypeFilter,
  parseColumnWidths,
  pickBoardID,
  columnStripLabel,
  stageTabsHTML,
  typeFilterItemsHTML,
  typeFilterLabel,
  typeIconHTML,
  typeTagHTML,
  wipStatsHTML,
  wipCounts,
  withColumnWidth,
} from "./board";
import type { Stage, TaskType } from "./stages";

const stage = (id: string, extra: Partial<Stage> = {}): Stage => ({
  id,
  label: id,
  color: "#88b4ff",
  position: 0,
  auto_advance: false,
  blocked_by_flags: false,
  initial: false,
  terminal: false,
  ingest_flags: false,
  push_branch: false,
  open_pr: false,
  wip_group: "",
  virtual: false,
  ...extra,
});

const boards = [{ id: "dev" }, { id: "ops" }];

describe("pickBoardID", () => {
  it("prefers the board in the URL", () => {
    expect(pickBoardID(boards, { fromURL: "ops", remembered: "dev", fallback: "dev" })).toBe("ops");
  });

  it("falls back to the remembered board when the URL has none", () => {
    expect(pickBoardID(boards, { fromURL: null, remembered: "ops", fallback: "dev" })).toBe("ops");
  });

  it("ignores a board that no longer exists", () => {
    expect(pickBoardID(boards, { fromURL: "gone", remembered: "also-gone", fallback: "dev" })).toBe(
      "dev",
    );
  });

  it("takes the first board when there is no default", () => {
    expect(pickBoardID(boards, {})).toBe("dev");
  });

  it("has something to say even with no boards at all", () => {
    expect(pickBoardID([], {})).toBe("default");
  });
});

describe("matchesTypeFilter", () => {
  it("shows everything when nothing is selected", () => {
    expect(matchesTypeFilter(new Set(), "bug")).toBe(true);
    expect(matchesTypeFilter(new Set(), undefined)).toBe(true);
  });

  it("matches the untyped bucket on the empty string", () => {
    expect(matchesTypeFilter(new Set([""]), undefined)).toBe(true);
    expect(matchesTypeFilter(new Set([""]), "bug")).toBe(false);
  });

  it("matches a selected type", () => {
    expect(matchesTypeFilter(new Set(["bug", "chore"]), "chore")).toBe(true);
  });
});

describe("matchesTaskSearch", () => {
  const fields = { title: "Fix the poller", projectNames: ["alpha", "beta"], agent: "dev" };

  it("matches everything on an empty query", () => {
    expect(matchesTaskSearch("", fields)).toBe(true);
  });

  it("matches the title regardless of case", () => {
    expect(matchesTaskSearch("POLLER", fields)).toBe(true);
  });

  it("matches a linked repo's name", () => {
    expect(matchesTaskSearch("beta", fields)).toBe(true);
  });

  it("matches the agent on the current stage", () => {
    expect(matchesTaskSearch("dev", fields)).toBe(true);
  });

  it("does not match the body, which the list payload does not carry", () => {
    expect(matchesTaskSearch("acceptance", fields)).toBe(false);
  });

  it("survives a task with no agent assigned", () => {
    expect(matchesTaskSearch("dev", { title: "x", projectNames: [] })).toBe(false);
  });

  it("matches the short id the card shows, with or without #", () => {
    const withID = { ...fields, id: "c82712f0aa11" };
    expect(matchesTaskSearch("#c82712", withID)).toBe(true);
    expect(matchesTaskSearch("C827", withID)).toBe(true);
    expect(matchesTaskSearch("#c8", withID)).toBe(true);
  });

  it("does not match an id by a short run of letters inside it", () => {
    const withID = { ...fields, id: "c82712f0aa11" };
    expect(matchesTaskSearch("c8", withID)).toBe(false);
    expect(matchesTaskSearch("2712", withID)).toBe(false);
  });
});

describe("wipCounts", () => {
  const stages: Record<string, Stage> = {
    backlog: stage("backlog", { initial: true }),
    build: stage("build", { wip_group: "work" }),
    review: stage("review", { wip_group: "review" }),
    done: stage("done", { terminal: true }),
  };
  const stageOf = (id: string) => stages[id];

  it("counts open tasks and buckets them by wip_group", () => {
    const tasks = [
      { stage: "backlog" },
      { stage: "build" },
      { stage: "build" },
      { stage: "review" },
      { stage: "done" },
    ];
    expect(wipCounts(tasks, stageOf)).toEqual({ open: 4, inProgress: 2, needsReview: 1 });
  });

  it("counts a task in an unknown stage as open and nothing else", () => {
    expect(wipCounts([{ stage: "mystery" }], stageOf)).toEqual({
      open: 1,
      inProgress: 0,
      needsReview: 0,
    });
  });
});

describe("column counters", () => {
  it("hides the total while nothing is filtered out", () => {
    expect(columnCount(3, 3)).toBe("3");
  });

  it("shows the total once a filter hides something", () => {
    expect(columnCount(1, 4)).toBe("1/4");
  });

  it("tells an empty stage apart from an emptied one", () => {
    expect(columnEmptyLabel(0, "Review")).toBe("Nothing in Review");
    expect(columnEmptyLabel(7, "Review")).toBe("No matches");
  });

  it("names the folded column and its count on the strip", () => {
    expect(columnStripLabel("Done", 2)).toBe("Show Done, 2 tasks");
  });
});

describe("column width", () => {
  it("clamps and rounds", () => {
    expect(clampColumnWidth(10)).toBe(COL_W_MIN);
    expect(clampColumnWidth(9999)).toBe(COL_W_MAX);
    expect(clampColumnWidth(248.6)).toBe(249);
  });

  it("reads junk out of storage as no overrides", () => {
    expect(parseColumnWidths(null)).toEqual({});
    expect(parseColumnWidths("not json")).toEqual({});
    expect(parseColumnWidths("null")).toEqual({});
    expect(parseColumnWidths('{"build":300}')).toEqual({ build: 300 });
  });

  it("stores an override and clears it rather than storing the default", () => {
    const one = withColumnWidth({}, "build", 300);
    expect(one).toEqual({ build: 300 });
    expect(withColumnWidth(one, "build", null)).toEqual({});
    expect(one).toEqual({ build: 300 });
    expect(COL_W_DEFAULT).toBeGreaterThan(COL_W_MIN);
  });
});

describe("markup", () => {
  it("escapes a stage's label and colour into the column head", () => {
    const html = columnShellHTML(stage("build", { label: "<b>B&B</b>", color: "#fff" }));
    expect(html).toContain("&lt;b&gt;B&amp;B&lt;/b&gt;");
    expect(html).toContain('data-add-stage="build"');
    expect(html).toContain('<span class="count col-count">0</span>');
    expect(html).toContain('<span class="count col-strip-count">0</span>');
  });

  it("puts a Dot in the column's colour before its name, in the head and the strip", () => {
    const html = columnShellHTML(stage("build", { label: "Build" }));
    const dot =
      '<span class="dot" aria-hidden="true" style="--dot-color:var(--col-color,var(--fg-4))"></span>';
    expect(html.split(dot)).toHaveLength(3);
    expect(html).toMatch(/<span class="dot"[^>]*><\/span>\s*<span class="col-title">Build<\/span>/);
  });

  const types: TaskType[] = [
    { id: "bug", label: "Bug", icon: "bug", color: "#d76b6b" } as TaskType,
    { id: "chore", label: "Chore" } as TaskType,
  ];

  it("draws a type's glyph, and a dot for a type without one", () => {
    expect(typeIconHTML(types, "bug")).toContain('href="#i-tt-bug"');
    expect(typeIconHTML(types, "chore")).toContain('class="tt-dot"');
  });

  it("names a type as an inline tag in its colour", () => {
    const bug = typeTagHTML(types, "bug");
    expect(bug).toMatch(/^<span class="tag tag-tint tag-inline" style="--tag-color:#d76b6b">/);
    expect(bug).toContain('href="#i-tt-bug"');
    expect(bug).toContain('<span class="tag-label">Bug</span>');
    const chore = typeTagHTML(types, "chore");
    expect(chore).toContain('class="tag-dot"');
    expect(chore).not.toContain("style=");
    expect(typeTagHTML(types, "gone")).toBe("");
  });

  it("draws nothing for a type the board has dropped", () => {
    expect(typeIconHTML(types, "gone")).toBe("");
    expect(typeIconHTML(types, undefined)).toBe("");
  });

  it("lists an Untyped item after the board's types and checks the active ones", () => {
    const html = typeFilterItemsHTML(types, ["chore"]);
    expect(html.match(/menu-item tf-item/g)).toHaveLength(3);
    expect(html).toContain(">Untyped<");
    expect(html).toMatch(
      /role="menuitemcheckbox" aria-checked="true"[^>]*data-keep-open=""[^>]*data-type="chore"/,
    );
    expect(html).toMatch(/aria-checked="false"[^>]*data-type="bug"/);
  });

  it("labels the Type button by what is picked", () => {
    expect(typeFilterLabel(types, [])).toBe("Type: All");
    expect(typeFilterLabel(types, ["bug"])).toBe("Type: Bug");
    expect(typeFilterLabel(types, [""])).toBe("Type: Untyped");
    expect(typeFilterLabel(types, ["bug", "chore"])).toBe("Type: 2");
  });

  it("puts the new-task button after the card list, inside the scroller", () => {
    const html = columnShellHTML(stage("build"));
    expect(html.indexOf("col-list")).toBeLessThan(html.indexOf("col-add"));
    expect(html.indexOf("col-add")).toBeLessThan(html.indexOf("col-strip"));
    expect(html).toContain("data-col-fold");
  });

  it("draws the fold and new-task controls as shared buttons", () => {
    const html = columnShellHTML(stage("build"));
    const fold = html.match(/<button[^>]*data-col-fold[^>]*>[\s\S]*?<\/button>/)?.[0] ?? "";
    expect(fold).toContain('class="icon-btn icon-btn-ghost col-fold"');
    expect(fold).toContain('aria-label="Fold build"');
    expect(fold).toContain('href="#i-chevron-left"');
    const add = html.match(/<button[^>]*col-add[^>]*>[\s\S]*?<\/button>/)?.[0] ?? "";
    expect(add).toContain('class="btn btn-ghost btn-sm col-add"');
    expect(add).toContain('data-add-stage="build"');
    expect(add).toContain('href="#i-plus"');
    expect(add).toContain("New task");
  });

  it("writes the stats as plain text", () => {
    const html = wipStatsHTML({ open: 11, inProgress: 3, needsReview: 2 });
    expect(html).toContain('<span class="wip-num">11</span> open');
    expect(html).toContain("to review");
    expect(html).not.toContain("<button");
  });

  it("draws one tab per stage with its count and selects the active one", () => {
    const html = stageTabsHTML(
      [
        { id: "a", label: "Backlog", color: "#123456" },
        { id: "b", label: "Ready", color: "red;x" },
      ],
      { a: 4 },
      "b",
    );
    expect(html).toContain('class="tabs tabs-pill" role="tablist"');
    expect(html).toMatch(/id="stage-tab-a" aria-selected="false" tabindex="-1" data-tab="a"/);
    expect(html).toMatch(/id="stage-tab-b" aria-selected="true" data-tab="b"/);
    expect(html).toMatch(/id="stage-tab-a-count"[^>]*>4</);
    expect(html).toMatch(/id="stage-tab-b-count"[^>]*hidden[^>]*>0</);
    expect(html).toContain('style="--dot-color:#123456"');
    expect(html).not.toContain("red;x");
  });
});
