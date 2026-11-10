// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { replaceURL } from "../page";
import {
  activeTab,
  bindTabs,
  tabFromHash,
  tabIds,
  tabKeyTarget,
  tabPanelAttrs,
  tabsClasses,
  tabsHTML,
  type TabsOptions,
} from "./tabs";
import { attrsHTML } from "./dom";

const items = [
  { id: "overview", label: "Overview" },
  { id: "runs", label: "Runs", count: 3 },
  { id: "cache", label: "Cache", flag: "Needs a look" },
];

describe("tabsClasses", () => {
  it("writes tabs and the variant, then extras", () => {
    expect(tabsClasses()).toBe("tabs");
    expect(tabsClasses({ variant: "pill" })).toBe("tabs tabs-pill");
    expect(tabsClasses({ variant: "vertical", cls: "wsp-side" })).toBe(
      "tabs tabs-vertical wsp-side",
    );
  });

  it("refuses a variant that does not exist", () => {
    expect(() => tabsClasses({ variant: "folder" as never })).toThrow(/no variant "folder"/);
  });
});

describe("tabIds and activeTab", () => {
  it("derives the ids from the item, with the prefix, unless the item names them", () => {
    expect(tabIds({ id: "runs", label: "" })).toEqual({
      tab: "tab-runs",
      panel: undefined,
      count: "tab-runs-count",
      flag: "tab-runs-flag",
    });
    expect(tabIds({ id: "runs", label: "" }, { idPrefix: "a-", panels: true }).panel).toBe(
      "a-panel-runs",
    );
    expect(tabIds({ id: "x", label: "", tabId: "t", panelId: "p" })).toMatchObject({
      tab: "t",
      panel: "p",
      count: "t-count",
    });
  });

  it("selects the named tab, or the first enabled one", () => {
    expect(activeTab(items, "runs")).toBe("runs");
    expect(activeTab(items, "nope")).toBe("overview");
    expect(
      activeTab(
        [
          { id: "a", label: "", disabled: true },
          { id: "b", label: "" },
        ],
        "a",
      ),
    ).toBe("b");
    expect(activeTab([])).toBeUndefined();
  });
});

