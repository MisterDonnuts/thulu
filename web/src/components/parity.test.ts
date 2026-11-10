// Runs in the node environment: under jsdom Vite compiles .astro files for the browser, where a
// component cannot render.

import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { MOD_KEY } from "../lib/shortcuts";
import { avatarHTML, type AvatarOptions } from "../lib/ui/avatar";
import {
  badgeHTML,
  dotHTML,
  type BadgeOptions,
  type BadgeTone,
  type DotOptions,
  type DotTone,
} from "../lib/ui/badge";
import { buttonHTML, type ButtonOptions } from "../lib/ui/button";
import { countHTML, type CountOptions } from "../lib/ui/count";
import {
  emptyRowHTML,
  emptyStateHTML,
  type EmptyRowOptions,
  type EmptyStateOptions,
} from "../lib/ui/empty";
import { factHTML, factsHTML, type Fact as FactRow, type FactsOptions } from "../lib/ui/facts";
import { icoSvg, type IconOptions } from "../lib/ui/icon";
import { kbdHTML, type KbdOptions } from "../lib/ui/kbd";
import {
  meterHTML,
  progressBarHTML,
  type MeterOptions,
  type ProgressBarOptions,
  type ProgressSegment,
} from "../lib/ui/progress";
import { tagHTML, type TagOptions } from "../lib/ui/tag";
import Avatar from "./Avatar.astro";
import Badge from "./Badge.astro";
import Button from "./Button.astro";
import Count from "./Count.astro";
import Dot from "./Dot.astro";
import EmptyRow from "./EmptyRow.astro";
import EmptyState from "./EmptyState.astro";
import Fact from "./Fact.astro";
import Facts from "./Facts.astro";
import Icon from "./Icon.astro";
import Kbd from "./Kbd.astro";
import Meter from "./Meter.astro";
import { normalize } from "./parity-normalize";
import ProgressBar from "./ProgressBar.astro";
import Tag from "./Tag.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("normalize", () => {
  it("drops formatting and keeps the tree", () => {
    expect(normalize('<b  class="x" id="y">\n  a &amp; b </b>')).toBe(
      normalize('<b id="y" class="x">a &amp; b</b>'),
    );
    expect(normalize('<use href="#a"/>')).toBe(normalize('<use href="#a"></use>'));
    expect(normalize("<b disabled>a</b>")).toBe(normalize('<b disabled="">a</b>'));
    expect(normalize('<b class="x">a</b>')).not.toBe(normalize('<b class="y">a</b>'));
    expect(normalize("<b hidden>a</b>")).not.toBe(normalize("<b>a</b>"));
    expect(normalize("<b>a</b>")).not.toBe(normalize("<i>a</i>"));
  });
});

