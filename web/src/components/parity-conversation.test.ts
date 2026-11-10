import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import {
  composerHTML,
  mdHTML,
  messageHTML,
  type ComposerOptions,
  type MessageOptions,
} from "../lib/ui/conversation";
import Composer from "./Composer.astro";
import Markdown from "./Markdown.astro";
import Message from "./Message.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Markdown.astro and mdHTML()", () => {
  const cases = [
    { name: "empty, to be filled", html: "", opts: { id: "ov-display", cls: "brief-doc" } },
    { name: "compact with content", html: "<p><strong>a</strong> b</p>", opts: { compact: true } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, ...props } = c.opts as { cls?: string; id?: string; compact?: boolean };
      const astro = await container.renderToString(Markdown, {
        props: { ...props, class: cls },
        slots: c.html ? { default: c.html } : {},
      });
      expect(normalize(astro)).toBe(normalize(mdHTML(c.html, c.opts)));
    });
  }
});

describe("Message.astro and messageHTML()", () => {
  const cases: Array<{ name: string; ts: MessageOptions }> = [
    {
      name: "a person's markdown with the time",
      ts: { role: "user", bodyHTML: "<p>Why <code>133.20</code>?</p>", meta: "17:54" },
    },
    {
      name: "an agent named, with extras",
      ts: {
        role: "agent",
        author: "dev",
        bodyHTML: "<ol><li>one</li></ol>",
        meta: "17:54 · 9.8s",
        cls: "lv-asst",
        attrs: { "data-id": "m1" },
      },
    },
    { name: "an error as plain text", ts: { role: "agent", tone: "error", text: "Refused <x>." } },
    { name: "a note from Thulu", ts: { role: "system", text: "Resumed from the transcript." } },
    {
      name: "a folded brief with its lead",
      ts: {
        role: "user",
        lead: "Recording case C-12 in staging",
        fold: "Show the instructions",
        bodyHTML: "<p>Open the cart.</p>",
      },
    },
    {
      name: "what follows the body and markup in the meta",
      ts: {
        role: "agent",
        bodyHTML: "<p>ok</p>",
        afterHTML: '<details class="chat-tools"><summary>2 tools</summary></details>',
        metaHTML: '<button class="lv-link" type="button">Open</button>',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { bodyHTML, afterHTML, metaHTML, cls, attrs, ...props } = c.ts;
      const slots: Record<string, string> = {};
      if (bodyHTML !== undefined) slots.default = bodyHTML;
      if (afterHTML) slots.after = afterHTML;
      if (metaHTML) slots.meta = metaHTML;
      const astro = await container.renderToString(Message, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(messageHTML(c.ts)));
    });
  }

  it("refuses a role that does not exist, like messageHTML", async () => {
    await expect(
      container.renderToString(Message, { props: { role: "assistant", text: "x" } }),
    ).rejects.toThrow(/no role "assistant"/);
  });
});

describe("Composer.astro and composerHTML()", () => {
  const cases: Array<{ name: string; ts: ComposerOptions; extra?: string }> = [
    {
      name: "the comment box with audience and attach",
      ts: {
        id: "act-composer",
        ids: { input: "dr-reply", audience: "act-audience", error: "act-err" },
        labelledBy: "ov-activity-h",
        placeholder: "Write a comment…",
        audience: true,
        attach: true,
        cls: "act-composer-main",
      },
      extra: '<span class="notify-slot" id="dr-notify-slot"></span>',
    },
    {
      name: "a reply in a thread with its draft and Cancel",
      ts: {
        variant: "thread",
        label: "Reply in thread",
        placeholder: "Reply to dev…",
        value: "half <written>",
        audience: "Goes to dev.",
        cancel: true,
        ready: true,
        submitLabel: "Reply",
        inputCls: "thread-input",
        attrs: { "data-thread": "c1" },
      },
    },
    {
      name: "the side chat",
      ts: {
        variant: "chat",
        id: "chat",
        ids: { input: "chat-input", submit: "chat-send" },
        label: "Message",
        placeholder: "Write to the agent…",
        attrs: { "aria-label": "Send a message" },
      },
    },
    {
      name: "the task chat with its extras in the foot",
      ts: {
        variant: "chat",
        as: "div",
        id: "lv",
        label: "Message",
        rows: 3,
        submitLabel: "Send",
      },
      extra: '<label class="check">interrupt</label><span class="lv-delivery"></span>',
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, inputCls, ...props } = c.ts;
      const astro = await container.renderToString(Composer, {
        props: { ...props, inputClass: inputCls, class: cls, ...attrs },
        slots: c.extra ? { extra: c.extra } : {},
      });
      expect(normalize(astro)).toBe(normalize(composerHTML({ ...c.ts, extraHTML: c.extra })));
    });
  }

  it("refuses a composer nobody named, like composerHTML", async () => {
    await expect(
      container.renderToString(Composer, { props: { placeholder: "x" } }),
    ).rejects.toThrow(/needs a label/);
  });
});
