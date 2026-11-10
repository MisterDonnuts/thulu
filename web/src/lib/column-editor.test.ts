// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { blankStage } from "./board-editor";
import {
  columnMenuEntries,
  flagCheckHTML,
  legacyFlagCheckHTML,
  mountColumnEditor,
} from "./column-editor";
import { ALT_KEY } from "./shortcuts";
import { isMenuItem, menuItemsHTML } from "./ui";
import type { Stage } from "./stages";

const COL_W = 200;
const ROW_H = 200;

const stages: Stage[] = [
  { ...blankStage("backlog", "Backlog", "#5d6477"), initial: true },
  blankStage("in_progress", "In progress", "#f2994a"),
  blankStage("review", "Review", "#bb86fc"),
  { ...blankStage("done", "Done", "#3aa667"), terminal: true },
];

let root: HTMLElement;
let abort: AbortController;
let render: ReturnType<typeof vi.fn<(stages: Stage[]) => void>>;

function rect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    x: left,
    y: top,
    toJSON: () => ({}),
  };
}

const order = () =>
  Array.from(root.querySelectorAll<HTMLElement>(".col[data-stage]")).map((c) => c.dataset.stage);

function pointer(target: EventTarget, type: string, x: number, y: number) {
  const e = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: y,
    button: 0,
  });
  Object.defineProperty(e, "pointerId", { value: 1 });
  target.dispatchEvent(e);
}

const headOf = (id: string) =>
  root.querySelector<HTMLElement>(`.col[data-stage="${id}"] .col-head`) as HTMLElement;

beforeEach(() => {
  document.body.innerHTML = `<div id="ce-live"></div><div id="ce-menu" class="menu menu-floating" role="menu" aria-label="Column options" tabindex="-1" hidden></div><div id="cols"></div>`;
  root = document.getElementById("cols") as HTMLElement;
  root.innerHTML = stages
    .map(
      (s) =>
        `<div class="col" data-stage="${s.id}"><div class="col-head"><span class="col-title">${s.label}</span></div></div>`,
    )
    .join("");
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    if (this === root) return rect(0, 0, COL_W * 4, ROW_H);
    const col = this.closest<HTMLElement>(".col");
    if (col?.parentElement !== root) return rect(0, 0, 0, 0);
    const i = Array.from(root.children).indexOf(col);
    return rect(i * COL_W, 0, COL_W, ROW_H);
  });
  Object.assign(HTMLElement.prototype, {
    setPointerCapture: () => undefined,
    releasePointerCapture: () => undefined,
    hasPointerCapture: () => false,
  });
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => undefined);
  vi.stubGlobal("matchMedia", () => ({ matches: true }));
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise<never>(() => undefined)),
  );
  abort = new AbortController();
  render = vi.fn<(stages: Stage[]) => void>();
  mountColumnEditor({
    root,
    board: "default",
    stages,
    signal: abort.signal,
    canEdit: true,
    editing: true,
    render,
    taskCount: () => 0,
    toast: () => undefined,
  });
});