describe("Button.astro and buttonHTML()", () => {
  const cases: Array<{ name: string; props: Record<string, unknown>; ts: ButtonOptions }> = [
    { name: "default", props: {}, ts: { label: "Save" } },
    {
      name: "primary sm with icon and hint",
      props: { variant: "primary", size: "sm", icon: "plus", kbd: "N" },
      ts: { label: "Save", variant: "primary", size: "sm", icon: "plus", kbd: "N" },
    },
    {
      name: "danger md submit",
      props: { variant: "danger", size: "md", type: "submit" },
      ts: { label: "Save", variant: "danger", size: "md", type: "submit" },
    },
    {
      name: "ghost link",
      props: { variant: "ghost", href: "/tasks/1" },
      ts: { label: "Save", variant: "ghost", href: "/tasks/1" },
    },
    {
      name: "link variant as a button",
      props: { variant: "link", "data-check-tab": "settings" },
      ts: { label: "Save", variant: "link", attrs: { "data-check-tab": "settings" } },
    },
    {
      name: "link variant with an href",
      props: { variant: "link", href: "/projects/" },
      ts: { label: "Save", variant: "link", href: "/projects/" },
    },
    {
      name: "disabled link",
      props: { href: "/tasks/1", disabled: true },
      ts: { label: "Save", href: "/tasks/1", disabled: true },
    },
    {
      name: "disabled with a sequence hint and extras",
      props: { disabled: true, kbd: "G T", id: "go", class: "head-wide", "data-act": "go" },
      ts: {
        label: "Save",
        disabled: true,
        kbd: "G T",
        id: "go",
        cls: "head-wide",
        attrs: { "data-act": "go" },
      },
    },
    {
      name: "trailing markup in the slot",
      props: { size: "sm", icon: "list" },
      ts: {
        label: "Tools",
        size: "sm",
        icon: "list",
        trailingHTML: '<span class="mcp-tools-count" data-mcp-count>2/5</span>',
      },
    },
    {
      name: "icon-only lg",
      props: { iconOnly: true, icon: "refresh", label: "Refresh", size: "lg" },
      ts: { iconOnly: true, icon: "refresh", label: "Refresh", size: "lg" },
    },
    {
      name: "icon-only xs ghost with a title",
      props: {
        iconOnly: true,
        icon: "i-plus",
        label: "New chat",
        size: "xs",
        variant: "ghost",
        title: "New chat (C)",
      },
      ts: {
        iconOnly: true,
        icon: "i-plus",
        label: "New chat",
        size: "xs",
        variant: "ghost",
        title: "New chat (C)",
      },
    },
    {
      name: "icon-only danger",
      props: { iconOnly: true, icon: "trash", label: "Delete", variant: "danger" },
      ts: { iconOnly: true, icon: "trash", label: "Delete", variant: "danger" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Button, {
        props: c.props,
        slots: c.ts.iconOnly ? {} : { default: c.ts.label + (c.ts.trailingHTML ?? "") },
      });
      expect(normalize(astro)).toBe(normalize(buttonHTML(c.ts)));
    });
  }

  it("refuses an icon-only button without a label or a glyph, like buttonHTML", async () => {
    await expect(
      container.renderToString(Button, { props: { iconOnly: true, icon: "x" } }),
    ).rejects.toThrow(/needs a label/);
    await expect(
      container.renderToString(Button, { props: { iconOnly: true, label: "Close" } }),
    ).rejects.toThrow(/needs an icon/);
    await expect(container.renderToString(Button, { props: { size: "lg" } })).rejects.toThrow(
      /no size "lg"/,
    );
  });
});

describe("Icon.astro and icoSvg()", () => {
  const cases: Array<{ name: string; icon: string; opts: IconOptions }> = [
    { name: "decorative", icon: "box", opts: {} },
    {
      name: "prefixed id, large, extra class",
      icon: "i-box",
      opts: { size: "lg", cls: "run-ico" },
    },
    { name: "small", icon: "box", opts: { size: "sm" } },
    { name: "labelled", icon: "warn", opts: { label: "Failed", size: "xl" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Icon, {
        props: { name: c.icon, size: c.opts.size, label: c.opts.label, class: c.opts.cls },
      });
      expect(normalize(astro)).toBe(normalize(icoSvg(c.icon, c.opts)));
    });
  }
});

describe("Kbd.astro and kbdHTML()", () => {
  const cases: Array<{ name: string; keys: string | string[]; opts: KbdOptions }> = [
    { name: "one key", keys: "N", opts: {} },
    { name: "small", keys: "/", opts: { size: "sm" } },
    { name: "decorative with a class", keys: "Esc", opts: { decorative: true, cls: "hint" } },
    { name: "chord with the modifier", keys: [MOD_KEY, "K"], opts: {} },
    { name: "small decorative sequence", keys: ["G", "T"], opts: { size: "sm", decorative: true } },
    { name: "inset in a field", keys: "/", opts: { size: "sm", inset: true, decorative: true } },
    { name: "keys against the end", keys: ["G", "T"], opts: { align: "end", cls: "x" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Kbd, {
        props: {
          keys: c.keys,
          size: c.opts.size,
          decorative: c.opts.decorative,
          align: c.opts.align,
          inset: c.opts.inset,
          class: c.opts.cls,
        },
      });
      expect(normalize(astro)).toBe(normalize(kbdHTML(c.keys, c.opts)));
    });
  }
});

