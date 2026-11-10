import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML } from "../lib/ui/button";
import { modalHTML, type ModalOptions } from "../lib/ui/modal";
import Modal from "./Modal.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Modal.astro and modalHTML()", () => {
  const del = buttonHTML({ label: "Delete", variant: "danger", id: "m-del" });
  const test = buttonHTML({ label: "Test", icon: "play", id: "m-test" });
  const cases: Array<{ name: string; ts: ModalOptions }> = [
    { name: "a heading alone", ts: { id: "m", title: "Rename" } },
    {
      name: "glyph, ✕, lede, size and body",
      ts: {
        id: "m",
        title: "New credential",
        icon: "shield",
        closable: true,
        size: "lg",
        lede: "Stored encrypted.",
        bodyHTML: '<input id="m-name">',
      },
    },
    {
      name: "Cancel and the main action",
      ts: {
        id: "m",
        title: "Rename",
        cancel: true,
        primary: { label: "Rename", id: "m-go", type: "submit", form: "m-form", disabled: true },
      },
    },
    {
      name: "a Close and a danger action with an icon and a hint",
      ts: {
        id: "m",
        title: "Remove member",
        size: "md",
        cancel: "Close",
        primary: { label: "Remove", variant: "danger", icon: "trash", kbd: "R" },
      },
    },
    {
      name: "actions at both ends",
      ts: {
        id: "m",
        title: "Credential",
        size: "xl",
        actionsStartHTML: del,
        actionsHTML: test,
        cancel: true,
        primary: { label: "Save" },
      },
    },
    {
      name: "a whole foot",
      ts: { id: "m", title: "Shot", footHTML: '<span id="own">x</span>' },
    },
    {
      name: "a sheet with extras",
      ts: {
        id: "f",
        title: "Filters",
        variant: "sheet",
        closable: true,
        cls: "filters",
        bodyClass: "fs-body",
        headHTML: '<span class="count">2</span>',
        attrs: { "data-kind": "filters" },
      },
    },
    {
      name: "a viewer with head controls",
      ts: {
        id: "lightbox",
        title: "",
        variant: "viewer",
        closable: true,
        headHTML: '<span class="lb-count" id="lb-count"></span>',
        bodyHTML: '<div class="lb-stage" id="lb-stage"></div>',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { bodyHTML, headHTML, footHTML, actionsHTML, actionsStartHTML, cls, attrs, ...props } =
        c.ts;
      const slots: Record<string, string> = {};
      if (bodyHTML) slots.default = bodyHTML;
      if (headHTML) slots.head = headHTML;
      if (footHTML) slots.foot = footHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      if (actionsStartHTML) slots["actions-start"] = actionsStartHTML;
      const astro = await container.renderToString(Modal, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(modalHTML(c.ts)));
    });
  }
});
