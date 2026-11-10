import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { confirmDialogHTML, moveTasksBodyHTML, type ConfirmDialogOptions } from "../lib/ui/confirm";
import ConfirmDialog from "./ConfirmDialog.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("ConfirmDialog.astro and confirmDialogHTML()", () => {
  const cases: Array<{ name: string; ts: ConfirmDialogOptions }> = [
    { name: "a neutral question", ts: { title: "Rotate the secret?", confirmLabel: "Rotate" } },
    {
      name: "a danger one with its message",
      ts: {
        title: "Delete <x>?",
        message: "It goes.\nForever & ever.",
        confirmLabel: "Delete",
        cancelLabel: "Keep it",
        danger: true,
        icon: "trash",
      },
    },
    {
      name: "details, option, warning and notes",
      ts: {
        title: "Delete git-flow?",
        message: "2 agents grant it:",
        details: ["backend-dev", "reviewer"],
        detailsMono: true,
        option: {
          label: "Also remove it",
          hint: "Or keep the grants",
          checked: true,
          name: "revoke",
        },
        warning: "No write scope.",
        warningLink: { href: "/settings/?s=keys", label: "Change it" },
        notes: ["History stays.", "Restore it from Deleted."],
        confirmLabel: "Delete",
        danger: true,
      },
    },
    {
      name: "a text to type",
      ts: {
        title: "Delete the workspace?",
        requireText: "acme",
        confirmLabel: "Delete",
        danger: true,
      },
    },
    {
      name: "a move body under another prefix",
      ts: {
        title: "Delete the column?",
        message: "There are 2 tasks in this column.",
        bodyHTML: moveTasksBodyHTML({
          count: 2,
          destinations: [
            { id: "todo", label: "To do" },
            { id: "done", label: "Done" },
          ],
          label: "Move 2 tasks to",
          id: "ce-move-to",
        }),
        notes: ["It cannot be undone."],
        confirmLabel: "Delete column",
        danger: true,
        idPrefix: "ce",
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { bodyHTML, ...props } = c.ts;
      const astro = await container.renderToString(ConfirmDialog, {
        props,
        slots: bodyHTML !== undefined ? { default: bodyHTML } : {},
      });
      expect(normalize(astro)).toBe(normalize(confirmDialogHTML(c.ts)));
    });
  }
});