afterEach(() => {
  abort.abort();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("column drag", () => {
  it("puts the column back when let go away from the columns after it moved", () => {
    pointer(headOf("review"), "pointerdown", 500, 50);
    pointer(window, "pointermove", 480, 50);
    pointer(window, "pointermove", 250, 50);
    expect(order()).toEqual(["backlog", "review", "in_progress", "done"]);

    pointer(window, "pointermove", 50, 900);
    expect(order()).toEqual(["backlog", "review", "in_progress", "done"]);

    pointer(document.body, "pointerup", 50, 900);
    expect(order()).toEqual(["backlog", "in_progress", "review", "done"]);
    expect(root.querySelector<HTMLElement>('[data-stage="review"]')?.style.transform).toBe("");
    expect(render).not.toHaveBeenCalled();

    pointer(headOf("in_progress"), "pointerup", 250, 50);
    expect(render).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("commits the order when let go over the columns", () => {
    pointer(headOf("review"), "pointerdown", 500, 50);
    pointer(window, "pointermove", 480, 50);
    pointer(window, "pointermove", 250, 50);
    pointer(document.body, "pointerup", 250, 50);
    expect(render).toHaveBeenCalledTimes(1);
    expect(render.mock.calls[0][0].map((s) => s.id)).toEqual([
      "backlog",
      "review",
      "in_progress",
      "done",
    ]);
  });
});

describe("column menu chords", () => {
  it("are escaped where the menu draws them", () => {
    const html = menuItemsHTML([{ title: "Move", kbd: ["<b>", "x"] }]);
    expect(html).toContain('<kbd class="kbd-sm">&lt;b&gt;</kbd>');
  });
});

describe("columnMenuEntries", () => {
  const items = (st: Stage) => columnMenuEntries(st, stages).filter(isMenuItem);
  const byAct = (st: Stage, act: string) => items(st).find((i) => i.attrs?.["data-act"] === act);

  it("turns Move off at the board's ends and keeps the chord's caps", () => {
    expect(byAct(stages[0], "left")?.disabled).toBe(true);
    expect(byAct(stages[0], "right")?.disabled).toBe(false);
    expect(byAct(stages[3], "right")?.disabled).toBe(true);
    expect(byAct(stages[1], "left")?.kbd).toEqual([ALT_KEY, "←"]);
    const left = byAct(stages[1], "left");
    expect(left).toBeDefined();
    const html = menuItemsHTML(left ? [left] : []);
    expect(html).toContain('<kbd class="kbd-sm" data-mod-key="alt">Alt</kbd>');
  });

  it("keeps Delete off on the first column and says why under it", () => {
    const del = byAct(stages[0], "delete");
    expect(del?.disabled).toBe(true);
    expect(del?.danger).toBe(true);
    expect(del?.sub).toBeTruthy();
    expect(byAct(stages[1], "delete")?.disabled).toBe(false);
    expect(byAct(stages[1], "delete")?.sub).toBeUndefined();
  });

  it("puts the colours in one row of radios that keep the menu open", () => {
    const html = menuItemsHTML(columnMenuEntries(stages[1], stages));
    const box = document.createElement("div");
    box.innerHTML = html;
    const row = box.querySelector("[data-menu-row]");
    expect(row?.getAttribute("role")).toBe("group");
    expect(row?.classList.contains("swatch-picker")).toBe(true);
    const swatches = Array.from(row?.querySelectorAll<HTMLElement>(".swatch") ?? []);
    expect(swatches.length).toBeGreaterThan(1);
    expect(swatches.every((s) => s.getAttribute("role") === "menuitemradio")).toBe(true);
    expect(swatches.every((s) => s.tabIndex === -1)).toBe(true);
    expect(swatches.every((s) => s.dataset.swatch === s.dataset.value)).toBe(true);
    expect(swatches.every((s) => s.hasAttribute("data-keep-open"))).toBe(true);
    expect(swatches.filter((s) => s.getAttribute("aria-checked") === "true")).toHaveLength(1);
  });
});

describe("column menu", () => {
  const menu = () => document.getElementById("ce-menu") as HTMLElement;
  function addTriggers() {
    for (const s of stages) {
      const head = headOf(s.id);
      head.insertAdjacentHTML(
        "beforeend",
        `<button type="button" data-col-menu aria-haspopup="menu" aria-expanded="false">⋯</button>`,
      );
    }
  }
  function need<T extends Element>(el: T | null | undefined, what: string): T {
    if (!el) throw new Error(`no ${what}`);
    return el;
  }
  const trigger = (id: string) =>
    need(headOf(id).querySelector<HTMLElement>("[data-col-menu]"), `${id} menu button`);

  it("opens on the column it was pressed on and closes from the same button", () => {
    addTriggers();
    trigger("in_progress").click();
    expect(menu().hidden).toBe(false);
    expect(menu().getAttribute("aria-label")).toContain("In progress");
    expect(trigger("in_progress").getAttribute("aria-expanded")).toBe("true");
    expect((document.activeElement as HTMLElement).dataset.act).toBe("rename");
    trigger("in_progress").click();
    expect(menu().hidden).toBe(true);
    expect(trigger("in_progress").getAttribute("aria-expanded")).toBe("false");
  });

  it("moves to another column's button and repaints for that column", () => {
    addTriggers();
    trigger("in_progress").click();
    trigger("review").click();
    expect(menu().hidden).toBe(false);
    expect(menu().getAttribute("aria-label")).toContain("Review");
    expect(trigger("in_progress").getAttribute("aria-expanded")).toBe("false");
    expect(trigger("review").getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps the menu open on a colour and moves the check to it", () => {
    addTriggers();
    trigger("review").click();
    const swatches = Array.from(menu().querySelectorAll<HTMLElement>(".swatch"));
    const pick = need(
      swatches.find((s) => s.getAttribute("aria-checked") !== "true"),
      "an unchecked swatch",
    );
    pick.click();
    expect(menu().hidden).toBe(false);
    expect(pick.getAttribute("aria-checked")).toBe("true");
    expect(swatches.filter((s) => s.getAttribute("aria-checked") === "true")).toEqual([pick]);
  });

  it("never runs an off item", () => {
    addTriggers();
    trigger("backlog").click();
    const del = need(menu().querySelector<HTMLElement>('[data-act="delete"]'), "Delete");
    expect(del.getAttribute("aria-disabled")).toBe("true");
    del.click();
    expect(menu().hidden).toBe(false);
  });
});

describe("flagCheckHTML", () => {
  function row(html: string): HTMLElement {
    const host = document.createElement("div");
    host.innerHTML = html;
    return host.firstElementChild as HTMLElement;
  }
  function find<T extends Element>(el: Element, sel: string): T {
    const found = el.querySelector<T>(sel);
    if (!found) throw new Error(`no ${sel}`);
    return found;
  }

  it("draws a flag as a Check with its hooks, its hint and an empty error line", () => {
    const el = row(flagCheckHTML("auto_advance", { checked: true, disabled: false, reason: "" }));
    expect(el.tagName).toBe("LABEL");
    expect(el.classList.contains("check")).toBe(true);
    expect(el.dataset.flagRow).toBe("auto_advance");
    const input = find<HTMLInputElement>(el, "input");
    expect(input.type).toBe("checkbox");
    expect(input.id).toBe("ce-flag-auto_advance");
    expect(input.dataset.flag).toBe("auto_advance");
    expect(input.checked).toBe(true);
    expect(input.disabled).toBe(false);
    expect(el.querySelector(".check-hint")?.textContent).not.toBe("");
    expect(el.querySelector(".check-reason")).toBeNull();
    const err = find<HTMLElement>(el, ".check-error");
    expect(err.id).toBe("ce-flag-auto_advance-error");
    expect(err.hidden).toBe(true);
    const described = (input.getAttribute("aria-describedby") ?? "").split(" ");
    expect(described).toContain("ce-flag-auto_advance-hint");
    expect(described).toContain("ce-flag-auto_advance-error");
  });

  it("says why a locked flag is off", () => {
    const el = row(
      flagCheckHTML("initial", {
        checked: false,
        disabled: true,
        reason: "Backlog already starts",
      }),
    );
    const input = find<HTMLInputElement>(el, "input");
    expect(input.disabled).toBe(true);
    expect(el.querySelector(".check-reason")?.textContent).toBe("Backlog already starts");
    expect(input.getAttribute("aria-describedby")).toContain("ce-flag-initial-reason");
  });

  it("drops a reason on a flag that is not locked", () => {
    const el = row(flagCheckHTML("terminal", { checked: false, disabled: false, reason: "stale" }));
    expect(el.querySelector(".check-reason")).toBeNull();
  });

  it("draws the blocked-by-flags setting ticked, as a box to turn off", () => {
    const el = row(legacyFlagCheckHTML());
    expect(el.dataset.flagRow).toBe("blocked_by_flags");
    const input = find<HTMLInputElement>(el, "input");
    expect(input.id).toBe("ce-flag-legacy");
    expect(input.hasAttribute("data-legacy")).toBe(true);
    expect(input.checked).toBe(true);
    expect(el.querySelector(".check-error")?.id).toBe("ce-flag-legacy-error");
  });
});

describe("ghost column", () => {
  it("is a bare column on the board", () => {
    expect(root.querySelector(".col-ghost")?.className).toBe("col col-ghost");
  });

  it("takes the host's column classes, so Settings draws it as a card", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const ctl = new AbortController();
    mountColumnEditor({
      root: host,
      board: "default",
      stages: [],
      signal: ctl.signal,
      canEdit: true,
      editing: true,
      columnClass: "card",
      render: () => undefined,
      taskCount: () => 0,
      toast: () => undefined,
    });
    expect(host.querySelector(".col-ghost")?.className).toBe("card col col-ghost");
    ctl.abort();
  });
});
