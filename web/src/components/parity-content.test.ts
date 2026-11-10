import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { codeBlockHTML, type CodeBlockOptions } from "../lib/ui/code";
import { disclosureHTML, type DisclosureOptions } from "../lib/ui/disclosure";
import { dropZoneHTML, type DropZoneOptions } from "../lib/ui/dropzone";
import { stepListHTML, type StepItem, type StepListOptions } from "../lib/ui/steps";
import CodeBlock from "./CodeBlock.astro";
import Disclosure from "./Disclosure.astro";
import DropZone from "./DropZone.astro";
import { normalize } from "./parity-normalize";
import StepList from "./StepList.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("CodeBlock.astro and codeBlockHTML()", () => {
  const cases: Array<{ name: string; ts: CodeBlockOptions }> = [
    { name: "a line", ts: { text: "thulu update apply" } },
    { name: "a line to copy, named", ts: { text: "sk_1", copy: true, label: "the key" } },
    {
      name: "a multiline log, capped and red",
      ts: { text: "npm ERR! <peer>\nnpm ERR! x", multiline: true, maxHeight: "sm", tone: "danger" },
    },
    {
      name: "a multiline that scrolls sideways",
      ts: { text: "a\nb", multiline: true, wrap: false },
    },
    {
      name: "an empty block a script fills, hidden",
      ts: { id: "wh", textId: "wh-value", copy: true, cls: "wh-secret", attrs: { hidden: true } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(CodeBlock, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(codeBlockHTML(c.ts)));
    });
  }

  it("refuses a tone that does not exist, like codeBlockHTML", async () => {
    await expect(
      container.renderToString(CodeBlock, { props: { text: "x", tone: "warn" } }),
    ).rejects.toThrow(/no tone "warn"/);
  });
});

describe("StepList.astro and stepListHTML()", () => {
  const steps: StepItem[] = [
    { title: "Identity provider", state: "done", detail: "OIDC — https://id.example" },
    {
      title: "First project",
      state: "pending",
      href: "/projects/",
      detail: "0 projects",
      note: "Meanwhile: no repository <yet>",
      finish: { text: "import one", href: "/projects/", command: "octopus project add" },
    },
    { title: "Agents", state: "blocked", finish: { text: "after a project" } },
    { title: "Starting", state: "now" },
    {
      title: "No prompt",
      state: "unknown",
      action: { label: "Write the prompt", attrs: { "data-check-tab": "prompts" } },
    },
    {
      title: "No key",
      state: "pending",
      action: { label: "Set up a key", href: "/settings/#llm" },
    },
    { title: "Unsafe", state: "pending", href: "javascript:alert(1)", attrs: { "data-id": "u" } },
  ];
  const cases: Array<{ name: string; opts: StepListOptions }> = [
    { name: "the full checklist", opts: {} },
    { name: "compact, with an id", opts: { compact: true, id: "first-steps-list", cls: "fs" } },
    { name: "inline, named", opts: { inline: true, attrs: { "aria-label": "Bring-up" } } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.opts;
      const astro = await container.renderToString(StepList, {
        props: { steps, ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(stepListHTML(steps, c.opts)));
    });
  }
});

describe("Disclosure.astro and disclosureHTML()", () => {
  const cases: Array<{ name: string; ts: DisclosureOptions & { bodyHTML: string } }> = [
    { name: "help", ts: { summary: "Why is a disk missing?", bodyHTML: "<p>Only some.</p>" } },
    {
      name: "help, ruled, hidden, with ids",
      ts: {
        summary: "Why?",
        ruled: true,
        id: "disks-help",
        bodyId: "disks-help-text",
        bodyHTML: "",
        attrs: { hidden: true },
      },
    },
    {
      name: "fields, with a form body and extra classes",
      ts: {
        variant: "fields",
        summary: "More options",
        id: "st-more",
        cls: "st-more",
        bodyHTML: '<div class="form-stack"><select id="s"></select></div>',
      },
    },
    {
      name: "fields, ruled, with an id",
      ts: {
        variant: "fields",
        summary: "More options",
        ruled: true,
        id: "ghapp-more",
        bodyHTML: '<div class="form-stack"><input id="x"></div>',
      },
    },
    {
      name: "code, open",
      ts: {
        variant: "code",
        summary: "Raw <output>",
        open: true,
        bodyHTML: codeBlockHTML({ text: '{"a":1}', multiline: true, maxHeight: "sm" }),
      },
    },
    {
      name: "row, open, its summary as markup",
      ts: {
        variant: "row",
        id: "tc-TC-1",
        cls: "tc-case tc-case-pass",
        open: true,
        attrs: { "data-key": "TC-1" },
        summaryHTML:
          '<span class="tc-row"><span class="tc-key">TC-1</span><span class="disclosure-title">Log in</span></span>',
        bodyHTML: '<div class="tc-detail"><p>Steps</p></div>',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, bodyHTML, summaryHTML, ...props } = c.ts;
      const slots: Record<string, string> = bodyHTML ? { default: bodyHTML } : {};
      if (summaryHTML) slots.summary = summaryHTML;
      const astro = await container.renderToString(Disclosure, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(disclosureHTML(c.ts)));
    });
  }
});

describe("DropZone.astro and dropZoneHTML()", () => {
  const cases: Array<{ name: string; ts: DropZoneOptions }> = [
    {
      name: "a folder or a zip, with a glyph and a line under",
      ts: { id: "m-drop", icon: "folder", title: "Drop the folder", sub: "or pick it" },
    },
    { name: "a zip only", ts: { id: "z", title: "Drop a .zip", accept: "zip", cls: "x" } },
    {
      name: "a folder only, hidden",
      ts: { id: "f", title: "Folder", accept: "folder", attrs: { hidden: true } },
    },
    { name: "one .json file", ts: { id: "j", title: "Drop a bundle", accept: "json" } },
    {
      name: "a .json overlay, hidden",
      ts: {
        id: "o",
        icon: "upload",
        title: "Drop to import",
        accept: "json",
        overlay: true,
        attrs: { hidden: true },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(DropZone, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(dropZoneHTML(c.ts)));
    });
  }
});
