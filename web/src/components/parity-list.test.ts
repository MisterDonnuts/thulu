import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML } from "../lib/ui/button";
import {
  groupHeadHTML,
  listRowHTML,
  type GroupHeadOptions,
  type ListRowOptions,
} from "../lib/ui/list-row";
import {
  tableHTML,
  tableRowHTML,
  type TableColumn,
  type TableOptions,
  type TableRow,
} from "../lib/ui/table";
import GroupHead from "./GroupHead.astro";
import ListRow from "./ListRow.astro";
import { normalize } from "./parity-normalize";
import Table from "./Table.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("ListRow.astro and listRowHTML()", () => {
  const rotate = buttonHTML({ label: "Rotate", size: "sm" });
  const cases: Array<{ name: string; ts: ListRowOptions }> = [
    { name: "a title alone", ts: { title: "deploy-bot" } },
    {
      name: "every zone, dim and boxed",
      ts: {
        title: "deploy-bot",
        meta: "developer · never used",
        aside: "2 h",
        leadHTML: '<span class="dot"></span>',
        badgesHTML: '<span class="badge">revoked</span>',
        actionsHTML: rotate,
        dim: true,
        boxed: true,
        cls: "key-row",
        attrs: { id: "k1", "data-id": "k1" },
      },
    },
    {
      name: "a link to the current page",
      ts: { href: "/tasks/", title: "Tasks", current: "page" },
    },
    {
      name: "a toggle row that is open",
      ts: { as: "button", title: "Batch 3", meta: "4 runs", expanded: true, current: true },
    },
    {
      name: "a mono title that links, in a list item, revealing its actions",
      ts: {
        as: "li",
        title: "https://hooks.example/x",
        titleHref: "/webhooks/1",
        mono: true,
        reveal: true,
        actionsHTML: rotate,
      },
    },
    {
      name: "a dense row whose title wraps and carries attributes",
      ts: {
        as: "li",
        size: "sm",
        wrap: true,
        title: "Endpoint de saldo",
        titleHref: "#task-t1",
        titleAttrs: { "data-open-task": "t1", "aria-label": "Endpoint, Hecha" },
        aside: "t1",
      },
    },
    {
      name: "a row whose title takes every line it needs",
      ts: { wrap: "all", title: "checkout-api · checkout_payment_errors_total" },
    },
    {
      name: "a palette result: a link option with its meta on the title's line",
      ts: {
        href: "/agents/?a=dev",
        size: "sm",
        inline: true,
        title: "developer",
        meta: "Writes the code",
        id: "cmdk-opt-0",
        attrs: { role: "option", "aria-selected": "true", "data-index": "0" },
      },
    },
    {
      name: "a button row with tags under its clamped meta",
      ts: {
        as: "button",
        title: "developer",
        meta: "Writes the code",
        clamp: 2,
        tagsHTML: '<span class="badge">Build</span>',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, leadHTML, badgesHTML, tagsHTML, actionsHTML, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (leadHTML) slots.lead = leadHTML;
      if (tagsHTML) slots.tags = tagsHTML;
      if (badgesHTML) slots.badges = badgesHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      const astro = await container.renderToString(ListRow, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(listRowHTML(c.ts)));
    });
  }

  it("refuses actions inside a link row, like listRowHTML", async () => {
    await expect(
      container.renderToString(ListRow, {
        props: { href: "/x/", title: "x" },
        slots: { actions: "<button></button>" },
      }),
    ).rejects.toThrow(/actions inside a a row/);
  });
});

describe("GroupHead.astro and groupHeadHTML()", () => {
  const cases: Array<{ name: string; ts: GroupHeadOptions }> = [
    { name: "a title alone", ts: { title: "pagos-api" } },
    {
      name: "glyph, kicker, meta and a heading",
      ts: {
        icon: "folder",
        kicker: "System",
        title: "web-app",
        meta: "1 case",
        level: 3,
        cls: "x",
      },
    },
    {
      name: "a band in a table row with extra",
      ts: {
        as: "tr",
        cols: 6,
        band: true,
        title: "Coupons",
        extraHTML: '<span class="tag">Running</span>',
        attrs: { "data-group": "t1" },
      },
    },
    {
      name: "a title that links to the group's page",
      ts: { as: "tr", cols: 8, band: true, title: "Coupons", href: "/tasks/#task-t1" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, extraHTML, ...props } = c.ts;
      const astro = await container.renderToString(GroupHead, {
        props: { ...props, class: cls, ...attrs },
        slots: extraHTML ? { extra: extraHTML } : {},
      });
      expect(normalize(astro)).toBe(normalize(groupHeadHTML(c.ts)));
    });
  }
});

describe("Table.astro and tableHTML()", () => {
  const cols: TableColumn[] = [
    { label: "Name" },
    { label: "CPU", num: true, sort: "none", key: "cpu" },
    { label: "Id", mono: true, width: "shrink" },
    { label: "Actions", hideLabel: true },
  ];
  const rows: TableRow[] = [
    { cells: ["sandbox-1", "7%", { text: "abc", sub: "kata" }], tone: "danger", id: "a" },
    { cells: ["<agent>", 3, null, { actions: [{ label: "Stop" }] }], dim: true },
  ];
  const cases: Array<{ name: string; rows: TableRow[]; ts: TableOptions }> = [
    { name: "rows, stacking", rows, ts: { stack: true, bodyId: "tb", label: "Pods" } },
    { name: "compact and bare", rows, ts: { compact: true, top: true, bare: true, cls: "x" } },
    { name: "empty", rows: [], ts: { empty: "No pods", noHead: true } },
    { name: "loading", rows: [], ts: { loading: true, expandable: true } },
    { name: "no rows yet", rows: [], ts: { bodyId: "tb" } },
    {
      name: "expandable with an open row",
      rows: [{ cells: ["r"], id: "r1", expanded: true, detailHTML: "<p>steps</p>" }],
      ts: { expandable: true, attrs: { id: "runs" } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const body = c.rows.map((r) => tableRowHTML(cols, r, c.ts)).join("");
      const astro = await container.renderToString(Table, {
        props: { cols, ...props, class: cls, ...attrs },
        slots: body ? { default: body } : {},
      });
      expect(normalize(astro)).toBe(normalize(tableHTML(cols, c.rows, c.ts)));
    });
  }
});
