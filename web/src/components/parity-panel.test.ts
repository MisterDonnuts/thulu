import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { badgeHTML } from "../lib/ui/badge";
import { buttonHTML } from "../lib/ui/button";
import { esc } from "../lib/ui/dom";
import { crumbsHTML, pageHeadHTML, type Crumb, type PageHeadOptions } from "../lib/ui/page-head";
import {
  panelHTML,
  sectionHeadHTML,
  type PanelOptions,
  type SectionHeadOptions,
} from "../lib/ui/panel";
import Crumbs from "./Crumbs.astro";
import PageHead from "./PageHead.astro";
import Panel from "./Panel.astro";
import { normalize } from "./parity-normalize";
import SectionHead from "./SectionHead.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Panel.astro and panelHTML()", () => {
  const check = buttonHTML({ label: "Check now", size: "sm", icon: "refresh" });
  const cases: Array<{ name: string; ts: PanelOptions }> = [
    { name: "a body alone", ts: { bodyHTML: "<p>x</p>" } },
    {
      name: "glyph, title and a flush body",
      ts: { icon: "cpu", title: "CPU <now>", body: "a & b", flush: true },
    },
    {
      name: "status, aside and actions",
      ts: {
        icon: "refresh",
        title: "Version",
        titleId: "v-h",
        as: "h3",
        statusHTML: badgeHTML("failed", "warn"),
        aside: "every 12 h",
        actionsHTML: check,
        bodyHTML: "<p>body</p>",
      },
    },
    {
      name: "the head slot",
      ts: {
        title: "Team",
        headHTML: '<span class="count-pill" id="team-count">0</span>',
        bodyHTML: "",
      },
    },
    { name: "a tray and no title", ts: { actionsHTML: check, bodyHTML: "<p>x</p>" } },
    {
      name: "small, danger, a title in markup, foot and extras",
      ts: {
        tag: "div",
        size: "sm",
        tone: "danger",
        titleHTML: "stage <b>In progress</b> in_progress",
        actionsHTML: check,
        bodyHTML: "<div>grid</div>",
        bodyCls: "grid",
        foot: "Measured <hourly>",
        id: "p1",
        cls: "detail",
        attrs: { "aria-labelledby": "x", hidden: true },
      },
    },
    {
      name: "a picked small box with a check for title and a hidden body",
      ts: {
        tag: "div",
        size: "sm",
        pick: true,
        as: "div",
        titleHTML: '<label class="check"><input class="check-input" type="checkbox"></label>',
        bodyHTML: "<div>fields</div>",
        bodyAttrs: { hidden: true, "data-aud-body": "review" },
      },
    },
    {
      name: "a small box under an h3, titled h4",
      ts: { size: "sm", as: "h4", titleHTML: "stage <b>Review</b>", bodyHTML: "<p>x</p>" },
    },
    {
      name: "a div title, pad md and a foot in markup",
      ts: {
        tag: "article",
        as: "div",
        titleHTML: '<input id="d-name" aria-label="Name">',
        pad: "md",
        asideHTML: '<a href="/tasks/">All tasks</a>',
        bodyHTML: "<p>x</p>",
        footHTML: "<span>a</span><span>b</span>",
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const {
        titleHTML,
        statusHTML,
        headHTML,
        aside,
        asideHTML,
        actionsHTML,
        body,
        bodyHTML,
        foot,
        footHTML,
        cls,
        bodyCls,
        bodyAttrs,
        attrs,
        ...props
      } = c.ts;
      const slots: Record<string, string> = {};
      if (titleHTML) slots.title = titleHTML;
      if (statusHTML) slots.status = statusHTML;
      if (headHTML) slots.head = headHTML;
      if (aside !== undefined) slots.aside = esc(aside);
      else if (asideHTML) slots.aside = asideHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      if (body !== undefined) slots.default = esc(body);
      else if (bodyHTML) slots.default = bodyHTML;
      if (foot !== undefined) slots.foot = esc(foot);
      else if (footHTML) slots.foot = footHTML;
      const astro = await container.renderToString(Panel, {
        props: { ...props, class: cls, bodyClass: bodyCls, bodyAttrs, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(panelHTML(c.ts)));
    });
  }

  it("refuses a pad on a flush body and a size that does not exist, like panelHTML", async () => {
    await expect(
      container.renderToString(Panel, {
        props: { flush: true, pad: "md" },
        slots: { default: "x" },
      }),
    ).rejects.toThrow(/flush body has no pad/);
    await expect(
      container.renderToString(Panel, { props: { size: "lg" }, slots: { default: "x" } }),
    ).rejects.toThrow(/no size "lg"/);
  });
});

describe("SectionHead.astro and sectionHeadHTML()", () => {
  const add = buttonHTML({ label: "Add", size: "sm", icon: "plus" });
  const cases: Array<{ name: string; ts: SectionHeadOptions }> = [
    { name: "a title alone", ts: { title: "Brief", id: "ov-brief-h" } },
    {
      name: "aside and actions",
      ts: {
        title: "Subtasks",
        id: "ov-st-h",
        asideHTML: '<span class="progress-line-text" id="st-progress"></span>',
        actionsHTML: add,
      },
    },
    {
      name: "a count and extras",
      ts: { title: "Pull <requests>", count: 2, cls: "pr-head", attrs: { "data-x": "1" } },
    },
    {
      name: "a subsection with markup",
      ts: { level: 4, titleHTML: "<em>Plan</em>", count: "3 of 5" },
    },
    {
      name: "a toggle with a hidden count",
      ts: {
        title: "Files",
        id: "ov-files-h",
        count: 0,
        countOpts: { hideZero: true, id: "ov-files-count" },
        toggle: { id: "ov-files-toggle", controls: "ov-files-body" },
        actionsHTML: add,
      },
    },
    {
      name: "an open toggle with nothing to fold",
      ts: { title: "Files", toggle: { controls: "b", expanded: true, disabled: true } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { titleHTML, asideHTML, actionsHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (titleHTML) slots.title = titleHTML;
      if (asideHTML) slots.aside = asideHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      const astro = await container.renderToString(SectionHead, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(sectionHeadHTML(c.ts)));
    });
  }
});

describe("Crumbs.astro and crumbsHTML()", () => {
  const cases: Array<{ name: string; crumbs: Crumb[]; label?: string; id?: string }> = [
    {
      name: "a link and the current page",
      crumbs: [
        { label: "Projects", href: "/projects/" },
        { label: "web <shop>", id: "crumb-name" },
      ],
    },
    {
      name: "every step a link, with a name and an id",
      crumbs: [
        { label: "Board", href: "/tasks/" },
        { label: "Parent", href: "/tasks/?task=1&x=2", id: "dh-parent" },
      ],
      label: "Where this task lives",
      id: "dr-crumbs",
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Crumbs, {
        props: { crumbs: c.crumbs, label: c.label, id: c.id },
      });
      expect(normalize(astro)).toBe(normalize(crumbsHTML(c.crumbs, { label: c.label, id: c.id })));
    });
  }

  it("a drawer's eyebrow: sm, one line, a mono id, a step a phone drops and the action", async () => {
    const crumbs: Crumb[] = [
      { label: "Board", href: "/tasks/?board=b1", hideNarrow: true },
      { label: "Parent", href: "#task-p1", attrs: { "data-open-task": "p1", title: "Open" } },
      { label: "a1b2c3", mono: true },
    ];
    const action = '<button type="button" class="btn" data-copy-link>Copy</button>';
    const astro = await container.renderToString(Crumbs, {
      props: { crumbs, label: "Where this task lives", size: "sm", line: true },
      slots: { action },
    });
    expect(normalize(astro)).toBe(
      normalize(
        crumbsHTML(crumbs, {
          label: "Where this task lives",
          size: "sm",
          line: true,
          actionHTML: action,
        }),
      ),
    );
  });

  it("refuses a step without a link before the last one, like crumbsHTML", async () => {
    await expect(
      container.renderToString(Crumbs, { props: { crumbs: [{ label: "A" }, { label: "B" }] } }),
    ).rejects.toThrow(/"A" needs an href/);
  });
});

