// @vitest-environment jsdom
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { bindModal, closeTopModal, escapeCloses, topModal } from "./modal";

afterEach(() => {
  document.body.innerHTML = "";
});

function overlay(): HTMLElement {
  const el = document.createElement("div");
  el.className = "overlay";
  document.body.appendChild(el);
  return el;
}

describe("escapeCloses", () => {
  it("closes the open modal and stops the cascade", () => {
    const a = bindModal(overlay());
    const b = bindModal(overlay());
    b.open();
    const onEscape = escapeCloses(a, b);
    expect(onEscape()).toBe(true);
    expect(b.isOpen()).toBe(false);
  });

  it("lets the cascade go on when no modal is open", () => {
    const a = bindModal(overlay());
    expect(escapeCloses(a)()).toBe(false);
  });

  it("respects a modal that refuses to close", () => {
    const m = bindModal(overlay(), { beforeClose: () => false });
    m.open();
    expect(escapeCloses(m)()).toBe(true);
    expect(m.isOpen()).toBe(true);
  });
});

function dialog(): { overlay: HTMLElement; box: HTMLElement; field: HTMLInputElement } {
  const el = overlay();
  const box = document.createElement("div");
  box.className = "modal";
  const field = document.createElement("input");
  box.appendChild(field);
  el.appendChild(box);
  return { overlay: el, box, field };
}

const nextFrame = () => new Promise((r) => requestAnimationFrame(() => r(undefined)));

describe("closeTopModal", () => {
  it("closes the modal opened last, one per press", () => {
    const a = bindModal(dialog().overlay);
    const b = bindModal(dialog().overlay);
    a.open();
    b.open();
    expect(closeTopModal(document.body)).toBe(true);
    expect(b.isOpen()).toBe(false);
    expect(a.isOpen()).toBe(true);
    expect(closeTopModal(document.body)).toBe(true);
    expect(a.isOpen()).toBe(false);
    expect(closeTopModal(document.body)).toBe(false);
  });

  it("puts a modal opened again back on top", () => {
    const a = bindModal(dialog().overlay);
    const b = bindModal(dialog().overlay);
    a.open();
    b.open();
    a.open();
    expect(topModal()).not.toBe(null);
    closeTopModal(null);
    expect(a.isOpen()).toBe(false);
    expect(b.isOpen()).toBe(true);
  });

  it("forgets a modal closed some other way", () => {
    const a = bindModal(dialog().overlay);
    const b = bindModal(dialog().overlay);
    a.open();
    b.open();
    b.close();
    closeTopModal(null);
    expect(a.isOpen()).toBe(false);
  });

  it("closes when the key was pressed inside the modal", () => {
    const d = dialog();
    const m = bindModal(d.overlay);
    m.open();
    expect(closeTopModal(d.field)).toBe(true);
    expect(m.isOpen()).toBe(false);
  });

  it("leaves a key pressed in another layer to that layer", () => {
    const m = bindModal(dialog().overlay);
    m.open();
    const confirm = document.createElement("button");
    document.body.appendChild(confirm);
    expect(closeTopModal(confirm)).toBe(false);
    expect(m.isOpen()).toBe(true);
  });

  it("leaves a modal that is not dismissable to its page", () => {
    const m = bindModal(dialog().overlay, { dismissable: false });
    m.open();
    expect(closeTopModal(null)).toBe(false);
    expect(m.isOpen()).toBe(true);
  });

  it("goes through beforeClose and still takes the key", () => {
    let ask = 0;
    const m = bindModal(dialog().overlay, {
      beforeClose: () => {
        ask++;
        return false;
      },
    });
    m.open();
    expect(closeTopModal(null)).toBe(true);
    expect(ask).toBe(1);
    expect(m.isOpen()).toBe(true);
  });

  it("runs onClose like any other close", () => {
    let closed = 0;
    const m = bindModal(dialog().overlay, { onClose: () => closed++ });
    m.open();
    closeTopModal(null);
    expect(closed).toBe(1);
  });

  it("skips a modal whose page went away", () => {
    const a = bindModal(dialog().overlay);
    const gone = dialog().overlay;
    const b = bindModal(gone);
    a.open();
    b.open();
    gone.remove();
    expect(topModal()).not.toBe(gone);
    closeTopModal(null);
    expect(a.isOpen()).toBe(false);
  });
});

describe("focus", () => {
  it("puts the focus on the box when no field asks for it", async () => {
    const d = dialog();
    const m = bindModal(d.overlay);
    m.open();
    await nextFrame();
    expect(document.activeElement).toBe(d.box);
    expect(d.box.tabIndex).toBe(-1);
  });

  it("focuses the field the page names", async () => {
    const d = dialog();
    bindModal(d.overlay).open(d.field);
    await nextFrame();
    expect(document.activeElement).toBe(d.field);
  });

  it("keeps a field the page focused before the frame", async () => {
    const d = dialog();
    bindModal(d.overlay).open();
    d.field.focus();
    await nextFrame();
    expect(document.activeElement).toBe(d.field);
  });

  it("gives the focus back to what opened the modal", async () => {
    const trigger = document.createElement("button");
    document.body.appendChild(trigger);
    trigger.focus();
    const d = dialog();
    const m = bindModal(d.overlay);
    m.open(d.field);
    await nextFrame();
    m.close();
    expect(document.activeElement).toBe(trigger);
  });

  it("leaves the focus where the page moved it", async () => {
    const trigger = document.createElement("button");
    const elsewhere = document.createElement("button");
    document.body.append(trigger, elsewhere);
    trigger.focus();
    const d = dialog();
    const m = bindModal(d.overlay, { onClose: () => elsewhere.focus() });
    m.open(d.field);
    await nextFrame();
    m.close();
    expect(document.activeElement).toBe(elsewhere);
  });
});

describe("Tab trap", () => {
  const proto = HTMLElement.prototype;
  const own = Object.getOwnPropertyDescriptor(proto, "offsetParent");
  beforeAll(() => {
    Object.defineProperty(proto, "offsetParent", {
      configurable: true,
      get() {
        return document.body;
      },
    });
  });
  afterAll(() => {
    if (own) Object.defineProperty(proto, "offsetParent", own);
  });

  function tab(on: HTMLElement, shiftKey = false): KeyboardEvent {
    on.focus();
    const e = new KeyboardEvent("keydown", {
      key: "Tab",
      shiftKey,
      bubbles: true,
      cancelable: true,
    });
    on.dispatchEvent(e);
    return e;
  }

  it("wraps from the last button to the first, past the icons in it", () => {
    const o = overlay();
    o.innerHTML = `<div class="modal" tabindex="-1">
      <button id="first">Close</button>
      <a id="link" href="/x">Docs</a>
      <button id="last"><svg><use href="#i-play"></use></svg>Run</button>
    </div>`;
    const m = bindModal(o);
    m.open();
    const e = tab(document.getElementById("last") as HTMLElement);
    expect(e.defaultPrevented).toBe(true);
    expect(document.activeElement?.id).toBe("first");
  });

  it("wraps backwards from the first to the last", () => {
    const o = overlay();
    o.innerHTML = `<div class="modal" tabindex="-1">
      <button id="first"><svg><use href="#i-x"></use></svg></button>
      <button id="last">Run</button>
    </div>`;
    bindModal(o).open();
    const e = tab(document.getElementById("first") as HTMLElement, true);
    expect(e.defaultPrevented).toBe(true);
    expect(document.activeElement?.id).toBe("last");
  });
});
