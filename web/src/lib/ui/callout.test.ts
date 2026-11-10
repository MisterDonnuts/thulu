// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { buttonHTML } from "./button";
import { calloutClasses, calloutEl, calloutHTML } from "./callout";

describe("calloutClasses", () => {
  it("writes callout, the tone, then the band and the extras", () => {
    expect(calloutClasses()).toBe("callout callout-info");
    expect(calloutClasses({ tone: "warn" })).toBe("callout callout-warn");
    expect(calloutClasses({ tone: "special" })).toBe("callout callout-special");
    expect(calloutClasses({ tone: "danger", band: true, cls: "dr-attn" })).toBe(
      "callout callout-danger callout-band dr-attn",
    );
  });

  it("throws on a tone that does not exist", () => {
    expect(() => calloutClasses({ tone: "error" as never })).toThrow(/no tone "error"/);
  });

  it("writes bare after the tone, and refuses a bare band", () => {
    expect(calloutClasses({ tone: "warn", bare: true, cls: "readonly-note" })).toBe(
      "callout callout-warn callout-bare readonly-note",
    );
    expect(() => calloutClasses({ band: true, bare: true })).toThrow(/not both/);
  });
});

describe("a bare callout", () => {
  it("is a paragraph with its text straight inside, for a script to fill", () => {
    expect(
      calloutHTML({
        tone: "warn",
        bare: true,
        id: "ro",
        body: "Read <only>",
        attrs: { role: "note" },
      }),
    ).toBe(
      '<p id="ro" class="callout callout-warn callout-bare" role="note">Read &lt;only&gt;</p>',
    );
    expect(calloutHTML({ tone: "danger", bare: true, attrs: { hidden: true } })).toBe(
      '<p class="callout callout-danger callout-bare" hidden></p>',
    );
  });

  it("refuses what only a full callout holds", () => {
    expect(() => calloutHTML({ bare: true, icon: "warn", body: "x" })).toThrow(/text only/);
    expect(() => calloutHTML({ bare: true, title: "T" })).toThrow(/text only/);
    expect(() => calloutHTML({ bare: true, detail: { summary: "s", text: "t" } })).toThrow(
      /text only/,
    );
    expect(() => calloutHTML({ bare: true, actionsHTML: "<button></button>" })).toThrow(
      /text only/,
    );
  });
});

describe("calloutHTML", () => {
  it("writes the body alone when there is nothing else", () => {
    expect(calloutHTML({ body: "Only an operator can change this." })).toBe(
      '<div class="callout callout-info"><div class="callout-body">' +
        '<div class="callout-text">Only an operator can change this.</div></div></div>',
    );
  });

  it("puts the glyph first and the title above the text", () => {
    const el = calloutEl({ tone: "warn", icon: "warn", title: "Read only", body: "Ask an admin." });
    const [ico, body] = Array.from(el.children);
    expect(ico.getAttribute("class")).toBe("ico callout-ico");
    expect(ico.getAttribute("aria-hidden")).toBe("true");
    expect(ico.querySelector("use")?.getAttribute("href")).toBe("#i-warn");
    expect(body.className).toBe("callout-body");
    expect(body.querySelector(".callout-title")?.textContent).toBe("Read only");
    expect(body.querySelector(".callout-title")?.tagName).toBe("P");
    expect(body.querySelector(".callout-text")?.textContent).toBe("Ask an admin.");
  });

  it("escapes the title, the body and the detail", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const el = calloutEl({ title: evil, body: evil, detail: { summary: evil, text: evil } });
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector(".callout-title")?.textContent).toBe(evil);
    expect(el.querySelector(".callout-text")?.textContent).toBe(evil);
    expect(el.querySelector("summary")?.textContent).toBe(evil);
    expect(el.querySelector("pre")?.textContent).toBe(evil);
  });

  it("writes bodyHTML as is, and prefers body when both are given", () => {
    const list = "<ul><li><b>db</b> is down</li></ul>";
    const el = calloutEl({ bodyHTML: list });
    expect(el.querySelector(".callout-text ul li b")?.textContent).toBe("db");
    expect(calloutEl({ body: "plain", bodyHTML: list }).querySelector("ul")).toBeNull();
  });

  it("folds the detail into a details block after the text", () => {
    const el = calloutEl({
      tone: "danger",
      body: "The clone failed.",
      detail: { summary: "Technical detail", text: "fatal: auth\n  at line 2" },
    });
    const details = el.querySelector(
      ".callout-body > details.callout-detail",
    ) as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(details.previousElementSibling?.className).toBe("callout-text");
    expect(details.querySelector("pre")?.textContent).toBe("fatal: auth\n  at line 2");
  });

  it("puts the actions after the body, outside it", () => {
    const el = calloutEl({
      tone: "accent",
      body: "Map these variables?",
      actionsHTML:
        buttonHTML({ label: "Accept", size: "sm", variant: "primary" }) +
        buttonHTML({ label: "Dismiss", size: "sm" }),
    });
    const actions = el.lastElementChild as HTMLElement;
    expect(actions.className).toBe("callout-actions");
    expect(actions.previousElementSibling?.className).toBe("callout-body");
    expect(Array.from(actions.querySelectorAll("button")).map((b) => b.textContent)).toEqual([
      "Accept",
      "Dismiss",
    ]);
  });

  it("writes the id and the caller's attributes after its own", () => {
    const html = calloutHTML({
      id: "stale",
      tone: "warn",
      band: true,
      body: "Offline",
      attrs: { role: "status", hidden: true, "data-kind": "stale" },
    });
    expect(
      html.startsWith(
        '<div id="stale" class="callout callout-warn callout-band" role="status" hidden data-kind="stale">',
      ),
    ).toBe(true);
  });

  it("leaves out the text block when there is no body", () => {
    const el = calloutEl({ title: "Done" });
    expect(el.querySelector(".callout-text")).toBeNull();
    expect(el.querySelector(".callout-actions")).toBeNull();
    expect(el.querySelector(".callout-ico")).toBeNull();
  });
});
