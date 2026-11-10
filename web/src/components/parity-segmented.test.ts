import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { segmentedHTML, type SegmentedOption, type SegmentedOptions } from "../lib/ui/segmented";
import { normalize } from "./parity-normalize";
import Segmented from "./Segmented.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

interface Case {
  name: string;
  group: string;
  options: SegmentedOption[];
  value?: string;
  o?: SegmentedOptions;
}

function astroProps(c: Case): Record<string, unknown> {
  const { cls, attrs, ...rest } = c.o ?? {};
  return { name: c.group, options: c.options, value: c.value, ...rest, class: cls, ...attrs };
}

const RANGES: SegmentedOption[] = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
];

describe("Segmented.astro and segmentedHTML()", () => {
  const cases: Case[] = [
    {
      name: "a small range named by a label",
      group: "range",
      options: RANGES,
      value: "1h",
      o: { size: "sm", label: "Range" },
    },
    { name: "no option chosen", group: "range", options: RANGES },
    {
      name: "glyphs only",
      group: "view",
      options: [
        { value: "grid", label: "Grid", icon: "columns" },
        { value: "list", label: "List", icon: "list" },
      ],
      value: "grid",
      o: { iconOnly: true, label: "View" },
    },
    {
      name: "glyphs with their words, filling the box",
      group: "theme",
      options: [
        { value: "system", label: "System", icon: "monitor", attrs: { title: "Follow the OS" } },
        { value: "light", label: "Light", icon: "sun" },
        { value: "dark", label: "Dark", icon: "moon" },
      ],
      value: "dark",
      o: { fill: true, labelledBy: "theme-label" },
    },
    {
      name: "cards with a disabled option, a description, an id and hooks",
      group: "r-agent:x",
      options: [
        { value: "keep", label: "Keep", inputAttrs: { "data-key": "agent:x" } },
        { value: "copy", label: "Copy", disabled: true },
        { value: "replace", label: "Replace & <b>" },
      ],
      value: "keep",
      o: {
        cards: true,
        label: "Choose",
        describedBy: "plan-d",
        id: "plan-x",
        cls: "plan-pick",
        attrs: { "data-row": "1" },
      },
    },
    {
      name: "every option off",
      group: "mode",
      options: RANGES,
      value: "15m",
      o: { disabled: true, label: "Mode" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Segmented, { props: astroProps(c) });
      expect(normalize(astro)).toBe(normalize(segmentedHTML(c.group, c.options, c.value, c.o)));
    });
  }

  it("refuses a single option, like segmentedHTML", async () => {
    await expect(
      container.renderToString(Segmented, { props: { name: "n", options: [RANGES[0]] } }),
    ).rejects.toThrow(/two options/);
  });
});
