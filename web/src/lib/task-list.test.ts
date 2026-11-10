// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  ALL_GROUP,
  LIST_COLUMNS,
  TASKS_VIEW_CACHE,
  ariaSort,
  cacheTasksView,
  defaultTasksView,
  groupFolded,
  listGroupHeadHTML,
  listGroups,
  listHeadHTML,
  listRowCellsHTML,
  listRowClasses,
  listRows,
  neighborRow,
  nextSort,
  parseTasksView,
  readCachedTasksView,
  rowKey,
  sameTasksView,
  sortTasks,
  statusRank,
  type ListRowView,
  type SortableTask,
  type TasksViewPref,
} from "./task-list";

const stages = [
  { id: "todo", label: "To do", color: "#888888" },
  { id: "doing", label: "Doing", color: "#3366ff" },
  { id: "done", label: "Done", color: "#22aa55", terminal: true },
];
const ctx = {
  stageIndex: (id: string) => stages.findIndex((s) => s.id === id),
  isTerminal: (id: string) => !!stages.find((s) => s.id === id)?.terminal,
};
const task = (id: string, o: Partial<SortableTask> = {}): SortableTask => ({
  id,
  title: id,
  stage: "todo",
  ...o,
});
const ids = (xs: Array<{ id: string }>) => xs.map((x) => x.id);

describe("the person's choice", () => {
  it("opens on the board, with the list flat, in the board's order", () => {
    expect(defaultTasksView()).toEqual({ layout: "board", group: "none", sort: "", dir: "asc" });
  });

  it("keeps each valid field and drops each unknown one on its own", () => {
    expect(parseTasksView({ layout: "list", group: "kanban", sort: "cost", dir: "desc" })).toEqual({
      layout: "list",
      group: "none",
      sort: "",
      dir: "desc",
    });
    for (const junk of [null, "list", 3, ["list"], { layout: 1 }])
      expect(parseTasksView(junk)).toEqual(defaultTasksView());
  });

  it("round-trips the browser copy and survives a storage that throws", () => {
    const store = new Map<string, string>();
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
    };
    const v: TasksViewPref = { layout: "list", group: "none", sort: "updated", dir: "desc" };
    cacheTasksView(storage, v);
    expect(store.has(TASKS_VIEW_CACHE)).toBe(true);
    expect(readCachedTasksView(storage)).toEqual(v);

    store.set(TASKS_VIEW_CACHE, "{not json");
    expect(readCachedTasksView(storage)).toEqual(defaultTasksView());
    const broken = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
    };
    expect(readCachedTasksView(broken)).toEqual(defaultTasksView());
    expect(() => cacheTasksView(broken, v)).not.toThrow();
    expect(readCachedTasksView(null)).toEqual(defaultTasksView());
  });

  it("compares every field", () => {
    const a = defaultTasksView();
    expect(sameTasksView(a, { ...a })).toBe(true);
    for (const patch of [
      { layout: "list" },
      { group: "stage" },
      { sort: "title" },
      { dir: "desc" },
    ])
      expect(sameTasksView(a, { ...a, ...patch } as TasksViewPref)).toBe(false);
  });
});