describe("Badge.astro and badgeHTML()", () => {
  const cases: Array<{ name: string; label: string; tone?: BadgeTone; opts: BadgeOptions }> = [
    { name: "neutral", label: "Idle", opts: {} },
    { name: "tone", label: "Ready", tone: "ok", opts: {} },
    {
      name: "pill, small, mono, dashed",
      label: "env",
      tone: "accent",
      opts: { shape: "pill", size: "sm", mono: true, edge: "dashed" },
    },
    {
      name: "icon and tooltip",
      label: "Failed",
      tone: "danger",
      opts: { icon: "i-x-circle", title: "exit 1" },
    },
    { name: "own-colour dot", label: "Configured", tone: "ok", opts: { dot: true } },
    { name: "toned dot", label: "1 up", opts: { dot: "ok", edge: "hollow" } },
    { name: "breathing", label: "Running", tone: "info", opts: { shape: "pill", pulse: true } },
    { name: "raised over a picture", label: "Failed", tone: "danger", opts: { raised: true } },
    {
      name: "id, class and attributes",
      label: "main",
      tone: "muted",
      opts: { id: "b1", cls: "ev-prod", attrs: { "data-x": "1", hidden: true } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...look } = c.opts;
      const astro = await container.renderToString(Badge, {
        props: { tone: c.tone, ...look, class: cls, ...attrs },
        slots: { default: c.label },
      });
      expect(normalize(astro)).toBe(normalize(badgeHTML(c.label, c.tone, c.opts)));
    });
  }

  it("refuses a tone that does not exist, like badgeHTML", async () => {
    await expect(
      container.renderToString(Badge, { props: { tone: "success" }, slots: { default: "x" } }),
    ).rejects.toThrow(/no tone "success"/);
  });
});

describe("Dot.astro and dotHTML()", () => {
  const cases: Array<{ name: string; tone?: DotTone; opts: DotOptions }> = [
    { name: "plain", opts: {} },
    { name: "toned and breathing", tone: "ok", opts: { pulse: true } },
    { name: "ring with a class", tone: "empty", opts: { cls: "env-dot" } },
    { name: "labelled", tone: "danger", opts: { label: "Down", id: "d1" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Dot, {
        props: {
          tone: c.tone,
          pulse: c.opts.pulse,
          label: c.opts.label,
          id: c.opts.id,
          class: c.opts.cls,
        },
      });
      expect(normalize(astro)).toBe(normalize(dotHTML(c.tone, c.opts)));
    });
  }
});

