// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { bindMenu, menuHTML } from "./menu";
import {
  bindPicker,
  filterPickerItems,
  openPicker,
  pickerClasses,
  pickerHTML,
  pickerGroups,
  pickerListHTML,
  pickerStep,
  type PickerItem,
} from "./picker";

function byId<T extends HTMLElement = HTMLElement>(id: string): T {
  const el = document.getElementById(id);
  if (!el) throw new Error(`no #${id}`);
  return el as T;
}

function pick<T extends HTMLElement = HTMLElement>(root: HTMLElement, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

const stages: PickerItem[] = [
  { value: "backlog", label: "Backlog", dot: "#5d6477" },
  { value: "review", label: "Review", dot: "#bb86fc", selected: true },
  { value: "done", label: "Done", hint: "terminal", disabled: true },
];

describe("pickerClasses", () => {
  it("adds the variants after .nselect", () => {
    expect(pickerClasses()).toBe("nselect");
    expect(pickerClasses({ place: "floating", width: "lg", start: true, up: true, cls: "x" })).toBe(
      "nselect nselect-floating nselect-start nselect-up nselect-lg x",
    );
    expect(pickerClasses({ place: "inline", width: "sm" })).toBe(
      "nselect nselect-inline nselect-sm",
    );
  });

  it("refuses a place or a width it does not have", () => {
    // @ts-expect-error not a place
    expect(() => pickerClasses({ place: "side" })).toThrow(/place/);
    // @ts-expect-error not a width
    expect(() => pickerClasses({ width: "xl" })).toThrow(/width/);
  });
});

describe("pickerHTML", () => {
  it("writes a single list without a search: the list takes the focus", () => {
    const html = pickerHTML({ id: "st", label: "Stage", items: stages, active: 1 });
    expect(html).toMatch(/^<div class="nselect" id="st" data-picker="">/);
    expect(html).not.toContain("nselect-headrow");
    expect(html).toContain(
      '<div id="st-list" class="nselect-list" role="listbox" aria-label="Stage" tabindex="0" aria-activedescendant="st-o1">',
    );
    expect(html).toContain(
      '<div id="st-o1" class="nselect-item active" role="option" aria-selected="true" data-value="review" data-index="1">' +
        '<span class="nselect-lead"><span class="dot" aria-hidden="true" style="--dot-color:#bb86fc"></span></span>' +
        '<span class="nselect-label">Review</span><svg class="ico nselect-check" aria-hidden="true"><use href="#i-check"/></svg></div>',
    );
    expect(html).toContain('aria-disabled="true"');
    expect(html).toContain('<span class="nselect-item-hint">terminal</span>');
  });

  it("writes a multi picker with chips, a search combobox, a hint and a create row", () => {
    const html = pickerHTML({
      id: "tg",
      label: "Labels",
      items: [
        { value: "ui", label: "ui", selected: true, chip: { color: "blue" } },
        { value: "api", label: "api" },
      ],
      multi: true,
      chips: true,
      search: "Add a label",
      query: "a",
      hint: "Enter adds it",
      create: "Create a",
      active: 2,
    });
    expect(html).toContain(
      '<div class="nselect-headrow"><span class="nselect-head"><span class="tag',
    );
    expect(html).toContain('data-remove="ui"');
    expect(html).toContain(
      '<input id="tg-q" class="nselect-input" type="text" role="combobox" aria-expanded="true" aria-controls="tg-list" aria-autocomplete="list" aria-activedescendant="tg-create" aria-label="Labels" placeholder="Add a label" value="a" autocomplete="off" spellcheck="false">',
    );
    expect(html).toContain('<div class="nselect-hint">Enter adds it</div>');
    expect(html).toContain('aria-multiselectable="true"');
    expect(html).not.toContain('tabindex="0"');
    expect(html).toContain(
      '<div id="tg-create" class="nselect-item nselect-create active" role="option" aria-selected="false" data-create="">',
    );
  });

  it("says nothing matches when no option is left", () => {
    expect(pickerListHTML({ id: "x", label: "X", items: [] })).toBe(
      '<div class="nselect-empty">No matches</div>',
    );
    expect(pickerListHTML({ id: "x", label: "X", items: [], empty: "No agents" })).toContain(
      "No agents",
    );
  });

  it("escapes labels and hints but writes labelHTML as given", () => {
    const html = pickerListHTML({
      id: "x",
      label: "X",
      items: [
        { value: '"v"', label: "<b>", hint: "&" },
        { value: "p", label: "pill", labelHTML: '<span class="tag">pill</span>' },
      ],
    });
    expect(html).toContain("&lt;b&gt;");
    expect(html).toContain("&amp;");
    expect(html).toContain('data-value="&quot;v&quot;"');
    expect(html).toContain('<span class="nselect-label"><span class="tag">pill</span></span>');
  });

  it("refuses a picker without an id or a label", () => {
    expect(() => pickerHTML({ id: "", label: "X", items: [] })).toThrow(/id and a label/);
    expect(() => pickerHTML({ id: "x", label: "", items: [] })).toThrow(/id and a label/);
  });
});

describe("groups", () => {
  it("cuts the options into the runs their group names, keeping the indexes", () => {
    const items: PickerItem[] = [
      { value: "a", label: "A" },
      { value: "b", label: "B", group: "Same parent" },
      { value: "c", label: "C", group: "Same parent" },
      { value: "d", label: "D", group: "Other tasks" },
    ];
    expect(pickerGroups(items).map((g) => [g.label, g.start, g.items.length])).toEqual([
      [undefined, 0, 1],
      ["Same parent", 1, 2],
      ["Other tasks", 3, 1],
    ]);
    const box = document.createElement("div");
    box.innerHTML = pickerListHTML({ id: "p", label: "Tasks", items, active: 3 });
    const groups = box.querySelectorAll<HTMLElement>('[role="group"].nselect-group');
    expect(groups).toHaveLength(2);
    expect(groups[0].getAttribute("aria-labelledby")).toBe("p-g1");
    expect(box.querySelector("#p-g1")?.textContent).toBe("Same parent");
    expect([...groups[1].querySelectorAll('[role="option"]')].map((o) => o.id)).toEqual(["p-o3"]);
  });
});

describe("filterPickerItems", () => {
  it("matches label, hint or value in any case", () => {
    expect(filterPickerItems(stages, "REV").map((i) => i.value)).toEqual(["review"]);
    expect(filterPickerItems(stages, "termi").map((i) => i.value)).toEqual(["done"]);
    expect(filterPickerItems(stages, "  ")).toBe(stages);
  });
});

describe("pickerStep", () => {
  const off = (i: number) => i === 1;
  it("wraps and skips off options", () => {
    expect(pickerStep(off, 3, -1, 1)).toBe(0);
    expect(pickerStep(off, 3, 0, 1)).toBe(2);
    expect(pickerStep(off, 3, 2, 1)).toBe(0);
    expect(pickerStep(off, 3, 0, -1)).toBe(2);
    expect(pickerStep(off, 3, -1, -1)).toBe(2);
    expect(pickerStep(() => true, 3, 0, 1)).toBe(-1);
    expect(pickerStep(off, 0, -1, 1)).toBe(-1);
  });
});

const press = (el: Element, key: string) =>
  el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
const type = (input: HTMLInputElement, value: string) => {
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
};
const activeValue = (el: HTMLElement) =>
  el.querySelector<HTMLElement>(".nselect-item.active")?.dataset.value;

describe("openPicker", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  function anchor(): HTMLButtonElement {
    document.body.innerHTML = `<div class="nprop"><button id="a" type="button">Stage</button></div><button id="out"></button>`;
    const a = document.getElementById("a") as HTMLButtonElement;
    a.focus();
    return a;
  }

  it("opens floating beside the anchor, on the chosen option, with the list focused", () => {
    const a = anchor();
    const h = openPicker(a, { label: "Stage", items: stages, onSelect: vi.fn() });
    expect(h.el.parentElement).toBe(a.parentElement);
    expect(h.el.className).toBe("nselect nselect-floating");
    expect(document.activeElement?.classList.contains("nselect-list")).toBe(true);
    expect(activeValue(h.el)).toBe("review");
    expect(h.el.querySelector(".nselect-list")?.getAttribute("aria-activedescendant")).toMatch(
      /-o1$/,
    );
    h.close();
  });

  it("goes under or over its row by the room in the box that clips it", () => {
    const rects: Record<string, [number, number]> = {};
    const spy = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockImplementation(function (this: HTMLElement) {
        const [top, bottom] = this.classList.contains("nselect")
          ? [0, 360]
          : (rects[this.id] ?? [0, 0]);
        return {
          top,
          bottom,
          height: bottom - top,
          left: 0,
          right: 0,
          width: 0,
          x: 0,
          y: top,
        } as DOMRect;
      });
    const scrolled = vi.mocked(Element.prototype.scrollIntoView);
    const open = (pane: string, row: [number, number]) => {
      document.body.innerHTML = `<div id="pane" style="${pane}"><div class="nprop" id="row"><button id="a" type="button">Deps</button></div></div>`;
      rects.row = row;
      scrolled.mockClear();
      const h = openPicker(byId("a"), {
        label: "Deps",
        items: stages,
        mount: byId("row"),
        onSelect: vi.fn(),
      });
      const seen = [
        h.el.classList.contains("nselect-up"),
        h.el.style.getPropertyValue("--nselect-room"),
        scrolled.mock.contexts.includes(h.el),
      ];
      h.close();
      return seen;
    };
    const hung = document.createElement("style");
    hung.textContent = ".nselect { position: absolute; }";
    document.head.append(hung);
    rects.pane = [100, 600];
    expect(open("overflow-y: auto", [130, 160])).toEqual([false, "", false]);
    expect(open("overflow-y: auto", [480, 510])).toEqual([true, "", false]);
    scrolled.mockImplementation(function (this: Element) {
      if (this.classList.contains("nselect")) rects.row = [100, 130];
    });
    expect(open("overflow-y: auto", [300, 330])).toEqual([false, "", true]);
    scrolled.mockImplementation(() => undefined);
    expect(open("overflow-y: auto", [300, 330])).toEqual([false, "262px", true]);
    expect(open("", [300, 500])).toEqual([true, "292px", false]);
    hung.textContent = ".nselect { position: static; }";
    expect(open("overflow-y: auto", [480, 510])).toEqual([false, "", true]);
    hung.remove();
    spy.mockRestore();
  });

  it("hangs from a mount by CSS as under a property row", () => {
    const a = anchor();
    const h = openPicker(a, {
      label: "Stage",
      items: stages,
      mount: byId("a").parentElement ?? document.body,
      width: "md",
      onSelect: vi.fn(),
    });
    expect(h.el.className).toBe("nselect nselect-md");
    h.close();
  });

  it("walks with the arrows skipping off options, and picks with Enter, closing first", () => {
    const a = anchor();
    const seen: string[] = [];
    const h = openPicker(a, {
      label: "Stage",
      items: stages,
      onSelect: (i) => seen.push(`${i.value}:${h.isOpen()}`),
    });
    const list = pick(h.el, ".nselect-list");
    press(list, "ArrowDown");
    expect(activeValue(h.el)).toBe("backlog");
    press(list, "ArrowUp");
    expect(activeValue(h.el)).toBe("review");
    press(list, "Enter");
    expect(seen).toEqual(["review:false"]);
    expect(h.el.isConnected).toBe(false);
    expect(document.activeElement).toBe(a);
  });

  it("picks on a click, never an off option", () => {
    const a = anchor();
    const onSelect = vi.fn();
    const h = openPicker(a, { label: "Stage", items: stages, onSelect });
    (h.el.querySelector('[data-value="done"]') as HTMLElement).click();
    expect(onSelect).not.toHaveBeenCalled();
    (h.el.querySelector('[data-value="backlog"]') as HTMLElement).click();
    expect(onSelect).toHaveBeenCalledWith(stages[0]);
  });

  it("filters as you type, offers to create, and says when nothing matches", () => {
    const a = anchor();
    const onCreate = vi.fn();
    const h = openPicker(a, {
      label: "Labels",
      search: "Find",
      items: stages,
      createLabel: (q) => `Create ${q}`,
      onCreate,
      onSelect: vi.fn(),
    });
    const input = pick<HTMLInputElement>(h.el, ".nselect-input");
    expect(document.activeElement).toBe(input);
    type(input, "back");
    expect(h.el.querySelectorAll(".nselect-item:not(.nselect-create)")).toHaveLength(1);
    expect(h.el.querySelector(".nselect-create")?.textContent).toContain("Create back");
    type(input, "zzz");
    expect(h.el.querySelector(".nselect-empty")?.textContent).toBe("No matches");
    expect(input.getAttribute("aria-activedescendant")).toMatch(/-create$/);
    press(input, "Enter");
    expect(onCreate).toHaveBeenCalledWith("zzz");
  });

  it("stays open on a multi pick, clears the search and repaints from the source", () => {
    const a = anchor();
    let chosen = ["ui"];
    const all = ["ui", "api", "docs"];
    const source = () => all.map((v) => ({ value: v, label: v, selected: chosen.includes(v) }));
    const h = openPicker(a, {
      label: "Labels",
      multi: true,
      chips: true,
      search: "Find",
      items: (q) => source().filter((i) => i.label.includes(q)),
      onSelect: (i) => {
        chosen = i.selected ? chosen.filter((v) => v !== i.value) : [...chosen, i.value];
      },
      onRemove: (v) => {
        chosen = chosen.filter((x) => x !== v);
        h.refresh();
      },
    });
    const input = pick<HTMLInputElement>(h.el, ".nselect-input");
    expect(h.el.querySelectorAll(".nselect-head .tag")).toHaveLength(1);
    type(input, "ap");
    press(input, "Enter");
    expect(chosen).toEqual(["ui", "api"]);
    expect(h.isOpen()).toBe(true);
    expect(input.value).toBe("");
    expect(h.el.querySelectorAll(".nselect-head .tag")).toHaveLength(2);
    press(input, "Backspace");
    expect(chosen).toEqual(["ui"]);
    (h.el.querySelector('.nselect-head [data-remove="ui"]') as HTMLElement).click();
    expect(chosen).toEqual([]);
    h.close();
  });

  it("lists the current values with an X, passes the kind with a pick and shows a failure", () => {
    const a = anchor();
    let current = ["t1"];
    const picks: Array<[string, string | undefined]> = [];
    const h = openPicker(a, {
      label: "Dependencies",
      search: "Find",
      multi: true,
      errors: true,
      current: () => ({
        heading: "Blocked by",
        empty: "None yet",
        items: current.map((v) => ({ value: v, label: `Task ${v}` })),
      }),
      kind: {
        label: "Kind",
        value: "hard",
        options: [
          { value: "hard", label: "blocking" },
          { value: "soft", label: "heads-up" },
        ],
      },
      items: [{ value: "t2", label: "Task t2" }],
      onSelect: (i, p) => picks.push([i.value, p?.kind]),
      onRemove: (v) => {
        current = current.filter((x) => x !== v);
        h.refresh();
      },
    });
    const group = pick(h.el, '.nselect-current[role="group"]');
    expect(byId(group.getAttribute("aria-labelledby") ?? "").textContent).toBe("Blocked by");
    const x = pick<HTMLButtonElement>(group, '[data-remove="t1"]');
    expect(x.getAttribute("aria-label")).toBe("Remove Task t1");
    pick<HTMLSelectElement>(h.el, ".nselect-kind").value = "soft";
    press(pick(h.el, ".nselect-input"), "Enter");
    expect(picks).toEqual([["t2", "soft"]]);
    pick(group, '[data-remove="t1"]').click();
    expect(current).toEqual([]);
    expect(group.querySelector(".nselect-empty")?.textContent).toBe("None yet");
    expect(document.activeElement).toBe(pick(h.el, ".nselect-input"));
    const err = pick(h.el, '.nselect-error[role="alert"]');
    expect(err.hidden).toBe(true);
    h.setError("That would make a cycle");
    expect(err.hidden).toBe(false);
    expect(err.textContent).toBe("That would make a cycle");
    h.setError("");
    expect(err.hidden).toBe(true);
    h.close();
  });

  it("leaves Enter on a row's X to the button: it removes that value and picks nothing", () => {
    const a = anchor();
    let current = ["t1"];
    const onSelect = vi.fn();
    const onRemove = vi.fn((v: string) => {
      current = current.filter((x) => x !== v);
      h.refresh();
    });
    const h = openPicker(a, {
      label: "Dependencies",
      search: "Find",
      multi: true,
      current: () => ({
        heading: "Blocked by",
        empty: "None yet",
        items: current.map((v) => ({ value: v, label: `Task ${v}` })),
      }),
      items: [{ value: "t2", label: "Task t2" }],
      onSelect,
      onRemove,
    });
    expect(activeValue(h.el)).toBe("t2");
    const x = pick<HTMLButtonElement>(h.el, '.nselect-current [data-remove="t1"]');
    x.focus();
    expect(press(x, "Enter")).toBe(true);
    expect(press(x, " ")).toBe(true);
    x.click();
    expect(onSelect).not.toHaveBeenCalled();
    expect(onRemove).toHaveBeenCalledTimes(1);
    expect(onRemove).toHaveBeenCalledWith("t1");
    expect(current).toEqual([]);
    h.close();
  });

  it("leaves the arrows and Enter on the kind select to the select", () => {
    const a = anchor();
    const onSelect = vi.fn();
    const h = openPicker(a, {
      label: "Dependencies",
      search: "Find",
      kind: {
        label: "Kind",
        value: "hard",
        options: [
          { value: "hard", label: "blocking" },
          { value: "soft", label: "heads-up" },
        ],
      },
      items: [
        { value: "t2", label: "Task t2" },
        { value: "t3", label: "Task t3" },
      ],
      onSelect,
    });
    const input = pick(h.el, ".nselect-input");
    const before = input.getAttribute("aria-activedescendant");
    const kind = pick<HTMLSelectElement>(h.el, ".nselect-kind");
    kind.focus();
    expect(press(kind, "ArrowDown")).toBe(true);
    expect(press(kind, "ArrowUp")).toBe(true);
    expect(press(kind, "Enter")).toBe(true);
    expect(activeValue(h.el)).toBe("t2");
    expect(input.getAttribute("aria-activedescendant")).toBe(before);
    expect(onSelect).not.toHaveBeenCalled();
    expect(h.isOpen()).toBe(true);
    h.close();
  });

  it("lets Tab reach its other controls and closes when the focus leaves it", () => {
    const a = anchor();
    const h = openPicker(a, {
      label: "Dependencies",
      search: "Find",
      kind: { label: "Kind", value: "hard", options: [{ value: "hard", label: "blocking" }] },
      items: [{ value: "t2", label: "Task t2" }],
      onSelect: vi.fn(),
    });
    const input = pick(h.el, ".nselect-input");
    press(input, "Tab");
    expect(h.isOpen()).toBe(true);
    const kind = pick(h.el, ".nselect-kind");
    input.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: kind }));
    expect(h.isOpen()).toBe(true);
    kind.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: byId("out") }));
    expect(h.isOpen()).toBe(false);
  });

  it("closes on Escape before the page sees it, and on a click outside", () => {
    const a = anchor();
    const page = vi.fn();
    document.addEventListener("keydown", page);
    const onClose = vi.fn();
    const h = openPicker(a, { label: "Stage", items: stages, onSelect: vi.fn(), onClose });
    press(pick(h.el, ".nselect-list"), "Escape");
    expect(h.isOpen()).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(page).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(a);
    document.removeEventListener("keydown", page);

    const h2 = openPicker(a, { label: "Stage", items: stages, onSelect: vi.fn() });
    byId("out").dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(h2.isOpen()).toBe(false);
    expect(h2.el.isConnected).toBe(false);
  });

  it("takes keys from a field outside when it leaves the focus there", () => {
    document.body.innerHTML = `<textarea id="ta"></textarea>`;
    const ta = document.getElementById("ta") as HTMLTextAreaElement;
    ta.focus();
    const onSelect = vi.fn();
    const agents = ["coder", "reviewer"].map((v) => ({ value: v, label: v }));
    const h = openPicker(ta, { label: "Agents", items: agents, focus: false, onSelect });
    expect(document.activeElement).toBe(ta);
    h.setQuery("rev");
    expect(activeValue(h.el)).toBe("reviewer");
    const enter = new KeyboardEvent("keydown", { key: "Enter", cancelable: true });
    expect(h.keydown(enter)).toBe(true);
    expect(enter.defaultPrevented).toBe(true);
    expect(onSelect).toHaveBeenCalledWith(agents[1]);
    expect(h.keydown(new KeyboardEvent("keydown", { key: "Enter" }))).toBe(false);
  });

  it("closes a menu that was open, and a menu closes it", () => {
    document.body.innerHTML = `<button id="b"></button>${menuHTML([{ title: "A" }], { id: "m", labelledBy: "b" })}<button id="a"></button>`;
    const menu = bindMenu(byId("m"), byId("b"));
    menu.open();
    const h = openPicker(byId("a"), {
      label: "X",
      items: stages,
      onSelect: vi.fn(),
    });
    expect(menu.isOpen()).toBe(false);
    menu.open();
    expect(h.isOpen()).toBe(false);
    menu.close();
  });
});

describe("bindPicker", () => {
  it("wires a picker already on the page and leaves it there", () => {
    document.body.innerHTML = pickerHTML({
      id: "rp",
      label: "Repositories",
      place: "inline",
      multi: true,
      search: "Filter",
      items: [],
    });
    const el = byId("rp");
    let chosen: string[] = [];
    const items = () =>
      ["web", "api"].map((v) => ({ value: v, label: v, selected: chosen.includes(v) }));
    const h = bindPicker(el, {
      items: (q) => filterPickerItems(items(), q),
      onSelect: (i) => {
        chosen = [...chosen, i.value];
      },
    });
    expect(el.querySelectorAll(".nselect-item")).toHaveLength(2);
    (el.querySelector('[data-value="api"]') as HTMLElement).click();
    expect(chosen).toEqual(["api"]);
    expect(el.querySelector('[data-value="api"]')?.getAttribute("aria-selected")).toBe("true");
    h.close();
    expect(el.isConnected).toBe(true);
    document.body.innerHTML = "";
  });
});
