import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { buttonHTML, type ButtonOptions } from "../lib/ui/button";
import {
  catalogItemHTML,
  catalogLayoutHTML,
  entityHeadHTML,
  fileRowHTML,
  mdEditorHTML,
  saveFootHTML,
  saveStateHTML,
  type CatalogItemOptions,
  type CatalogLayoutOptions,
  type EntityHeadOptions,
  type FileRowOptions,
  type MdEditorOptions,
} from "../lib/ui/catalog";
import { historyViewHTML, type HistoryViewOptions } from "../lib/ui/history";
import CatalogItem from "./CatalogItem.astro";
import CatalogLayout from "./CatalogLayout.astro";
import EntityHead from "./EntityHead.astro";
import FileRow from "./FileRow.astro";
import HistoryView from "./HistoryView.astro";
import MdEditor from "./MdEditor.astro";
import { normalize } from "./parity-normalize";
import SaveFoot from "./SaveFoot.astro";
import SaveState from "./SaveState.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("CatalogLayout.astro and catalogLayoutHTML()", () => {
  const cases: Array<{ name: string; ts: CatalogLayoutOptions }> = [
    { name: "the list and the editor alone", ts: { listId: "l", mainId: "e" } },
    {
      name: "a short side with every slot",
      ts: {
        side: "sm",
        label: "Skills",
        listId: "skill-list",
        mainId: "editor",
        toolbarHTML: '<input id="search">',
        listHTML: '<div class="row-empty">Loading</div>',
        footHTML: "<kbd>↑</kbd> walk",
        mainHTML: "<p>Pick a skill</p>",
        cls: "x",
        attrs: { "data-page": "skills" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { toolbarHTML, listHTML, footHTML, mainHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (toolbarHTML) slots.toolbar = toolbarHTML;
      if (listHTML) slots.list = listHTML;
      if (footHTML) slots.foot = footHTML;
      if (mainHTML) slots.default = mainHTML;
      const astro = await container.renderToString(CatalogLayout, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(catalogLayoutHTML(c.ts)));
    });
  }
});