describe("Count.astro and countHTML()", () => {
  const cases: Array<{ name: string; n: number | string; opts: CountOptions }> = [
    { name: "bare number", n: 3, opts: {} },
    { name: "bare with a class", n: 12, opts: { cls: "col-count-vis", id: "c1" } },
    { name: "pill with words", n: 2, opts: { pill: true, label: "questions", id: "stat-q" } },
    {
      name: "pill with a breathing dot",
      n: "3 / 3",
      opts: { pill: true, dot: "ok", pulse: true, label: "up" },
    },
    {
      name: "sidebar: small accent, capped, hidden at zero",
      n: 0,
      opts: { pill: true, size: "sm", tone: "accent", max: 99, hideZero: true, srLabel: "waiting" },
    },
    { name: "capped", n: 150, opts: { pill: true, tone: "danger", max: 99, title: "150" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...rest } = c.opts;
      const astro = await container.renderToString(Count, {
        props: { n: c.n, ...rest, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(countHTML(c.n, c.opts)));
    });
  }

  it("refuses a tone on the bare number, like countHTML", async () => {
    await expect(
      container.renderToString(Count, { props: { n: 1, tone: "warn" } }),
    ).rejects.toThrow(/needs pill/);
  });
});

describe("Tag.astro and tagHTML()", () => {
  const cases: Array<{ name: string; label: string; opts: TagOptions }> = [
    { name: "grey", label: "draft", opts: {} },
    { name: "stage colour with a dot", label: "Review", opts: { color: "#a78bfa", dot: true } },
    { name: "bucket, small", label: "docs", opts: { color: "green", size: "sm" } },
    { name: "bucket, a card's name", label: "tienda-web", opts: { color: "purple", size: "lg" } },
    {
      name: "repo link with its own dot",
      label: "tienda-web",
      opts: { dot: "pink", href: "/project/?id=p1", title: "tienda-web" },
    },
    { name: "hex dot", label: "col", opts: { dot: "#10b981" } },
    {
      name: "agent inline",
      label: "coder",
      opts: { tone: "special", icon: "i-bot", inline: true },
    },
    { name: "warn, dashed, mono", label: "all", opts: { tone: "warn", dashed: true, mono: true } },
    {
      name: "picker option off",
      label: "api",
      opts: { color: "blue", pressed: false, attrs: { "data-id": "p1" } },
    },
    { name: "picker option on", label: "api", opts: { pressed: true } },
    {
      name: "head button",
      label: "Review",
      opts: { button: true, id: "dr-stage", color: "#a78bfa", dot: true, disabled: true },
    },
    { name: "removable", label: "docs", opts: { color: "red", removable: true } },
    {
      name: "removable with a label and data",
      label: "docs",
      opts: { removable: { label: "Drop docs", attrs: { "data-tag": "docs" } }, cls: "tags-item" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { attrs, cls, ...rest } = c.opts;
      const astro = await container.renderToString(Tag, {
        props: { label: c.label, ...rest, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(tagHTML(c.label, c.opts)));
    });
  }

  it("refuses a colour with a tone and an X on a link, like tagHTML", async () => {
    await expect(
      container.renderToString(Tag, { props: { label: "x", color: "blue", tone: "warn" } }),
    ).rejects.toThrow(/colour and a tone/);
    await expect(
      container.renderToString(Tag, { props: { label: "x", href: "/x", removable: true } }),
    ).rejects.toThrow(/removable/);
  });
});

describe("EmptyState.astro and emptyStateHTML()", () => {
  const cases: Array<{
    name: string;
    props: Record<string, unknown>;
    body?: string;
    action?: string;
    ts: EmptyStateOptions;
  }> = [
    { name: "title only", props: { title: "Nothing" }, ts: { title: "Nothing" } },
    {
      name: "block with glyph, body and action",
      props: { icon: "search", title: "No matches" },
      body: "Change the search.",
      action: buttonHTML({ label: "Clear filters" }),
      ts: {
        icon: "search",
        title: "No matches",
        body: "Change the search.",
        action: { label: "Clear filters" },
      },
    },
    {
      name: "sm list item with markup in the body",
      props: { size: "sm", as: "li", icon: "i-box", title: "Pick one" },
      body: "or press <kbd>N</kbd>",
      ts: {
        size: "sm",
        as: "li",
        icon: "i-box",
        title: "Pick one",
        bodyHTML: "or press <kbd>N</kbd>",
      },
    },
    {
      name: "inline dashed with an action",
      props: { size: "inline", dashed: true, title: "No engines" },
      action: buttonHTML({ label: "Add", size: "sm", variant: "primary" }),
      ts: {
        size: "inline",
        dashed: true,
        title: "No engines",
        action: { label: "Add", size: "sm", variant: "primary" },
      },
    },
    {
      name: "danger, hidden, filled later by id",
      props: {
        tone: "danger",
        icon: "alert",
        id: "err",
        titleId: "err-t",
        bodyId: "err-b",
        hidden: true,
      },
      ts: {
        tone: "danger",
        icon: "alert",
        id: "err",
        titleId: "err-t",
        bodyId: "err-b",
        attrs: { hidden: true },
      },
    },
    {
      name: "loading with an extra class",
      props: { loading: true, class: "span-all" },
      ts: { loading: true, cls: "span-all" },
    },
    {
      name: "dashed first-run guide with steps and an action",
      props: {
        dashed: true,
        titleTag: "h2",
        title: "Start",
        steps: [{ title: "One", body: "First" }, { title: "Two" }],
      },
      action: buttonHTML({ label: "New", variant: "primary" }),
      ts: {
        dashed: true,
        titleTag: "h2",
        title: "Start",
        steps: [{ title: "One", body: "First" }, { title: "Two" }],
        action: { label: "New", variant: "primary" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const slots: Record<string, string> = {};
      if (c.body !== undefined) slots.default = c.body;
      if (c.action !== undefined) slots.action = c.action;
      const astro = await container.renderToString(EmptyState, { props: c.props, slots });
      expect(normalize(astro)).toBe(normalize(emptyStateHTML(c.ts)));
    });
  }

  it("refuses a size that does not exist, like emptyStateHTML", async () => {
    await expect(
      container.renderToString(EmptyState, { props: { size: "lg", title: "x" } }),
    ).rejects.toThrow(/no size "lg"/);
  });

  it("refuses a title tag that does not exist, like emptyStateHTML", async () => {
    await expect(
      container.renderToString(EmptyState, { props: { titleTag: "h1", title: "x" } }),
    ).rejects.toThrow(/no title tag "h1"/);
  });
});

describe("EmptyRow.astro and emptyRowHTML()", () => {
  const cases: Array<{ name: string; text?: string; opts: EmptyRowOptions }> = [
    { name: "table row", text: "Queue is empty", opts: { cols: 6 } },
    { name: "list item", text: "No runs yet", opts: { as: "li" } },
    { name: "loading row", opts: { cols: 3, loading: true } },
    {
      name: "failed div with an id",
      text: "Daemon offline",
      opts: { as: "div", tone: "danger", id: "e" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.opts;
      const astro = await container.renderToString(EmptyRow, {
        props: { ...props, class: cls, ...attrs },
        slots: c.text === undefined ? {} : { default: c.text },
      });
      expect(normalize(astro)).toBe(normalize(emptyRowHTML(c.text, c.opts)));
    });
  }
  it("markup in the slot is html in the function", async () => {
    const html = 'No column. <a href="/settings/#boards">Edit the board</a>';
    const astro = await container.renderToString(EmptyRow, {
      props: { cols: 2, class: "tpl-empty" },
      slots: { default: html },
    });
    expect(normalize(astro)).toBe(
      normalize(emptyRowHTML(undefined, { cols: 2, cls: "tpl-empty", html })),
    );
  });
});

describe("ProgressBar.astro and progressBarHTML()", () => {
  const segs: ProgressSegment[] = [
    { value: 2, tone: "ok", label: "2 passed" },
    { value: 1, tone: "info", label: "1 running", outline: true },
  ];
  const cases: Array<{ name: string; segments: ProgressSegment[]; opts: ProgressBarOptions }> = [
    { name: "named by its segments, with a track remainder", segments: segs, opts: { total: 5 } },
    {
      name: "xs, split, decorative, extra class",
      segments: segs,
      opts: { size: "xs", split: true, decorative: true, cls: "card-bar" },
    },
    { name: "md with its own name", segments: segs, opts: { size: "md", label: "Batch 3" } },
    { name: "empty", segments: [], opts: {} },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(ProgressBar, {
        props: {
          segments: c.segments,
          total: c.opts.total,
          size: c.opts.size,
          split: c.opts.split,
          label: c.opts.label,
          decorative: c.opts.decorative,
          class: c.opts.cls,
        },
      });
      expect(normalize(astro)).toBe(normalize(progressBarHTML(c.segments, c.opts)));
    });
  }
});

describe("Meter.astro and meterHTML()", () => {
  const cases: Array<{ name: string; value: number; opts: MeterOptions }> = [
    { name: "thresholds", value: 82, opts: { label: "Disk", thresholds: true } },
    {
      name: "labelled by, value text, md, tone",
      value: 42.4,
      opts: { labelledBy: "runs", valueText: "424 of 1000", size: "md", tone: "ok" },
    },
    { name: "inline xs, clamped", value: 140, opts: { label: "Share", size: "xs", inline: true } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Meter, {
        props: {
          value: c.value,
          label: c.opts.label,
          labelledBy: c.opts.labelledBy,
          valueText: c.opts.valueText,
          tone: c.opts.tone,
          thresholds: c.opts.thresholds,
          size: c.opts.size,
          inline: c.opts.inline,
          class: c.opts.cls,
        },
      });
      expect(normalize(astro)).toBe(normalize(meterHTML(c.value, c.opts)));
    });
  }

  it("refuses a meter with no name, like meterHTML", async () => {
    await expect(container.renderToString(Meter, { props: { value: 3 } })).rejects.toThrow(
      /needs a label/,
    );
  });
});

