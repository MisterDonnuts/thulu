import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML } from "../lib/ui/button";
import { calloutHTML, type CalloutOptions } from "../lib/ui/callout";
import { esc } from "../lib/ui/dom";
import { skeletonHTML, type SkeletonOptions } from "../lib/ui/skeleton";
import Callout from "./Callout.astro";
import { normalize } from "./parity-normalize";
import Skeleton from "./Skeleton.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Callout.astro and calloutHTML()", () => {
  const retry = buttonHTML({ label: "Retry", size: "sm", icon: "refresh" });
  const cases: Array<{ name: string; ts: CalloutOptions }> = [
    { name: "info with text only", ts: { body: "Only an operator can change this." } },
    {
      name: "warn with glyph and title",
      ts: { tone: "warn", icon: "warn", title: "Read only", body: "Ask an admin." },
    },
    {
      name: "danger with detail and an action",
      ts: {
        tone: "danger",
        body: "The clone failed: <auth> required.",
        detail: { summary: "Technical detail", text: "fatal: could not read\n  Username" },
        actionsHTML: retry,
      },
    },
    {
      name: "ok band with extras",
      ts: {
        tone: "ok",
        band: true,
        icon: "check",
        body: "2 subtasks done",
        id: "attn",
        cls: "dr-attn",
        attrs: { role: "status", "data-kind": "subtasks" },
      },
    },
    { name: "accent title alone", ts: { tone: "accent", title: "Cloning…" } },
    {
      name: "special with a glyph",
      ts: { tone: "special", icon: "warn", body: "The browser image was denied." },
    },
    {
      name: "a bare warn note, hidden",
      ts: {
        tone: "warn",
        bare: true,
        id: "board-readonly",
        cls: "readonly-note",
        body: "Read <only>",
        attrs: { role: "note", hidden: true },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { body, actionsHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (body !== undefined) slots.default = esc(body);
      if (actionsHTML) slots.actions = actionsHTML;
      const astro = await container.renderToString(Callout, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(calloutHTML(c.ts)));
    });
  }

  it("refuses a glyph on a bare callout, like calloutHTML", async () => {
    await expect(
      container.renderToString(Callout, {
        props: { bare: true, icon: "warn" },
        slots: { default: "x" },
      }),
    ).rejects.toThrow(/text only/);
  });

  it("refuses a tone that does not exist, like calloutHTML", async () => {
    await expect(
      container.renderToString(Callout, { props: { tone: "error" }, slots: { default: "x" } }),
    ).rejects.toThrow(/no tone "error"/);
  });
});

describe("Skeleton.astro and skeletonHTML()", () => {
  const cases: Array<{ name: string; ts: SkeletonOptions }> = [
    { name: "a line", ts: {} },
    { name: "a chip in a list", ts: { shape: "chip", tag: "li" } },
    {
      name: "a block with extras",
      ts: { shape: "block", tag: "div", cls: "bs-card", attrs: { id: "w" } },
    },
    { name: "a paragraph", ts: { lines: 3, cls: "desc" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(Skeleton, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(skeletonHTML(c.ts)));
    });
  }

  it("refuses lines on a chip and a tag it does not draw, like skeletonHTML", async () => {
    await expect(
      container.renderToString(Skeleton, { props: { shape: "chip", lines: 2 } }),
    ).rejects.toThrow(/only a line skeleton/);
    await expect(container.renderToString(Skeleton, { props: { tag: "td" } })).rejects.toThrow(
      /no tag "td"/,
    );
  });
});