describe("CatalogItem.astro and catalogItemHTML()", () => {
  const cases: Array<{ name: string; ts: CatalogItemOptions }> = [
    { name: "a name alone", ts: { name: "developer" } },
    {
      name: "the open one, with its description and a hook",
      ts: {
        name: "developer",
        description: "Writes the code & the tests",
        current: true,
        attrs: { "data-name": "developer" },
      },
    },
    {
      name: "a mono slug cut at one line, dim, with a badge",
      ts: {
        name: "git-flow",
        mono: true,
        clamp: 1,
        dim: true,
        description: "Branches",
        badgesHTML: '<span class="badge">update</span>',
        cls: "x",
      },
    },
    {
      name: "an agent with its stages under the description",
      ts: {
        name: "developer",
        description: "Writes the code",
        tagsHTML: '<span class="badge">Build</span><span class="badge">Review</span>',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { badgesHTML, tagsHTML, attrs, cls, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (badgesHTML) slots.badges = badgesHTML;
      if (tagsHTML) slots.tags = tagsHTML;
      const astro = await container.renderToString(CatalogItem, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(catalogItemHTML(c.ts)));
    });
  }
});

describe("EntityHead.astro and entityHeadHTML()", () => {
  const del = buttonHTML({ label: "Delete", icon: "trash", variant: "danger", size: "sm" });
  const cases: Array<{ name: string; ts: EntityHeadOptions }> = [
    { name: "a title alone", ts: { title: "developer" } },
    {
      name: "mono, meta with an id, badges and actions",
      ts: {
        title: "git-flow",
        titleId: "sk-title",
        mono: true,
        meta: "Edited today",
        metaId: "sk-meta",
        badgesHTML: '<span class="badge">system</span>',
        actionsHTML: del,
        cls: "x",
        attrs: { "data-slug": "git-flow" },
      },
    },
    { name: "an empty meta a script fills, at h3", ts: { title: "a", metaId: "m", level: 3 } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { badgesHTML, actionsHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (badgesHTML) slots.badges = badgesHTML;
      if (actionsHTML) slots.actions = actionsHTML;
      const astro = await container.renderToString(EntityHead, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(entityHeadHTML(c.ts)));
    });
  }
});

describe("SaveState.astro, SaveFoot.astro and their functions", () => {
  for (const dirty of [false, true]) {
    for (const quiet of [false, true]) {
      it(`a ${dirty ? "dirty" : "clean"}${quiet ? " quiet" : ""} state`, async () => {
        const astro = await container.renderToString(SaveState, {
          props: { id: "s", dirty, quiet },
        });
        expect(normalize(astro)).toBe(normalize(saveStateHTML(dirty, { id: "s", quiet })));
      });
    }
  }

  it("a foot with its buttons", async () => {
    const buttons =
      buttonHTML({ label: "Revert", id: "set-revert" }) +
      buttonHTML({ label: "Save", variant: "primary", id: "set-save", disabled: true });
    const astro = await container.renderToString(SaveFoot, {
      props: { id: "ed-save-bar", stateId: "set-status", hidden: true },
      slots: { default: buttons },
    });
    expect(normalize(astro)).toBe(
      normalize(
        saveFootHTML({
          id: "ed-save-bar",
          stateId: "set-status",
          actionsHTML: buttons,
          attrs: { hidden: true },
        }),
      ),
    );
  });
});

describe("MdEditor.astro and mdEditorHTML()", () => {
  const history: ButtonOptions = { label: "History", icon: "clock", id: "pp-history" };
  const cases: Array<{ name: string; ts: MdEditorOptions }> = [
    {
      name: "every part of the bar",
      ts: {
        path: "prompts/main.md",
        pathId: "pp-path",
        note: "(new)",
        textId: "pp-text",
        value: "# Prompt <b>",
        back: { id: "pp-back" },
        stateId: "pp-state",
        buttons: [history],
        save: { id: "pp-save", kbd: "⌘S", disabled: true },
        cls: "x",
        attrs: { "data-file": "main" },
      },
    },
    {
      name: "a reader's box, no Back, no Save",
      ts: {
        path: "SKILL.md",
        pathId: "p",
        textId: "t",
        back: false,
        readOnly: true,
        box: true,
        save: { id: "s", hidden: true },
      },
    },
    {
      name: "Back and Save with labels of their own",
      ts: {
        path: "a.sh",
        pathId: "p",
        textId: "t",
        back: { label: "Close", id: "b" },
        save: { id: "s", label: "Save hook" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { back, buttons, cls, attrs, ...rest } = c.ts;
      const props: Record<string, unknown> = { ...rest, class: cls, ...attrs };
      if (back === false) props.back = false;
      else if (back) Object.assign(props, { backLabel: back.label, backId: back.id });
      const slot = (buttons ?? []).map((b) => buttonHTML({ ...b, size: "sm" })).join("");
      const astro = await container.renderToString(MdEditor, {
        props,
        slots: slot ? { buttons: slot } : {},
      });
      expect(normalize(astro)).toBe(normalize(mdEditorHTML(c.ts)));
    });
  }
});

describe("FileRow.astro and fileRowHTML()", () => {
  const edit = buttonHTML({ label: "Edit", icon: "edit", size: "sm" });
  const cases: Array<{ name: string; ts: FileRowOptions }> = [
    { name: "a label alone", ts: { label: "main.md" } },
    {
      name: "an existing file with its actions",
      ts: {
        label: "En curso",
        status: "prompts/in_progress.md",
        tone: "ok",
        actionsHTML: edit,
        attrs: { "data-stage": "in_progress" },
      },
    },
    {
      name: "a dashed fallback whose status is words, in a list",
      ts: {
        as: "li",
        label: "main.md",
        status: "Used where no stage has its own",
        statusMono: false,
        variant: "dashed",
        cls: "x",
      },
    },
    {
      name: "a left-over file, struck out, with a mono label",
      ts: {
        label: "old/x.md",
        labelMono: true,
        status: "stage gone",
        tone: "warn",
        variant: "deleted",
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { actionsHTML, cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(FileRow, {
        props: { ...props, class: cls, ...attrs },
        slots: actionsHTML ? { actions: actionsHTML } : {},
      });
      expect(normalize(astro)).toBe(normalize(fileRowHTML(c.ts)));
    });
  }
});

describe("HistoryView.astro and historyViewHTML()", () => {
  const cases: Array<{ name: string; ts: HistoryViewOptions }> = [
    { name: "the bar, loading", ts: { title: "History of prompts/main.md", pick: "Pick one" } },
    {
      name: "boxed, without the bar",
      ts: { title: "Init script", pick: "Pick a revision", bar: false, boxed: true, cls: "x" },
    },
    { name: "a Back of its own", ts: { title: "H", pick: "P", backLabel: "Close" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(HistoryView, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(historyViewHTML(c.ts)));
    });
  }
});
