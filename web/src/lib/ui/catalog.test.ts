// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  bindItemList,
  catalogItemHTML,
  catalogLayoutClasses,
  catalogLayoutHTML,
  entityHeadHTML,
  fileRowClasses,
  fileRowHTML,
  fileStatusHTML,
  mdEditorHTML,
  saveFootHTML,
  saveStateHTML,
  setCurrentItem,
  setSaveState,
} from "./catalog";

function byId<T extends HTMLElement = HTMLElement>(id: string): T {
  const el = document.getElementById(id);
  if (!el) throw new Error(`no #${id}`);
  return el as T;
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("catalogLayoutHTML", () => {
  it("is the list column beside the editor, md by default", () => {
    expect(catalogLayoutClasses()).toBe("catalog");
    expect(catalogLayoutClasses("sm", "x")).toBe("catalog catalog-side-sm x");
    expect(() => catalogLayoutClasses("lg" as never)).toThrow(/no side "lg"/);
  });

  it("writes the toolbar and the foot only when given, the list and the editor always", () => {
    const html = catalogLayoutHTML({ listId: "agent-list", mainId: "editor", label: "Agents" });
    expect(html).toBe(
      '<div class="catalog"><aside class="catalog-side" aria-label="Agents">' +
        '<div id="agent-list" class="catalog-side-list"></div></aside>' +
        '<section id="editor" class="catalog-main"></section></div>',
    );
    const full = catalogLayoutHTML({
      side: "sm",
      toolbarHTML: "<input>",
      listHTML: "<p>a</p>",
      footHTML: "<kbd>↑</kbd>",
      mainHTML: "<p>e</p>",
    });
    expect(full).toContain('<div class="catalog-side-toolbar"><input></div>');
    expect(full).toContain('<div class="catalog-side-foot"><kbd>↑</kbd></div>');
    expect(full).toContain('<section class="catalog-main"><p>e</p></section>');
  });
});

describe("catalogItemHTML", () => {
  it("is a list row button, the description cut at two lines", () => {
    const html = catalogItemHTML({
      name: "dev<x>",
      description: "Writes & tests",
      attrs: { "data-name": "dev" },
    });
    expect(html).toBe(
      '<button class="row row-clamp-2" type="button" data-name="dev">' +
        '<span class="row-main"><span class="row-top"><span class="row-title">dev&lt;x&gt;</span></span>' +
        '<span class="row-meta">Writes &amp; tests</span></span></button>',
    );
  });

  it("marks the open item current and takes a mono name cut at one line", () => {
    const html = catalogItemHTML({ name: "git-flow", mono: true, clamp: 1, current: true });
    expect(html).toMatch(/^<button class="row row-clamp-1" type="button" aria-current="true">/);
    expect(html).toContain('class="row-title row-title-mono"');
    expect(catalogItemHTML({ name: "x", current: false })).not.toContain("aria-current");
  });
});

describe("bindItemList and setCurrentItem", () => {
  const list = () => {
    document.body.innerHTML =
      `<div id="l">` +
      ["a", "b", "c"].map((n) => catalogItemHTML({ name: n, attrs: { id: n } })).join("") +
      `</div><button id="out">x</button>`;
    return byId("l");
  };

  it("marks the item clicked current, alone, and hands it to onPick", () => {
    const onPick = vi.fn();
    bindItemList(list(), { onPick });
    byId("b").click();
    expect(onPick).toHaveBeenCalledWith(byId("b"));
    expect(byId("b").getAttribute("aria-current")).toBe("true");
    byId("c").click();
    expect(byId("b").hasAttribute("aria-current")).toBe(false);
    expect(byId("c").getAttribute("aria-current")).toBe("true");
  });

  it("walks the items with the arrows, Home and End, and keeps the keys to itself", () => {
    const l = list();
    bindItemList(l);
    const outer = vi.fn();
    document.addEventListener("keydown", outer);
    const press = (key: string) =>
      document.activeElement?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    byId("a").focus();
    press("ArrowDown");
    expect(document.activeElement?.id).toBe("b");
    press("End");
    expect(document.activeElement?.id).toBe("c");
    press("ArrowDown");
    expect(document.activeElement?.id).toBe("c");
    press("Home");
    expect(document.activeElement?.id).toBe("a");
    press("ArrowUp");
    expect(document.activeElement?.id).toBe("a");
    expect(outer).not.toHaveBeenCalled();
    press("x");
    expect(outer).toHaveBeenCalledTimes(1);
    document.removeEventListener("keydown", outer);
  });

  it("stops listening once unbound, and clears the mark with null", () => {
    const l = list();
    const onPick = vi.fn();
    const off = bindItemList(l, { onPick });
    byId("a").click();
    off();
    byId("b").click();
    expect(onPick).toHaveBeenCalledTimes(1);
    setCurrentItem(l, null);
    expect(l.querySelector("[aria-current]")).toBeNull();
  });
});

describe("entityHeadHTML", () => {
  it("writes the name, the meta, badges and sm actions", () => {
    const html = entityHeadHTML({
      title: "git-flow",
      mono: true,
      meta: "Edited <today>",
      metaId: "sk-meta",
      badgesHTML: '<span class="badge">system</span>',
      actions: [{ label: "Delete", icon: "trash", variant: "danger", id: "sk-delete" }],
    });
    expect(html).toMatch(/^<div class="entity-head"><div class="entity-head-text">/);
    expect(html).toContain('<h2 class="entity-head-title entity-head-title-mono">git-flow</h2>');
    expect(html).toContain('<p id="sk-meta" class="entity-head-meta">Edited &lt;today&gt;</p>');
    expect(html).toContain(
      '<div class="entity-head-badges"><span class="badge">system</span></div>',
    );
    expect(html).toMatch(
      /<div class="entity-head-actions"><button id="sk-delete" class="btn btn-danger btn-sm"/,
    );
  });

  it("keeps an empty meta line that has an id, and drops one that has neither", () => {
    expect(entityHeadHTML({ title: "a", metaId: "m" })).toContain(
      '<p id="m" class="entity-head-meta"></p>',
    );
    const bare = entityHeadHTML({ title: "a", level: 3 });
    expect(bare).toBe(
      '<div class="entity-head"><div class="entity-head-text"><h3 class="entity-head-title">a</h3></div></div>',
    );
  });

  it("refuses a head without a title", () => {
    expect(() => entityHeadHTML({})).toThrow(/needs a title/);
  });
});

describe("saveStateHTML and setSaveState", () => {
  it("says there are no changes, or unsaved ones with an amber dot", () => {
    const clean = saveStateHTML(false, { id: "s" });
    expect(clean).toMatch(/^<span id="s" class="save-state" role="status" data-dirty="false">/);
    expect(clean).toContain('<span class="dot" aria-hidden="true"></span>');
    const dirty = saveStateHTML(true);
    expect(dirty).toContain('data-dirty="true"');
    expect(dirty).toContain("dot-warn");
    expect(dirty).not.toBe(clean.replace(' id="s"', ""));
  });

  it("hides a quiet state while clean and shows it while dirty", () => {
    document.body.innerHTML = saveStateHTML(false, { id: "s", quiet: true });
    const el = byId("s");
    expect(el.hidden).toBe(true);
    setSaveState(el, true);
    expect(el.hidden).toBe(false);
    expect(el.dataset.dirty).toBe("true");
    expect(el.querySelector(".dot-warn")).not.toBeNull();
    setSaveState(el, false);
    expect(el.hidden).toBe(true);
  });

  it("leaves a loud state in view either way", () => {
    document.body.innerHTML = saveStateHTML(true, { id: "s" });
    const el = byId("s");
    setSaveState(el, false);
    expect(el.hidden).toBe(false);
    expect(el.querySelector(".dot-warn")).toBeNull();
  });
});

describe("saveFootHTML", () => {
  it("puts the state at the left and the buttons at the right", () => {
    const html = saveFootHTML({
      id: "ed-save-bar",
      stateId: "set-status",
      actions: [{ label: "Revert" }, { label: "Save", variant: "primary", disabled: true }],
      attrs: { hidden: true },
    });
    expect(html).toMatch(/^<div id="ed-save-bar" class="save-foot" hidden><span id="set-status"/);
    expect(html).toContain(
      '<div class="save-foot-actions"><button class="btn" type="button">Revert</button>',
    );
    expect(html).toContain('class="btn btn-primary"');
  });
});

describe("mdEditorHTML", () => {
  it("writes Back, the path, a quiet state, extra buttons and Save over the text", () => {
    const html = mdEditorHTML({
      path: "prompts/<main>.md",
      pathId: "pp-path",
      note: "(new)",
      textId: "pp-text",
      value: "# Hi & bye",
      back: { id: "pp-back" },
      stateId: "pp-state",
      buttons: [{ label: "History", icon: "clock", id: "pp-history" }],
      save: { id: "pp-save", disabled: true },
    });
    expect(html).toMatch(/^<div class="md-editor"><div class="md-editor-bar"><button id="pp-back"/);
    expect(html).toContain(
      '<span id="pp-path" class="md-editor-path">prompts/&lt;main&gt;.md<span class="md-editor-note">(new)</span></span>',
    );
    expect(html).toContain(
      '<span id="pp-state" class="save-state" role="status" data-dirty="false" data-quiet="" hidden>',
    );
    expect(html).toContain('id="pp-history" class="btn btn-sm"');
    expect(html).toContain('id="pp-save" class="btn btn-primary btn-sm"');
    expect(html).toContain(
      '<textarea id="pp-text" class="md-editor-text" spellcheck="false" aria-labelledby="pp-path"># Hi &amp; bye</textarea>',
    );
  });

  it("draws a box without Back or Save for a reader", () => {
    const html = mdEditorHTML({
      path: "a.md",
      pathId: "p",
      textId: "t",
      back: false,
      readOnly: true,
      box: true,
      save: { id: "s", hidden: true },
    });
    expect(html).toMatch(/^<div class="md-editor md-editor-box">/);
    expect(html).not.toContain("<button");
    expect(html).toContain(" readonly>");
  });
});

describe("fileRowHTML", () => {
  it("is a boxed list row: the label over its status, the actions after", () => {
    const html = fileRowHTML({
      label: "En curso",
      status: "prompts/in_progress.md",
      tone: "ok",
      actions: [{ label: "Edit", icon: "edit" }],
      attrs: { "data-stage": "in_progress" },
    });
    expect(html).toMatch(/^<div class="row row-boxed file-row" data-stage="in_progress">/);
    expect(html).toContain(
      '<span class="row-meta"><span class="file-status file-status-mono" data-tone="ok"><span class="dot dot-ok" aria-hidden="true"></span><span class="file-status-text">prompts/in_progress.md</span></span></span>',
    );
    expect(html).toContain('<span class="row-actions"><button class="btn btn-sm"');
  });

  it("takes a dashed fallback, a struck-out deletion and a status in words", () => {
    expect(fileRowClasses("dashed")).toBe("file-row file-row-dashed");
    expect(fileRowClasses("deleted", "x")).toBe("file-row file-row-deleted x");
    expect(() => fileRowClasses("gone" as never)).toThrow(/no file row "gone"/);
    expect(fileStatusHTML({ text: "Uses main.md", mono: false })).toBe(
      '<span class="file-status"><span class="file-status-text">Uses main.md</span></span>',
    );
    expect(() => fileStatusHTML({ tone: "danger" as never })).toThrow(/no file tone/);
    expect(fileRowHTML({ label: "a.md", as: "li", variant: "deleted" })).toBe(
      '<li class="row row-boxed file-row file-row-deleted"><span class="row-main"><span class="row-top"><span class="row-title">a.md</span></span></span></li>',
    );
  });
});