describe("tabsHTML", () => {
  const o: TabsOptions = { label: "Infra", items, active: "runs", panels: true, id: "infra-tabs" };

  it("writes a tablist with roles, selection and a roving tab order", () => {
    const html = tabsHTML(o);
    expect(html).toMatch(/^<div id="infra-tabs" class="tabs" role="tablist" aria-label="Infra">/);
    expect(html).toContain(
      '<button type="button" class="tab" role="tab" id="tab-overview" aria-selected="false" aria-controls="panel-overview" tabindex="-1" data-tab="overview">',
    );
    expect(html).toContain(
      '<button type="button" class="tab" role="tab" id="tab-runs" aria-selected="true" aria-controls="panel-runs" data-tab="runs">',
    );
  });

  it("writes the count as a pill that hides at zero, and the flag with its words", () => {
    const html = tabsHTML(o);
    expect(html).toContain('<span id="tab-runs-count" class="count-pill">3</span>');
    expect(html).toContain('<span class="tab-flag" id="tab-cache-flag"><svg class="ico"');
    expect(html).toContain('<span class="visually-hidden">Needs a look</span>');
    const zero = tabsHTML({ label: "x", items: [{ id: "a", label: "A", count: 0 }] });
    expect(zero).toContain('<span id="tab-a-count" class="count-pill" hidden>0</span>');
    const hiddenFlag = tabsHTML({
      label: "x",
      items: [{ id: "a", label: "A", flag: "f", flagHidden: true }],
    });
    expect(hiddenFlag).toContain('<span class="tab-flag" id="tab-a-flag" hidden>');
  });

  it("selects none with active null, the first enabled tab keeping the tab stop", () => {
    const html = tabsHTML({
      label: "x",
      active: null,
      items: [
        { id: "a", label: "A", disabled: true },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
    });
    expect(html).not.toContain('aria-selected="true"');
    expect(html).toContain('id="tab-b" aria-selected="false" data-tab="b"');
    expect(html).toContain('id="tab-c" aria-selected="false" tabindex="-1"');
  });

  it("writes a badge after the label, before the count, with its words escaped", () => {
    const html = tabsHTML({
      label: "x",
      items: [
        { id: "a", label: "Pagos", badge: { label: "Proposed <i>", tone: "accent" }, count: 2 },
      ],
    });
    expect(html).toContain(
      '<span>Pagos</span><span class="badge badge-accent">Proposed &lt;i&gt;</span><span id="tab-a-count"',
    );
    const plain = tabsHTML({
      label: "x",
      items: [{ id: "a", label: "A", badge: { label: "New" } }],
    });
    expect(plain).toContain('<span class="badge">New</span>');
  });

  it("puts a small icon or a dot before the label, the dot coloured only by a hex", () => {
    const html = tabsHTML({
      label: "x",
      items: [
        { id: "a", label: "A", icon: "list" },
        { id: "b", label: "B", dot: true, dotColor: "#4c8bf5", icon: "list" },
        { id: "c", label: "C", dot: "ok", dotColor: "red;background:url(x)" },
      ],
    });
    expect(html).toContain(
      '<svg class="ico ico-sm" aria-hidden="true"><use href="#i-list"/></svg><span>A</span>',
    );
    expect(html).toContain(
      '<span class="dot" aria-hidden="true" style="--dot-color:#4c8bf5"></span><span>B</span>',
    );
    expect(html).toContain('<span class="dot dot-ok" aria-hidden="true"></span><span>C</span>');
  });

  it("puts the label's id on its span, for a script that renames the tab", () => {
    const html = tabsHTML({
      label: "x",
      items: [{ id: "containers", label: "Containers", labelId: "tab-containers-label" }],
    });
    expect(html).toContain('<span id="tab-containers-label">Containers</span>');
    expect(tabsHTML({ label: "x", items: [{ id: "a", label: "A" }] })).toContain("<span>A</span>");
  });

  it("escapes labels, ids and the strip's name", () => {
    const html = tabsHTML({ label: 'a"b', items: [{ id: 'x"y', label: "<b>" }] });
    expect(html).toContain('aria-label="a&quot;b"');
    expect(html).toContain('data-tab="x&quot;y"');
    expect(html).toContain("<span>&lt;b&gt;</span>");
  });

  it("marks a vertical list's orientation and a disabled tab", () => {
    const html = tabsHTML({
      variant: "vertical",
      label: "x",
      items: [
        { id: "a", label: "A" },
        { id: "b", label: "B", disabled: true },
      ],
    });
    expect(html).toContain(
      'class="tabs tabs-vertical" role="tablist" aria-label="x" aria-orientation="vertical"',
    );
    expect(html).toContain('data-tab="b" disabled>');
  });
});

describe("tabPanelAttrs", () => {
  it("names the panel's tab and hides the panels not selected", () => {
    expect(attrsHTML(tabPanelAttrs({ id: "runs", label: "" }, false))).toBe(
      ' id="panel-runs" role="tabpanel" aria-labelledby="tab-runs" hidden',
    );
    expect(attrsHTML(tabPanelAttrs({ id: "runs", label: "" }, true, { idPrefix: "a-" }))).toBe(
      ' id="a-panel-runs" role="tabpanel" aria-labelledby="a-tab-runs"',
    );
  });
});

describe("tabKeyTarget and tabFromHash", () => {
  it("moves with the arrows, wrapping, and jumps with Home and End", () => {
    expect(tabKeyTarget("ArrowRight", 0, 3)).toBe(1);
    expect(tabKeyTarget("ArrowRight", 2, 3)).toBe(0);
    expect(tabKeyTarget("ArrowLeft", 0, 3)).toBe(2);
    expect(tabKeyTarget("Home", 2, 3)).toBe(0);
    expect(tabKeyTarget("End", 0, 3)).toBe(2);
    expect(tabKeyTarget("ArrowDown", 0, 3)).toBe(-1);
    expect(tabKeyTarget("ArrowDown", 0, 3, true)).toBe(1);
    expect(tabKeyTarget("ArrowUp", 0, 3, true)).toBe(2);
    expect(tabKeyTarget("Enter", 0, 3)).toBe(-1);
  });

  it("reads a tab id from the hash only when that tab exists", () => {
    expect(tabFromHash("#runs", ["overview", "runs"])).toBe("runs");
    expect(tabFromHash("#nope", ["overview", "runs"])).toBeUndefined();
    expect(tabFromHash("", ["overview"])).toBeUndefined();
  });
});

describe("bindTabs", () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
  });
  afterEach(() => {
    document.body.innerHTML = "";
    replaceURL("/infra/");
  });

  const el = <T extends HTMLElement = HTMLElement>(sel: string): T => {
    const found = document.querySelector<T>(sel);
    if (!found) throw new Error(`no ${sel}`);
    return found;
  };

  function mount(o: Partial<TabsOptions> = {}) {
    const opts: TabsOptions = { label: "x", items, panels: true, ...o };
    document.body.innerHTML =
      tabsHTML(opts) +
      opts.items
        .map(
          (i) =>
            `<div${attrsHTML(tabPanelAttrs(i, i.id === activeTab(opts.items, opts.active)))}></div>`,
        )
        .join("");
    return el('[role="tablist"]');
  }
  const tab = (id: string) => el<HTMLButtonElement>(`[data-tab="${id}"]`);
  const panel = (id: string) => el(`#panel-${id}`);
  const key = (el: HTMLElement, k: string) =>
    el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));

  it("selects on click: aria-selected, tab order and panels follow", () => {
    const onChange = vi.fn();
    const h = bindTabs(mount(), { onChange });
    tab("runs").click();
    expect(tab("runs").getAttribute("aria-selected")).toBe("true");
    expect(tab("runs").tabIndex).toBe(0);
    expect(tab("overview").getAttribute("aria-selected")).toBe("false");
    expect(tab("overview").tabIndex).toBe(-1);
    expect(panel("runs").hidden).toBe(false);
    expect(panel("overview").hidden).toBe(true);
    expect(h.current()).toBe("runs");
    expect(onChange).toHaveBeenCalledWith("runs", { user: true });
    tab("runs").click();
    expect(onChange).toHaveBeenCalledTimes(1);
  });

  it("moves with the keys, focusing the tab, and skips a disabled one", () => {
    const list = mount({
      items: [
        { id: "a", label: "A" },
        { id: "b", label: "B", disabled: true },
        { id: "c", label: "C" },
      ],
    });
    bindTabs(list);
    tab("a").focus();
    key(tab("a"), "ArrowRight");
    expect(tab("c").getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(tab("c"));
    key(tab("c"), "ArrowRight");
    expect(tab("a").getAttribute("aria-selected")).toBe("true");
    key(tab("a"), "End");
    expect(document.activeElement).toBe(tab("c"));
    key(tab("c"), "ArrowDown");
    expect(tab("c").getAttribute("aria-selected")).toBe("true");
  });

  it("keeps a panel that several tabs name", () => {
    document.body.innerHTML =
      tabsHTML({
        variant: "vertical",
        label: "x",
        items: [
          { id: "agent", label: "A", panelId: "list" },
          { id: "skill", label: "S", panelId: "list" },
        ],
      }) + '<div id="list"></div>';
    bindTabs(el('[role="tablist"]'));
    key(tab("agent"), "ArrowDown");
    expect(tab("skill").getAttribute("aria-selected")).toBe("true");
    expect(el("#list").hidden).toBe(false);
  });

  it("with hash, starts from the URL, writes it on a pick and follows hashchange", () => {
    replaceURL("/infra/?w=1#cache");
    const onChange = vi.fn();
    const h = bindTabs(mount(), { hash: true, onChange });
    expect(h.current()).toBe("cache");
    expect(onChange).toHaveBeenCalledWith("cache", { user: false });
    tab("runs").click();
    expect(window.location.hash).toBe("#runs");
    expect(window.location.search).toBe("?w=1");
    tab("overview").click();
    expect(window.location.hash).toBe("");
    replaceURL("/infra/#cache");
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    expect(h.current()).toBe("cache");
    expect(panel("cache").hidden).toBe(false);
  });

  it("asks beforeChange and keeps the selected tab when it says no", async () => {
    const onChange = vi.fn();
    let allow = false;
    const beforeChange = vi.fn(() => Promise.resolve(allow));
    const h = bindTabs(mount(), { beforeChange, onChange });
    tab("runs").click();
    await Promise.resolve();
    await Promise.resolve();
    expect(beforeChange).toHaveBeenCalledWith("runs");
    expect(h.current()).toBe("overview");
    expect(tab("overview").getAttribute("aria-selected")).toBe("true");
    expect(panel("runs").hidden).toBe(true);
    expect(onChange).not.toHaveBeenCalled();
    allow = true;
    tab("runs").click();
    await Promise.resolve();
    await Promise.resolve();
    expect(h.current()).toBe("runs");
    expect(onChange).toHaveBeenCalledWith("runs", { user: true });
    h.select("cache");
    expect(beforeChange).toHaveBeenCalledTimes(2);
    expect(h.current()).toBe("cache");
  });

  it("selects from code without calling it the reader's pick", () => {
    const onChange = vi.fn();
    const h = bindTabs(mount(), { onChange });
    h.select("cache");
    expect(tab("cache").getAttribute("aria-selected")).toBe("true");
    expect(onChange).toHaveBeenCalledWith("cache", { user: false });
    h.select("nope");
    expect(h.current()).toBe("cache");
  });
});
