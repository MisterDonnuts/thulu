import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML } from "../lib/ui/button";
import { propListClasses, propRowHTML, type PropRowOptions } from "../lib/ui/prop";
import { normalize } from "./parity-normalize";
import PropList from "./PropList.astro";
import PropRow from "./PropRow.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("PropRow.astro and propRowHTML()", () => {
  const edit = buttonHTML({ iconOnly: true, icon: "edit", label: "Edit", size: "sm" });
  const cases: Array<{ name: string; ts: PropRowOptions }> = [
    {
      name: "an editable value",
      ts: {
        name: "stage",
        label: "Stage",
        icon: "i-columns",
        valueHTML: '<span class="tag">Backlog</span>',
        edit: "Change the stage",
        valueAttrs: { "data-edit": "stage" },
      },
    },
    {
      name: "a read-only value with its edit button",
      ts: { name: "projects", label: "Projects", valueHTML: "<em>none</em>", trailingHTML: edit },
    },
    {
      name: "a placed row without a glyph",
      ts: {
        name: "branch",
        label: "Branch & PR",
        valueHTML: "main",
        cls: "props-wide props-folded",
        attrs: { "data-x": "1" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { valueHTML, trailingHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = { default: valueHTML };
      if (trailingHTML) slots.trailing = trailingHTML;
      const astro = await container.renderToString(PropRow, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(propRowHTML(c.ts)));
    });
  }
});

describe("PropList.astro and propListClasses()", () => {
  for (const layout of ["rows", "auto", "sheet"] as const) {
    it(`writes the ${layout} list with the same classes`, async () => {
      const astro = await container.renderToString(PropList, {
        props: { layout, class: "props", id: "ov-props" },
        slots: { default: "" },
      });
      expect(normalize(astro)).toBe(
        normalize(`<div class="${propListClasses({ layout, cls: "props" })}" id="ov-props"></div>`),
      );
    });
  }
});