describe("PageHead.astro and pageHeadHTML()", () => {
  const cases: Array<{ name: string; ts: PageHeadOptions }> = [
    { name: "a title alone", ts: { title: "Settings" } },
    {
      name: "subtitle with a cap, actions, flush and a title id",
      ts: {
        title: "Usage & cost",
        titleId: "p-title",
        subtitle: "Last <30> days",
        subMax: 720,
        flush: true,
        actionsHTML: buttonHTML({ label: "Refresh", icon: "refresh" }),
      },
    },
    {
      name: "a section with back, crumbs, above, status and a sub in markup",
      ts: {
        level: "section",
        title: "Support",
        titleId: "bs-detail-title",
        subHTML: '<span id="bs-detail-meta">3 columns</span>',
        back: { href: "/settings/#boards", label: "All boards" },
        crumbs: [{ label: "Settings", href: "/settings/" }, { label: "Support" }],
        crumbsId: "c1",
        aboveHTML: '<p class="eyebrow">x</p>',
        statusHTML: badgeHTML("default", "accent"),
        cls: "bs-detail",
        attrs: { "data-view": "detail" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { subtitle, subHTML, aboveHTML, statusHTML, actionsHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (subtitle === undefined && subHTML) slots.sub = subHTML;
      if (aboveHTML) slots.above = aboveHTML;
      if (statusHTML) slots.status = statusHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      const astro = await container.renderToString(PageHead, {
        props: { ...props, subtitle, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(pageHeadHTML(c.ts)));
    });
  }
});
