// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";

import { t } from "../../i18n";
import { buttonHTML } from "./button";
import {
  modalCancelButton,
  modalClasses,
  modalEl,
  modalHTML,
  modalPrimaryButton,
  overlayClasses,
} from "./modal";

function parse(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

describe("modalClasses", () => {
  it("writes modal, then the size when it is not sm, then the extras", () => {
    expect(modalClasses()).toBe("modal");
    expect(modalClasses({ size: "sm" })).toBe("modal");
    expect(modalClasses({ size: "md" })).toBe("modal modal-md");
    expect(modalClasses({ size: "lg", cls: "dash-editor" })).toBe("modal modal-lg dash-editor");
    expect(modalClasses({ size: "xl" })).toBe("modal modal-xl");
  });

  it("gives a sheet its class and no size", () => {
    expect(modalClasses({ variant: "sheet", size: "xl" })).toBe("modal modal-sheet");
    expect(overlayClasses("sheet")).toBe("overlay overlay-sheet");
    expect(overlayClasses()).toBe("overlay");
  });

  it("gives a viewer its class, no size and the plain overlay", () => {
    expect(modalClasses({ variant: "viewer", size: "lg", cls: "lb" })).toBe(
      "modal modal-viewer lb",
    );
    expect(overlayClasses("viewer")).toBe("overlay");
  });

  it("refuses a size or a variant that does not exist", () => {
    // @ts-expect-error not a size
    expect(() => modalClasses({ size: "wide" })).toThrow(/no size "wide"/);
    // @ts-expect-error not offered to new code
    expect(() => modalClasses({ variant: "plain" })).toThrow(/no variant "plain"/);
  });
});

describe("modal foot buttons", () => {
  it("writes Cancel with the Esc hint and the close hook", () => {
    const html = buttonHTML(modalCancelButton());
    const b = parse(html);
    expect(b.textContent).toContain(t("common.buttons.cancel"));
    expect(b.hasAttribute("data-modal-close")).toBe(true);
    expect(b.getAttribute("aria-keyshortcuts")).toBe(t("common.kbd.esc"));
    expect(b.querySelector("kbd")?.textContent).toBe(t("common.kbd.esc"));
    expect(parse(buttonHTML(modalCancelButton("Close"))).textContent).toContain("Close");
  });

  it("makes the main action primary unless it deletes", () => {
    expect(modalPrimaryButton({ label: "Save" }).variant).toBe("primary");
    const del = parse(buttonHTML(modalPrimaryButton({ label: "Delete", variant: "danger" })));
    expect(del.className).toBe("btn btn-danger");
  });

  it("sends a form from a foot outside it", () => {
    const b = parse(
      buttonHTML(
        modalPrimaryButton({
          label: "Rename",
          type: "submit",
          form: "rename-form",
          disabled: true,
        }),
      ),
    );
    expect(b.getAttribute("type")).toBe("submit");
    expect(b.getAttribute("form")).toBe("rename-form");
    expect(b.hasAttribute("disabled")).toBe(true);
  });
});

describe("modalHTML", () => {
  it("writes the overlay, the dialog and its heading", () => {
    const el = parse(modalHTML({ id: "rename", title: "Rename <it>" }));
    expect(el.id).toBe("rename");
    expect(el.className).toBe("overlay");
    expect(el.getAttribute("role")).toBe("presentation");
    const box = el.firstElementChild as HTMLElement;
    expect(box.className).toBe("modal");
    expect(box.getAttribute("role")).toBe("dialog");
    expect(box.getAttribute("aria-modal")).toBe("true");
    expect(box.getAttribute("tabindex")).toBe("-1");
    expect(box.getAttribute("aria-labelledby")).toBe("rename-title");
    expect(box.hasAttribute("aria-describedby")).toBe(false);
    const h = el.querySelector("#rename-title");
    expect(h?.tagName).toBe("H3");
    expect(h?.textContent).toBe("Rename <it>");
    expect(el.querySelector(".modal-foot")).toBeNull();
    expect(el.querySelector("[data-modal-close]")).toBeNull();
  });

  it("escapes what it writes and keeps markup it is handed", () => {
    const html = modalHTML({
      id: "m",
      title: '"><script>',
      lede: "<b>not bold</b>",
      bodyHTML: '<input id="m-name">',
    });
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("<b>");
    expect(parse(html).querySelector(".modal-body #m-name")).not.toBeNull();
  });

  it("writes the lede first in the body and names it the description", () => {
    const el = parse(modalHTML({ id: "m", title: "T", lede: "Why", bodyHTML: "<p>x</p>" }));
    const body = el.querySelector(".modal-body") as HTMLElement;
    const lede = body.firstElementChild as HTMLElement;
    expect(lede.className).toBe("modal-lede");
    expect(lede.id).toBe("m-lede");
    expect(el.querySelector(".modal")?.getAttribute("aria-describedby")).toBe("m-lede");
  });

  it("puts the glyph, the head markup and the ✕ in the head", () => {
    const el = parse(
      modalHTML({
        id: "m",
        title: "T",
        icon: "edit",
        closable: true,
        headHTML: '<span class="badge">beta</span>',
      }),
    );
    const head = el.querySelector(".modal-head") as HTMLElement;
    const kids = Array.from(head.children).map((c) => c.tagName.toLowerCase());
    expect(kids).toEqual(["svg", "h3", "span", "button"]);
    expect(head.querySelector("svg")?.getAttribute("class")).toContain("ico-lg");
    const x = head.querySelector("button") as HTMLElement;
    expect(x.className).toBe("icon-btn");
    expect(x.getAttribute("aria-label")).toBe(t("common.buttons.close"));
    expect(x.hasAttribute("data-modal-close")).toBe(true);
  });

  it("orders the foot: start, extra actions, Cancel, main action", () => {
    const el = parse(
      modalHTML({
        id: "m",
        title: "T",
        cancel: true,
        primary: { label: "Save", id: "m-save" },
        actionsHTML: '<button id="m-test">Test</button>',
        actionsStartHTML: '<button id="m-del">Delete</button>',
      }),
    );
    const foot = el.querySelector(".modal-foot") as HTMLElement;
    const order = Array.from(foot.children).map((c) => c.id || c.className);
    expect(order).toEqual(["modal-foot-start", "m-test", "btn", "m-save"]);
    expect(foot.querySelector(".modal-foot-start #m-del")).not.toBeNull();
    expect((foot.querySelector("#m-save") as HTMLElement).className).toBe("btn btn-primary");
  });

  it("lets a whole foot win over the actions", () => {
    const el = parse(
      modalHTML({ id: "m", title: "T", cancel: true, footHTML: '<span id="own">x</span>' }),
    );
    const foot = el.querySelector(".modal-foot") as HTMLElement;
    expect(foot.children).toHaveLength(1);
    expect(foot.firstElementChild?.id).toBe("own");
  });

  it("writes the size, the box and body classes and the overlay's attributes", () => {
    const el = parse(
      modalHTML({
        id: "m",
        title: "T",
        size: "lg",
        cls: "dash-modal",
        bodyClass: "dash-body",
        attrs: { "data-kind": "dash" },
      }),
    );
    expect(el.dataset.kind).toBe("dash");
    expect(el.querySelector(".modal")?.className).toBe("modal modal-lg dash-modal");
    expect(el.querySelector(".modal-body")?.className).toBe("modal-body dash-body");
  });

  it("writes a viewer with controls in its head", () => {
    const el = parse(
      modalHTML({
        id: "lb",
        title: "shot.png",
        variant: "viewer",
        closable: true,
        headHTML: '<span id="lb-count">1 / 3</span>',
        bodyHTML: '<div id="lb-stage"></div>',
      }),
    );
    expect(el.className).toBe("overlay");
    expect(el.querySelector(".modal")?.className).toBe("modal modal-viewer");
    expect(el.querySelector(".modal-head h3")?.textContent).toBe("shot.png");
    expect(el.querySelector(".modal-head #lb-count + [data-modal-close]")).not.toBeNull();
    expect(el.querySelector(".modal-body > #lb-stage")).not.toBeNull();
    expect(el.querySelector(".modal-foot")).toBeNull();
  });

  it("docks a sheet to the bottom", () => {
    const el = parse(modalHTML({ id: "f", title: "Filters", variant: "sheet", closable: true }));
    expect(el.className).toBe("overlay overlay-sheet");
    expect(el.querySelector(".modal")?.className).toBe("modal modal-sheet");
  });
});

describe("modalEl", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("appends the overlay to the body, closed", () => {
    const el = modalEl({ id: "made", title: "T" });
    expect(document.getElementById("made")).toBe(el);
    expect(el.classList.contains("open")).toBe(false);
  });
});