describe("sorting", () => {
  it("first sorts the way the column reads, then reverses, then goes back to the board", () => {
    let s = nextSort({ sort: "", dir: "asc" }, "title");
    expect(s).toEqual({ sort: "title", dir: "asc" });
    s = nextSort(s, "title");
    expect(s).toEqual({ sort: "title", dir: "desc" });
    expect(nextSort(s, "title")).toEqual({ sort: "", dir: "asc" });

    s = nextSort({ sort: "title", dir: "desc" }, "updated");
    expect(s).toEqual({ sort: "updated", dir: "desc" });
    expect(nextSort(s, "updated")).toEqual({ sort: "updated", dir: "asc" });
    expect(nextSort({ sort: "", dir: "asc" }, "priority")).toEqual({
      sort: "priority",
      dir: "desc",
    });
  });

  it("names the sort for aria-sort", () => {
    expect(ariaSort({ sort: "title", dir: "asc" }, "title")).toBe("ascending");
    expect(ariaSort({ sort: "title", dir: "desc" }, "title")).toBe("descending");
    expect(ariaSort({ sort: "title", dir: "asc" }, "stage")).toBe("none");
  });

  it("keeps the board's order when nothing is sorted", () => {
    const rows = [task("b"), task("a")];
    const out = sortTasks(rows, { sort: "", dir: "asc" }, ctx);
    expect(ids(out)).toEqual(["b", "a"]);
    expect(out).not.toBe(rows);
  });

  it("sorts titles the way people read numbers and case", () => {
    const rows = [task("1", { title: "Item 10" }), task("2", { title: "item 9" })];
    expect(ids(sortTasks(rows, { sort: "title", dir: "asc" }, ctx))).toEqual(["2", "1"]);
    expect(ids(sortTasks(rows, { sort: "title", dir: "desc" }, ctx))).toEqual(["1", "2"]);
  });

  it("puts urgent first, unprioritised last, and keeps ties in board order", () => {
    const rows = [
      task("none"),
      task("low", { priority: "low" }),
      task("urgent", { priority: "urgent" }),
      task("high-1", { priority: "high" }),
      task("high-2", { priority: "high" }),
    ];
    expect(ids(sortTasks(rows, { sort: "priority", dir: "desc" }, ctx))).toEqual([
      "urgent",
      "high-1",
      "high-2",
      "low",
      "none",
    ]);
  });

  it("puts the newest first and a task with no date last", () => {
    const rows = [
      task("old", { updated_at: "2026-01-01T00:00:00Z" }),
      task("undated"),
      task("created-only", { created_at: "2026-06-01T00:00:00Z" }),
      task("new", { updated_at: "2026-11-01T00:00:00Z" }),
    ];
    expect(ids(sortTasks(rows, { sort: "updated", dir: "desc" }, ctx))).toEqual([
      "new",
      "created-only",
      "old",
      "undated",
    ]);
  });

  it("follows the board's flow when sorted by stage", () => {
    const rows = [task("d", { stage: "done" }), task("t"), task("g", { stage: "doing" })];
    expect(ids(sortTasks(rows, { sort: "stage", dir: "asc" }, ctx))).toEqual(["t", "g", "d"]);
  });

  it("ranks what waits on a person before what works, queues, idles and is done", () => {
    expect(statusRank(task("q", { open_questions: 1 }), false)).toBe(0);
    expect(statusRank(task("f", { status: "failed" }), false)).toBe(0);
    expect(statusRank(task("r", { status: "running" }), false)).toBe(1);
    expect(statusRank(task("q", { status: "queued" }), false)).toBe(2);
    expect(statusRank(task("i"), false)).toBe(3);
    expect(statusRank(task("d", { status: "failed" }), true)).toBe(4);
    const rows = [
      task("idle"),
      task("done", { stage: "done" }),
      task("run", { status: "running" }),
      task("ask", { status: "awaiting_input" }),
    ];
    expect(ids(sortTasks(rows, { sort: "status", dir: "asc" }, ctx))).toEqual([
      "ask",
      "run",
      "idle",
      "done",
    ]);
  });
});

