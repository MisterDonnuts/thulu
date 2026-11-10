import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML } from "../lib/ui/button";
import {
  drawerBackButton,
  drawerHeadHTML,
  drawerHTML,
  type DrawerHeadOptions,
  type DrawerOptions,
} from "../lib/ui/drawer";
import Drawer from "./Drawer.astro";
import DrawerHead from "./DrawerHead.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Drawer.astro and drawerHTML()", () => {
  const cases: Array<{ name: string; ts: DrawerOptions }> = [
    { name: "a heading alone", ts: { id: "d", title: "Permissions" } },
    {
      name: "an empty heading and sub for the script, with a note and a button in the foot",
      ts: {
        id: "perm",
        title: "",
        sub: "",
        footNote: "Untick a tool to deny it.",
        footHTML: '<button id="perm-done" data-modal-close="">Done</button>',
        bodyHTML: '<div id="perm-list"></div>',
      },
    },
    {
      name: "a large record with an eyebrow, actions and a bar under the head",
      ts: {
        id: "case",
        title: "Checkout with a coupon",
        size: "lg",
        eyebrowHTML: '<span id="case-key">EV-2</span>',
        actionsHTML: '<button id="case-more">More</button>',
        headHTML: '<div class="case-bar"></div>',
        bodyClass: "case-body",
        attrs: { "data-kind": "case" },
      },
    },
    {
      name: "a docked, resizable panel on the left with a frame body and no ✕",
      ts: {
        id: "chat",
        title: "Chat",
        side: "left",
        size: "sm",
        modal: false,
        resizable: true,
        closable: false,
        body: "frame",
        cls: "chat-panel",
        attrs: { "data-view": "conversation" },
      },
    },
    {
      name: "a docked panel with a heading the reader edits and a tab bar",
      ts: {
        id: "drawer",
        title: "",
        titleId: "dr-title",
        titleAttrs: { contenteditable: "plaintext-only", "aria-describedby": "dr-hint" },
        modal: false,
        resizable: true,
        size: "lg",
        closable: false,
        barHTML: '<div class="tabs" role="tablist"></div>',
        bodyHTML: '<div id="pane"></div>',
      },
    },
    {
      name: "a docked panel on the left with a back button before the title",
      ts: {
        id: "chat",
        title: "New chat",
        side: "left",
        size: "sm",
        modal: false,
        leadHTML: buttonHTML({ ...drawerBackButton(), cls: "chat-back" }),
      },
    },
    {
      name: "a bottom sheet with a flush body",
      ts: { id: "props", title: "Properties", side: "bottom", body: "flush" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const {
        leadHTML,
        eyebrowHTML,
        actionsHTML,
        headHTML,
        barHTML,
        bodyHTML,
        footHTML,
        cls,
        attrs,
        ...props
      } = c.ts;
      const slots: Record<string, string> = {};
      if (bodyHTML) slots.default = bodyHTML;
      if (leadHTML) slots.lead = leadHTML;
      if (eyebrowHTML) slots.eyebrow = eyebrowHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      if (headHTML) slots.head = headHTML;
      if (barHTML) slots.bar = barHTML;
      if (footHTML) slots.foot = footHTML;
      const astro = await container.renderToString(Drawer, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(drawerHTML(c.ts)));
    });
  }

  it("refuses a resizable bottom sheet at build time", async () => {
    await expect(
      container.renderToString(Drawer, {
        props: { id: "s", title: "T", side: "bottom", resizable: true },
      }),
    ).rejects.toThrow(/resizable/);
  });
});

describe("DrawerHead.astro and drawerHeadHTML()", () => {
  const cases: Array<{ name: string; ts: DrawerHeadOptions }> = [
    { name: "a title alone", ts: { title: "Permissions", titleId: "p-title" } },
    {
      name: "a back button, a sub as markup, actions and the ✕",
      ts: {
        title: "Chat <one>",
        titleId: "chat-conv-title",
        titleAttrs: { title: "Click to rename" },
        subId: "chat-conv-sub",
        subHTML:
          '<span class="badge badge-warn badge-pill" id="d" hidden>On</span><span id="s"></span>',
        leadHTML: buttonHTML({ ...drawerBackButton("Back to chats"), cls: "chat-back" }),
        actionsHTML: '<button id="m" aria-haspopup="menu">More</button>',
      },
    },
    {
      name: "an empty sub for the script, an eyebrow, a head under the row and no ✕",
      ts: {
        title: "",
        titleId: "t",
        sub: "",
        subId: "t-sub",
        eyebrowHTML: '<span id="k">EV-2</span>',
        headHTML: '<div class="bar"></div>',
        closable: false,
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { leadHTML, eyebrowHTML, actionsHTML, headHTML, subHTML, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (leadHTML) slots.lead = leadHTML;
      if (eyebrowHTML) slots.eyebrow = eyebrowHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      if (subHTML) slots.sub = subHTML;
      if (headHTML) slots.default = headHTML;
      const astro = await container.renderToString(DrawerHead, { props, slots });
      expect(normalize(astro)).toBe(normalize(drawerHeadHTML(c.ts)));
    });
  }
});
