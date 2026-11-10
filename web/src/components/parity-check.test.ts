import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { checkGroupHTML, checkHTML, type CheckOptions } from "../lib/ui/check";
import Check from "./Check.astro";
import CheckGroup from "./CheckGroup.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

function astroInput(o: CheckOptions): {
  props: Record<string, unknown>;
  slots: Record<string, string>;
} {
  const { cls, attrs, inputCls, labelHTML, hintHTML, trailingHTML, ...props } = o;
  const slots: Record<string, string> = {};
  if (labelHTML !== undefined) slots.default = labelHTML;
  if (hintHTML !== undefined) slots.hint = hintHTML;
  if (trailingHTML !== undefined) slots.trailing = trailingHTML;
  return { props: { ...props, class: cls, inputClass: inputCls, ...attrs }, slots };
}

describe("Check.astro and checkHTML()", () => {
  const cases: Array<{ name: string; ts: CheckOptions }> = [
    { name: "a plain checkbox", ts: { label: "Running only" } },
    {
      name: "a checked one with an id and a hint",
      ts: {
        id: "deleg",
        name: "deleg",
        label: "Let chats act",
        hint: "For 8 hours",
        checked: true,
      },
    },
    {
      name: "a radio card with a glyph",
      ts: {
        type: "radio",
        card: true,
        name: "source",
        value: "git",
        icon: "branch",
        label: "Git repository",
        hint: "Clone from GitHub",
        checked: true,
      },
    },
    {
      name: "a disabled card with its reason",
      ts: { id: "sk", card: true, label: "api-style", disabled: true, reason: "Not wired" },
    },
    {
      name: "hooks on the input and the label",
      ts: {
        label: "deploy",
        value: "deploy",
        inputCls: "perm-box",
        inputAttrs: { "data-perm": "deploy" },
        cls: "perm-row",
        attrs: { "data-flag-row": "gate", title: "Deploy" },
      },
    },
    {
      name: "markup in the label, the hint and the trailing slot",
      ts: {
        labelHTML: "<code>push</code>",
        hintHTML: '<a href="/docs">docs</a>',
        trailingHTML: '<span class="badge">Project</span>',
      },
    },
    { name: "a list head", ts: { head: true, label: "GitHub tools", checked: true } },
    {
      name: "an empty error line and a full one",
      ts: { id: "f", label: "Gate", errorId: "f-err" },
    },
    { name: "an error to show", ts: { label: "Gate", error: "Two gates in a row" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Check, astroInput(c.ts));
      expect(normalize(astro)).toBe(normalize(checkHTML(c.ts)));
    });
  }

  it("refuses a reason on an enabled check, like checkHTML", async () => {
    await expect(
      container.renderToString(Check, { props: { label: "x", reason: "Off" } }),
    ).rejects.toThrow(/disabled check/);
  });
});

describe("CheckGroup.astro and checkGroupHTML()", () => {
  const items: CheckOptions[] = [
    { type: "radio", name: "source", value: "git", card: true, label: "Git", checked: true },
    { type: "radio", name: "source", value: "upload", card: true, label: "Upload" },
  ];
  const slot = items.map((i) => checkHTML(i)).join("");

  it("a radio grid named by its heading", async () => {
    const astro = await container.renderToString(CheckGroup, {
      props: { type: "radio", layout: "grid", labelledBy: "m-source-label" },
      slots: { default: slot },
    });
    expect(normalize(astro)).toBe(
      normalize(
        checkGroupHTML({ type: "radio", layout: "grid", labelledBy: "m-source-label", items }),
      ),
    );
  });

  it("a scrolling list with a label, an id and extras", async () => {
    const astro = await container.renderToString(CheckGroup, {
      props: { layout: "list", label: "Grants", id: "g", class: "grant-box", "data-x": "1" },
      slots: { default: "<p>none</p>" },
    });
    expect(normalize(astro)).toBe(
      normalize(
        checkGroupHTML({
          layout: "list",
          label: "Grants",
          id: "g",
          cls: "grant-box",
          attrs: { "data-x": "1" },
          itemsHTML: "<p>none</p>",
        }),
      ),
    );
  });

  it("a wide grid of checkboxes", async () => {
    const astro = await container.renderToString(CheckGroup, {
      props: { layout: "grid", wide: true, label: "Skills" },
    });
    expect(normalize(astro)).toBe(
      normalize(checkGroupHTML({ layout: "grid", wide: true, label: "Skills", itemsHTML: "" })),
    );
  });
});