describe("groups", () => {
  const cards = [
    task("t1"),
    task("g1", { stage: "doing" }),
    task("t2", { priority: "urgent" }),
    task("x", { stage: "gone" }),
  ];

  it("makes one group per stage that has rows, in the flow's order", () => {
    const groups = listGroups(
      cards,
      stages,
      { group: "stage", sort: "", dir: "asc" },
      () => true,
      ctx,
    );
    expect(groups.map((g) => g.id)).toEqual(["todo", "doing"]);
    expect(ids(groups[0].tasks)).toEqual(["t1", "t2"]);
  });

  it("sorts inside each group and leaves out what the filter hides", () => {
    const groups = listGroups(
      cards,
      stages,
      { group: "stage", sort: "priority", dir: "desc" },
      (x) => x.id !== "g1",
      ctx,
    );
    expect(groups.map((g) => g.id)).toEqual(["todo"]);
    expect(ids(groups[0].tasks)).toEqual(["t2", "t1"]);
  });

  it("puts everything in one group when not grouped, and nothing when nothing shows", () => {
    const flat = listGroups(
      cards,
      stages,
      { group: "none", sort: "", dir: "asc" },
      () => true,
      ctx,
    );
    expect(flat).toHaveLength(1);
    expect(flat[0].id).toBe(ALL_GROUP);
    expect(flat[0].stage).toBeNull();
    // The board's order read left to right: todo's cards, then doing's.
    expect(ids(flat[0].tasks)).toEqual(["t1", "t2", "g1"]);
    const sorted = listGroups(
      cards,
      stages,
      { group: "none", sort: "priority", dir: "desc" },
      () => true,
      ctx,
    );
    expect(ids(sorted[0].tasks)[0]).toBe("t2");
    expect(
      listGroups(cards, stages, { group: "none", sort: "", dir: "asc" }, () => false, ctx),
    ).toEqual([]);
  });

  it("folds a stage that ends the flow until the person unfolds it", () => {
    const done = { id: "done", stage: stages[2], tasks: [] };
    const doing = { id: "doing", stage: stages[1], tasks: [] };
    expect(groupFolded({}, done)).toBe(true);
    expect(groupFolded({}, doing)).toBe(false);
    expect(groupFolded({ done: false, doing: true }, done)).toBe(false);
    expect(groupFolded({ done: false, doing: true }, doing)).toBe(true);
    expect(groupFolded({}, { id: ALL_GROUP, stage: null, tasks: [] })).toBe(false);
  });
});

describe("rows", () => {
  const kids = new Map([["p", [task("k1"), task("k2")]]]);

  it("shows a parent's children under it only while it is open", () => {
    const closed = listRows([task("p"), task("q")], kids, "nested", new Set(), null);
    expect(closed.map(rowKey)).toEqual(["0:p", "0:q"]);
    expect(closed[0]).toMatchObject({ kids: 2, open: false });

    const open = listRows([task("p"), task("q")], kids, "nested", new Set(["p"]), null);
    expect(open.map(rowKey)).toEqual(["0:p", "1:k1", "1:k2", "0:q"]);
    expect(open[1]).toMatchObject({ depth: 1, match: false });
  });

  it("opens a parent whose child a search hits, and marks that child", () => {
    const rows = listRows(
      [task("p")],
      kids,
      "nested",
      new Set(),
      new Map([["p", new Set(["k2"])]]),
    );
    expect(rows.map((r) => [rowKey(r), r.match])).toEqual([
      ["0:p", false],
      ["1:k1", false],
      ["1:k2", true],
    ]);
  });

  it("never nests when subtasks are cards of their own", () => {
    const rows = listRows([task("p"), task("k1")], kids, "cards", new Set(["p"]), null);
    expect(rows.map(rowKey)).toEqual(["0:p", "0:k1"]);
    expect(rows[0].kids).toBe(0);
  });

  it("moves between rows with the arrows and stops at the ends", () => {
    const order = ["a", "b", "c"];
    expect(neighborRow(order, "b", "ArrowUp")).toBe("a");
    expect(neighborRow(order, "b", "ArrowDown")).toBe("c");
    expect(neighborRow(order, "a", "ArrowUp")).toBeNull();
    expect(neighborRow(order, "c", "ArrowDown")).toBeNull();
    expect(neighborRow(order, "z", "ArrowDown")).toBeNull();
    expect(neighborRow(order, "a", "ArrowLeft")).toBeNull();
  });
});

