import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import {
  chipHTML,
  filterBarHTML,
  filterChipsHTML,
  filterFieldHTML,
  searchHTML,
  type ChipOptions,
  type FilterBarOptions,
  type FilterChipItem,
  type FilterChipsOptions,
  type FilterFieldOptions,
  type SearchOptions,
} from "../lib/ui/toolbar";
import Chip from "./Chip.astro";
import FilterBar from "./FilterBar.astro";
import FilterChips from "./FilterChips.astro";
import FilterField from "./FilterField.astro";
import { normalize } from "./parity-normalize";
import SearchInput from "./SearchInput.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("SearchInput.astro and searchHTML()", () => {
  const cases: Array<{ name: string; ts: SearchOptions }> = [
    { name: "default words and the / cap", ts: { id: "search" } },
    {
      name: "a placeholder and a width",
      ts: { id: "search", placeholder: "Find a project", width: 360 },
    },
    {
      name: "fluid with a label and a class",
      ts: { id: "f-q", fluid: true, label: "Search the cases", cls: "grow" },
    },
    {
      name: "no cap, type search, a value and input extras",
      ts: {
        id: "ws-q",
        kbd: false,
        type: "search",
        value: "mcp",
        attrs: { spellcheck: "false", "data-x": "1" },
      },
    },
    { name: "another key", ts: { id: "s", kbd: "F" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(SearchInput, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(searchHTML(c.ts)));
    });
  }
});

describe("Chip.astro and chipHTML()", () => {
  const cases: Array<{ name: string; ts: ChipOptions }> = [
    { name: "a bare chip", ts: { label: "Clear" } },
    {
      name: "a pressed preset with its value",
      ts: { label: "24h", pressed: true, attrs: { "data-preset": "24h" } },
    },
    {
      name: "md toggle with glyph and a hidden count",
      ts: {
        label: "Blocked",
        icon: "lock",
        size: "md",
        pressed: false,
        count: 0,
        countId: "blocked-count",
        hideZero: true,
        title: "Only blocked tasks",
        id: "blocked-btn",
      },
    },
    {
      name: "a dot and a count",
      ts: { label: "Failed", dot: "danger", count: 3, pressed: false, size: "md" },
    },
    {
      name: "menu chip with a value in force",
      ts: {
        label: "Type: bug",
        labelId: "type-btn-label",
        menu: true,
        controls: "type-menu",
        active: true,
        size: "md",
        cls: "filters-btn",
      },
    },
    { name: "disabled", ts: { label: "7d", disabled: true, pressed: false } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(Chip, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(chipHTML(c.ts)));
    });
  }

  it("refuses a menu chip that is pressed, like chipHTML", async () => {
    await expect(
      container.renderToString(Chip, { props: { label: "x", menu: true, pressed: true } }),
    ).rejects.toThrow(/not a toggle/);
  });
});

describe("FilterChips.astro and filterChipsHTML()", () => {
  const items: FilterChipItem[] = [
    { value: "all", label: "All", count: 6 },
    { value: "failed", label: "Failed", count: 1, dot: "danger", title: "Cases that failed" },
    { value: "blocked", label: "Blocked", count: 0, disabled: true },
  ];
  const cases: Array<{
    name: string;
    picked: string | string[] | null;
    ts: FilterChipsOptions;
  }> = [
    { name: "one picked, default attribute", picked: "all", ts: {} },
    {
      name: "several picked, md, a named attribute and a label",
      picked: ["failed", "blocked"],
      ts: { attr: "status", size: "md", label: "By status", id: "chips", cls: "x" },
    },
    { name: "none picked, zero counts hidden", picked: null, ts: { hideZero: true } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, ...props } = c.ts;
      const astro = await container.renderToString(FilterChips, {
        props: { ...props, class: cls, items, picked: c.picked },
      });
      expect(normalize(astro)).toBe(normalize(filterChipsHTML(items, c.picked, c.ts)));
    });
  }
});

describe("FilterField.astro and filterFieldHTML()", () => {
  const select = '<select id="f-env"><option value="">Any</option></select>';
  const cases: Array<{ name: string; ts: FilterFieldOptions }> = [
    { name: "label over its control", ts: { label: "Environment", for: "f-env", control: select } },
    {
      name: "inline, growing, with extras",
      ts: {
        label: "Project",
        for: "f-env",
        inline: true,
        grow: true,
        id: "scope",
        cls: "x",
        attrs: { hidden: true },
        control: select,
      },
    },
    {
      name: "a label for screen readers only",
      ts: { label: "Kind", for: "f-env", hideLabel: true, control: select },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, control, ...props } = c.ts;
      const astro = await container.renderToString(FilterField, {
        props: { ...props, class: cls, ...attrs },
        slots: { default: control },
      });
      expect(normalize(astro)).toBe(normalize(filterFieldHTML(c.ts)));
    });
  }
});

describe("FilterBar.astro and filterBarHTML()", () => {
  const cases: Array<{ name: string; ts: FilterBarOptions }> = [
    { name: "plain, filters only", ts: { start: '<button class="chip">a</button>' } },
    {
      name: "boxed section with a name and actions at the end",
      ts: {
        look: "boxed",
        as: "section",
        label: "Filter the usage",
        start: "<span>f</span>",
        end: "<button>Clear</button>",
      },
    },
    {
      name: "ruled div with a name, an id and extras",
      ts: {
        look: "ruled",
        label: "Filters",
        id: "bar",
        cls: "board-toolbar",
        attrs: { "data-x": "1" },
        start: "<span>f</span>",
      },
    },
    { name: "inset", ts: { look: "inset", start: "<span>f</span>", end: "<i>n</i>" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, start, end, ...props } = c.ts;
      const slots: Record<string, string> = { default: start };
      if (end) slots.end = end;
      const astro = await container.renderToString(FilterBar, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(filterBarHTML(c.ts)));
    });
  }
});
