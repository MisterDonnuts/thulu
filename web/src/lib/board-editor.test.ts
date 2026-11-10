// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { ApiError } from "./api";
import {
  BoardEditor,
  applyPatch,
  blankStage,
  defaultInsertIndex,
  errorText,
  finalIndex,
  flagPatch,
  flagState,
  flowWarnings,
  mayEditBoards,
  moveItem,
  nextColor,
  normalizeHex,
  placeStage,
  slugify,
  stepStage,
  swatchName,
  validId,
  type BoardApi,
} from "./board-editor";
import type { Stage } from "./stages";

const col = (id: string, extra: Partial<Stage> = {}): Stage => ({
  ...blankStage(id, id.toUpperCase(), "#5d6477"),
  ...extra,
});

const board = (): Stage[] => [
  col("backlog", { initial: true }),
  col("in_progress", { auto_advance: true }),
  col("done", { terminal: true }),
];

const ids = (list: readonly Stage[]) => list.map((s) => s.id);

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function deferred<T>() {
  let resolve: (v: T) => void = () => undefined;
  let reject: (e: unknown) => void = () => undefined;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

interface Call {
  op: keyof BoardApi;
  board: string;
  args: unknown[];
}

function fakeApi(server: Stage[]) {
  const calls: Call[] = [];
  const answers: Array<ReturnType<typeof deferred<Stage[]>>> = [];
  let auto = true;
  let state = server;
  const answer = (op: keyof BoardApi, board: string, args: unknown[], next: () => Stage[]) => {
    calls.push({ op, board, args });
    if (auto) {
      state = next();
      return Promise.resolve(state);
    }
    const d = deferred<Stage[]>();
    answers.push(d);
    return d.promise;
  };
  const api: BoardApi = {
    reorder: (b, order) =>
      answer("reorder", b, [order], () =>
        order.map((id) => state.find((s) => s.id === id)).filter((s): s is Stage => !!s),
      ),
    patch: (b, id, patch) => answer("patch", b, [id, patch], () => applyPatch(state, id, patch)),
    create: (b, stage, index) =>
      answer("create", b, [stage, index], () => [
        ...state.slice(0, index),
        stage,
        ...state.slice(index),
      ]),
    remove: (b, id, moveTo) =>
      answer("remove", b, [id, moveTo], () => state.filter((s) => s.id !== id)),
  };
  return {
    api,
    calls,
    answers,
    manual() {
      auto = false;
    },
  };
}

describe("reorder math", () => {
  it("moves an item to an index and leaves the input alone", () => {
    const list = ["a", "b", "c", "d"];
    expect(moveItem(list, 0, 2)).toEqual(["b", "c", "a", "d"]);
    expect(moveItem(list, 3, 0)).toEqual(["d", "a", "b", "c"]);
    expect(list).toEqual(["a", "b", "c", "d"]);
  });

  it("clamps a target past either end and ignores a missing item", () => {
    expect(moveItem(["a", "b", "c"], 0, 9)).toEqual(["b", "c", "a"]);
    expect(moveItem(["a", "b", "c"], 2, -4)).toEqual(["c", "a", "b"]);
    expect(moveItem(["a", "b"], -1, 0)).toEqual(["a", "b"]);
  });

  it("drops in front of a column counting the gap the dragged one leaves", () => {
    expect(finalIndex(0, 2, 4)).toBe(1);
    expect(finalIndex(3, 1, 4)).toBe(1);
    expect(finalIndex(1, null, 4)).toBe(3);
    expect(finalIndex(2, 2, 4)).toBe(2);
    expect(finalIndex(2, 3, 4)).toBe(2);
  });

  it("steps one column left or right and stops at the edges", () => {
    expect(ids(stepStage(board(), "in_progress", 1) ?? [])).toEqual([
      "backlog",
      "done",
      "in_progress",
    ]);
    expect(stepStage(board(), "backlog", -1)).toBeNull();
    expect(stepStage(board(), "done", 1)).toBeNull();
    expect(stepStage(board(), "nope", 1)).toBeNull();
  });

  it("places a column at an index", () => {
    expect(ids(placeStage(board(), "done", 0))).toEqual(["done", "backlog", "in_progress"]);
  });
});

describe("flow warnings", () => {
  it("has nothing to say about the shipped board", () => {
    expect(flowWarnings(board())).toEqual([]);
  });

  it("flags a start column that is not first and work after the finish", () => {
    const list = [
      col("in_progress"),
      col("backlog", { initial: true }),
      col("done", { terminal: true }),
      col("qa"),
    ];
    expect(flowWarnings(list)).toEqual([
      { stage: "backlog", kind: "initial_not_first" },
      { stage: "qa", kind: "after_terminal" },
    ]);
  });

  it("does not warn about two finishing columns side by side", () => {
    const list = [...board(), col("cancelled", { terminal: true })];
    expect(flowWarnings(list)).toEqual([]);
  });
});

describe("flags", () => {
  it("locks the start column's own start flag", () => {
    const [backlog] = board();
    const s = flagState(backlog, "initial", board());
    expect(s).toMatchObject({ checked: true, disabled: true });
    expect(s.reason).not.toBe("");
  });

  it("locks the last finishing column but frees it once there is another", () => {
    const list = board();
    expect(flagState(list[2], "terminal", list).disabled).toBe(true);
    const two = [...list, col("cancelled", { terminal: true })];
    expect(flagState(two[2], "terminal", two).disabled).toBe(false);
  });

  it("keeps auto-advance off finishing columns and the start off a finish", () => {
    const list = board();
    expect(flagState(list[2], "auto_advance", list).disabled).toBe(true);
    expect(flagState(list[2], "initial", list).disabled).toBe(true);
    expect(flagState(list[0], "terminal", list).disabled).toBe(true);
    expect(flagState(list[1], "open_pr", list).disabled).toBe(false);
  });

  it("turns off auto-advance when a column becomes a finish", () => {
    expect(flagPatch(board()[1], "terminal", true)).toEqual({
      terminal: true,
      auto_advance: false,
    });
    expect(flagPatch(board()[1], "open_pr", true)).toEqual({ open_pr: true });
  });

  it("moves the start marker the way the daemon does", () => {
    const next = applyPatch(board(), "in_progress", { initial: true });
    expect(next.filter((s) => s.initial).map((s) => s.id)).toEqual(["in_progress"]);
  });
});

describe("ids, colours, roles", () => {
  it("turns a label into an id the daemon accepts", () => {
    expect(slugify("Revisión de seguridad")).toBe("revision_de_seguridad");
    expect(slugify("  QA / Staging!! ")).toBe("qa_staging");
    expect(slugify("¿¿??")).toBe("column");
    expect(slugify("Done", ["done"])).toBe("done_2");
    expect(slugify("Done", ["done", "done_2"])).toBe("done_3");
    expect(slugify("Chat", [], "column", ["chat"])).toBe("chat_2");
    const long = slugify("a".repeat(40), ["a".repeat(32)]);
    expect(long).toHaveLength(32);
    expect(long.endsWith("_2")).toBe(true);
    expect(validId(slugify("Una columna con un nombre larguísimo de verdad"))).toBe(true);
  });

  it("validates hand-typed ids", () => {
    expect(validId("bugfix_2")).toBe(true);
    expect(validId("Bugfix")).toBe(false);
    expect(validId("bug-fix")).toBe(false);
    expect(validId("")).toBe(false);
    expect(validId("a".repeat(33))).toBe(false);
  });

  it("names swatches and picks one the board does not use", () => {
    expect(normalizeHex("#ABC")).toBe("#aabbcc");
    expect(swatchName("#F2994A")).toBe("orange");
    expect(swatchName("#123456")).toBe("");
    expect(nextColor(board())).toBe("#f2994a");
  });

  it("lets admins and owners edit, and a session with no role", () => {
    expect(mayEditBoards("owner")).toBe(true);
    expect(mayEditBoards("admin")).toBe(true);
    expect(mayEditBoards("developer")).toBe(false);
    expect(mayEditBoards("viewer")).toBe(false);
    expect(mayEditBoards(undefined)).toBe(true);
  });

  it("adds a new column before the first finish", () => {
    expect(defaultInsertIndex(board())).toBe(2);
    expect(defaultInsertIndex([col("a"), col("b")])).toBe(2);
  });

  it("explains a failure with the daemon's words", () => {
    expect(errorText(new ApiError(400, "board needs a terminal stage"))).toBe(
      "board needs a terminal stage",
    );
    expect(errorText(undefined)).not.toBe("");
  });
});

describe("BoardEditor", () => {
  it("shows a reorder at once and sends the new order", async () => {
    const fake = fakeApi(board());
    const seen: string[][] = [];
    const ed = new BoardEditor({
      board: "default",
      stages: board(),
      api: fake.api,
      onChange: (s) => seen.push(ids(s)),
    });
    const done = ed.step("in_progress", -1);
    expect(ids(ed.stages)).toEqual(["in_progress", "backlog", "done"]);
    const res = await done;
    expect(res).toMatchObject({ ok: true, kind: "reorder" });
    expect(fake.calls).toEqual([
      { op: "reorder", board: "default", args: [["in_progress", "backlog", "done"]] },
    ]);
    expect(seen[0]).toEqual(["in_progress", "backlog", "done"]);
    expect(ed.busy).toBe(false);
  });

  it("sends nothing for a move that changes nothing", () => {
    const fake = fakeApi(board());
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    expect(ed.step("backlog", -1)).toBeNull();
    expect(ed.moveTo("done", 2)).toBeNull();
    expect(ed.rename("done", "  DONE ")).toBeNull();
    expect(ed.rename("done", "   ")).toBeNull();
    expect(ed.recolor("done", "#5D6477")).toBeNull();
    expect(fake.calls).toEqual([]);
  });

  it("rolls back a refused edit and reports it", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const errors: unknown[] = [];
    const ed = new BoardEditor({
      board: "default",
      stages: board(),
      api: fake.api,
      onError: (r) => errors.push(r.error),
    });
    const done = ed.rename("in_progress", "Doing");
    expect(ed.stages[1].label).toBe("Doing");
    const refusal = new ApiError(400, "label too long");
    await flush();
    fake.answers[0].reject(refusal);
    const res = await done;
    expect(res).toMatchObject({ ok: false, kind: "patch", stage: "in_progress" });
    expect(ed.stages[1].label).toBe("IN_PROGRESS");
    expect(errors).toEqual([refusal]);
  });

  it("keeps a later edit on screen when an earlier one fails", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const first = ed.recolor("backlog", "#e5484d");
    const second = ed.rename("done", "Shipped");
    expect(ed.stages[0].color).toBe("#e5484d");
    expect(ed.stages[2].label).toBe("Shipped");

    await flush();
    expect(fake.calls).toHaveLength(1);
    fake.answers[0].reject(new Error("nope"));
    await first;
    await flush();
    expect(fake.calls).toHaveLength(2);
    expect(ed.stages[0].color).toBe("#5d6477");
    expect(ed.stages[2].label).toBe("Shipped");

    fake.answers[1].resolve(applyPatch(board(), "done", { label: "Shipped" }));
    expect(await second).toMatchObject({ ok: true });
    expect(ed.stages.map((s) => s.label)).toEqual(["BACKLOG", "IN_PROGRESS", "Shipped"]);
  });

  it("toggles a flag with what has to change alongside it", async () => {
    const fake = fakeApi([...board(), col("cancelled", { terminal: true })]);
    const ed = new BoardEditor({
      board: "default",
      stages: [...board(), col("cancelled", { terminal: true })],
      api: fake.api,
    });
    await ed.setFlag("in_progress", "terminal", true);
    expect(fake.calls[0]).toEqual({
      op: "patch",
      board: "default",
      args: ["in_progress", { terminal: true, auto_advance: false }],
    });
    expect(ed.stages[1]).toMatchObject({ terminal: true, auto_advance: false });
    expect(ed.setFlag("backlog", "initial", false)).toBeNull();
    expect(ed.setFlag("done", "auto_advance", true)).toBeNull();
    expect(fake.calls).toHaveLength(1);
  });

  it("makes another column the start and clears the old one", async () => {
    const fake = fakeApi(board());
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    await ed.setFlag("in_progress", "initial", true);
    expect(ed.stages.filter((s) => s.initial).map((s) => s.id)).toEqual(["in_progress"]);
  });

  it("deletes a column and sends where its tasks go", async () => {
    const fake = fakeApi(board());
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const res = await ed.remove("in_progress", "backlog");
    expect(res).toMatchObject({ ok: true, kind: "remove", stage: "in_progress" });
    expect(fake.calls).toEqual([
      { op: "remove", board: "default", args: ["in_progress", "backlog"] },
    ]);
    expect(ids(ed.stages)).toEqual(["backlog", "done"]);
  });

  it("puts a deleted column back when the daemon refuses", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const done = ed.remove("in_progress");
    expect(ids(ed.stages)).toEqual(["backlog", "done"]);
    await flush();
    fake.answers[0].reject(new ApiError(409, "stage has tasks", { task_count: 3 }));
    const res = await done;
    expect(res.ok).toBe(false);
    expect(fake.calls[0].args).toEqual(["in_progress", ""]);
    expect(ids(ed.stages)).toEqual(["backlog", "in_progress", "done"]);
  });

  it("creates a column with an id from its label, before the finish", async () => {
    const fake = fakeApi(board());
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const made = ed.create("Code review");
    expect(made?.id).toBe("code_review");
    expect(ids(ed.stages)).toEqual(["backlog", "in_progress", "code_review", "done"]);
    await made?.done;
    const [stage, index] = fake.calls[0].args as [Stage, number];
    expect(stage).toMatchObject({ id: "code_review", label: "Code review", color: "#f2994a" });
    expect(index).toBe(2);
    expect(ed.create("   ")).toBeNull();
  });

  it("creates at the index asked for, with a free id", async () => {
    const fake = fakeApi(board());
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const made = ed.create("Backlog", { index: 0, color: "#56a8f2" });
    expect(made?.id).toBe("backlog_2");
    await made?.done;
    expect(ids(ed.stages)).toEqual(["backlog_2", "backlog", "in_progress", "done"]);
  });

  it("ignores a fresh read while its own edits are in flight", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const done = ed.step("done", -1);
    ed.reset("default", board());
    expect(ids(ed.stages)).toEqual(["backlog", "done", "in_progress"]);
    await flush();
    fake.answers[0].resolve(placeStage(board(), "done", 1));
    await done;
    ed.reset("other", [col("todo", { initial: true }), col("shipped", { terminal: true })]);
    expect(ed.boardID).toBe("other");
    expect(ids(ed.stages)).toEqual(["todo", "shipped"]);
  });

  it("drops edits still waiting when the board switches, never sending them to the new one", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const errors: unknown[] = [];
    const ed = new BoardEditor({
      board: "default",
      stages: board(),
      api: fake.api,
      onError: (r) => errors.push(r),
    });
    const first = ed.rename("backlog", "Inbox");
    const second = ed.recolor("backlog", "#e5484d");
    const third = ed.step("done", -1);
    await flush();
    expect(fake.calls).toHaveLength(1);

    ed.reset("other", [col("backlog", { initial: true }), col("done", { terminal: true })]);
    fake.answers[0].resolve(applyPatch(board(), "backlog", { label: "Inbox" }));
    expect(await first).toMatchObject({ ok: true });
    expect(await second).toMatchObject({ ok: false, dropped: true });
    expect(await third).toMatchObject({ ok: false, dropped: true });
    await flush();

    expect(fake.calls.map((c) => [c.op, c.board])).toEqual([["patch", "default"]]);
    expect(errors).toEqual([]);
    expect(ed.boardID).toBe("other");
    expect(ids(ed.stages)).toEqual(["backlog", "done"]);
    expect(ed.stages[0].label).toBe("BACKLOG");
  });

  it("still drops them after switching away and back", async () => {
    const fake = fakeApi(board());
    fake.manual();
    const ed = new BoardEditor({ board: "default", stages: board(), api: fake.api });
    const first = ed.rename("backlog", "Inbox");
    const second = ed.rename("done", "Shipped");
    await flush();
    ed.reset("other", [col("todo", { initial: true })]);
    ed.reset("default", board());
    fake.answers[0].resolve(applyPatch(board(), "backlog", { label: "Inbox" }));
    await first;
    expect(await second).toMatchObject({ dropped: true });
    expect(fake.calls).toHaveLength(1);
  });
});