describe("markup", () => {
  const row = (o: Partial<ListRowView> = {}): ListRowView => ({
    id: "abc123",
    href: "/tasks/?board=main#task-abc123",
    title: "Fix <login>",
    depth: 0,
    kids: 0,
    open: false,
    match: false,
    typeHTML: "",
    partOf: "",
    blocked: "",
    stage: stages[1],
    canMove: false,
    stateHTML: "",
    agent: "",
    repos: [],
    tags: [],
    progressHTML: "",
    age: "",
    updatedISO: "",
    ...o,
  });
  const doc = (html: string, wrap = "tr") => {
    const tpl = document.createElement("template");
    tpl.innerHTML = `<table><tbody><${wrap}>${html}</${wrap}></tbody></table>`;
    return tpl.content.querySelector(wrap) as HTMLElement;
  };
  const head = (html: string) => {
    const tpl = document.createElement("template");
    tpl.innerHTML = `<table><thead>${html}</thead></table>`;
    return tpl.content.querySelector("tr") as HTMLElement;
  };

  it("has one cell per column and a header for each", () => {
    expect(doc(listRowCellsHTML(row())).children).toHaveLength(LIST_COLUMNS.length);
    expect(head(listHeadHTML(defaultTasksView())).querySelectorAll("th")).toHaveLength(
      LIST_COLUMNS.length,
    );
  });

  it("links the title to the task and escapes it", () => {
    const tr = doc(listRowCellsHTML(row()));
    const link = tr.querySelector<HTMLAnchorElement>("a.tl-open");
    expect(link?.getAttribute("href")).toBe("/tasks/?board=main#task-abc123");
    expect(link?.textContent).toBe("Fix <login>");
    expect(tr.querySelector("login")).toBeNull();
    expect(tr.querySelector("[data-card-menu]")?.getAttribute("aria-label")).toContain(
      "Fix <login>",
    );
  });

  it("offers the subtask toggle only on a parent and says whether it is open", () => {
    expect(doc(listRowCellsHTML(row())).querySelector("[data-row-expand]")).toBeNull();
    const parent = doc(listRowCellsHTML(row({ kids: 3, open: true })));
    expect(parent.querySelector("[data-row-expand]")?.getAttribute("aria-expanded")).toBe("true");
  });

  it("shows a dash where a column has nothing, and the stage as a tag", () => {
    const tr = doc(listRowCellsHTML(row()));
    expect(tr.querySelector(".tl-c-agent")?.textContent).toBe("—");
    expect(tr.querySelector(".tl-c-stage")?.textContent).toContain("Doing");
    expect(tr.querySelector("[data-row-stage]")).toBeNull();
  });

  it("makes the stage a button that opens the stage choices for someone who may move it", () => {
    const btn = doc(listRowCellsHTML(row({ canMove: true }))).querySelector<HTMLButtonElement>(
      ".tl-c-stage button[data-row-stage]",
    );
    expect(btn?.dataset.rowStage).toBe("abc123");
    expect(btn?.getAttribute("aria-haspopup")).toBe("menu");
    expect(btn?.getAttribute("aria-expanded")).toBe("false");
    expect(btn?.getAttribute("aria-label")).toContain("Doing");
    expect(btn?.textContent).toContain("Doing");
  });

  it("makes sortable headers buttons with aria-sort, except the stage when grouped by it", () => {
    const grouped = head(listHeadHTML({ group: "stage", sort: "title", dir: "asc" }));
    expect(grouped.querySelector('th[data-sort="title"]')?.getAttribute("aria-sort")).toBe(
      "ascending",
    );
    expect(grouped.querySelector('th[data-sort="priority"]')?.getAttribute("aria-sort")).toBe(
      "none",
    );
    expect(grouped.querySelector(".tl-c-stage button")).toBeNull();
    expect(grouped.querySelector(".tl-c-agent button")).toBeNull();
    const flat = head(listHeadHTML({ group: "none", sort: "", dir: "asc" }));
    expect(flat.querySelector(".tl-c-stage button")).not.toBeNull();
  });

  it("makes the group head one button that says what it does", () => {
    const tr = doc(
      listGroupHeadHTML({ id: "done", label: "Done", color: "#22aa55", count: 4, folded: true }),
    );
    const btn = tr.querySelector<HTMLButtonElement>("button[data-fold]");
    expect(btn?.dataset.fold).toBe("done");
    expect(btn?.getAttribute("aria-expanded")).toBe("false");
    expect(btn?.getAttribute("aria-label")).toContain("Done");
    expect(tr.querySelector("th")?.getAttribute("colspan")).toBe(String(LIST_COLUMNS.length));
    const bad = doc(
      listGroupHeadHTML({ id: "x", label: "X", color: "red;}", count: 1, folded: false }),
    );
    expect(bad.querySelector(".tl-dot")?.getAttribute("style")).toBeNull();
  });

  it("classes nested, matched and lifted rows", () => {
    expect(listRowClasses({ depth: 0, match: false })).toBe("tl-row");
    expect(listRowClasses({ depth: 1, match: true, lifted: true })).toBe(
      "tl-row tl-row-kid is-match is-lifted",
    );
  });
});
