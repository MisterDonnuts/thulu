import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { swatchPickerHTML, type SwatchPickerOptions } from "../lib/ui/swatch";
import { normalize } from "./parity-normalize";
import SwatchPicker from "./SwatchPicker.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

const colours = [
  { value: "#F2994A", label: "Orange", attrs: { "data-swatch": "#F2994A" } },
  { value: "#27AE60", label: "Green <g>", attrs: { "data-swatch": "#27AE60" } },
];

describe("SwatchPicker.astro and swatchPickerHTML()", () => {
  const cases: Array<{ name: string; o: SwatchPickerOptions }> = [
    { name: "colours, none picked", o: { label: "Colour", items: colours } },
    {
      name: "boxed colours with one picked",
      o: { label: "Colour of Bug", items: colours, selected: "#27AE60", boxed: true },
    },
    {
      name: "a menu row that keeps the menu open",
      o: {
        label: "Colour",
        items: colours,
        selected: "#F2994A",
        role: "menuitemradio",
        keepOpen: true,
      },
    },
    {
      name: "glyphs with an id and extras",
      o: {
        kind: "glyph",
        boxed: true,
        label: "Icon",
        items: [
          { value: "bug", label: "Bug", icon: "tt-bug", attrs: { "data-icon": "bug" } },
          { value: "star", label: "Star", icon: "tt-star", attrs: { "data-icon": "star" } },
        ],
        selected: "star",
        id: "pick",
        cls: "wide",
        attrs: { "data-for": "bug" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.o;
      const astro = await container.renderToString(SwatchPicker, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(swatchPickerHTML(c.o)));
    });
  }

  it("refuses a kind that does not exist, like swatchPickerHTML", async () => {
    await expect(
      container.renderToString(SwatchPicker, {
        props: { label: "x", items: colours, kind: "pattern" },
      }),
    ).rejects.toThrow(/no kind "pattern"/);
  });
});