describe("Facts.astro and factsHTML()", () => {
  const rows: FactRow[] = [
    { label: "Kernel", value: "6.8 & up" },
    { label: "Image", value: "sha256:ab", mono: true, note: "from $IMAGE" },
    { label: "Skipped", value: null },
    { label: "Cores", value: 8 },
    {
      label: "Client",
      value: "",
      attrs: { id: "client-fact", hidden: true },
      valueAttrs: { id: "client-id" },
    },
  ];
  const cases: Array<{ name: string; opts: FactsOptions }> = [
    { name: "columns", opts: {} },
    { name: "grid boxed with a class", opts: { layout: "grid", boxed: true, cls: "detail" } },
    { name: "inline with an id", opts: { layout: "inline", attrs: { id: "summary" } } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const astro = await container.renderToString(Facts, {
        props: {
          rows,
          layout: c.opts.layout,
          boxed: c.opts.boxed,
          class: c.opts.cls,
          ...c.opts.attrs,
        },
      });
      expect(normalize(astro)).toBe(normalize(factsHTML(rows, c.opts)));
    });
  }

  it("a Fact with markup in its slot is a row with valueHTML", async () => {
    const valueHTML = '<a href="/p">p &amp; q</a>';
    const astro = await container.renderToString(Fact, {
      props: { label: "Repo", mono: true, note: "main", valueAttrs: { id: "repo" }, id: "row" },
      slots: { default: valueHTML },
    });
    expect(normalize(astro)).toBe(
      normalize(
        factHTML({
          label: "Repo",
          valueHTML,
          mono: true,
          note: "main",
          valueAttrs: { id: "repo" },
          attrs: { id: "row" },
        }),
      ),
    );
  });
});

describe("Avatar.astro and avatarHTML()", () => {
  const cases: Array<{ name: string; opts: AvatarOptions }> = [
    { name: "a person", opts: { name: "Ana Díaz" } },
    { name: "an agent, md", opts: { kind: "agent", name: "dev", size: "md" } },
    { name: "the system", opts: { kind: "system" } },
    { name: "an invite", opts: { pending: true, size: "md", cls: "member-mark" } },
    {
      name: "a running chat",
      opts: {
        kind: "agent",
        name: "dev",
        shape: "square",
        letters: true,
        status: "ok",
        pulse: true,
        label: "dev, running",
      },
    },
    { name: "a glyph of its own", opts: { icon: "tt-users" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, ...rest } = c.opts;
      const astro = await container.renderToString(Avatar, { props: { ...rest, class: cls } });
      expect(normalize(astro)).toBe(normalize(avatarHTML(c.opts)));
    });
  }
});
