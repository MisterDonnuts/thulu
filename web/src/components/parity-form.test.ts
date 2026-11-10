import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { t } from "../i18n";
import {
  fieldHTML,
  formErrorHTML,
  formSectionHTML,
  inlineStatusHTML,
  type FieldOptions,
  type FormErrorOptions,
  type FormSectionOptions,
  type InlineStatusOptions,
} from "../lib/ui/form";
import { dateInputHTML, type DateInputOptions } from "../lib/ui/datetime";
import DateInput from "./DateInput.astro";
import Field from "./Field.astro";
import FormError from "./FormError.astro";
import FormSection from "./FormSection.astro";
import InlineStatus from "./InlineStatus.astro";
import { normalize } from "./parity-normalize";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Field.astro and fieldHTML()", () => {
  const cases: Array<{ name: string; ts: FieldOptions }> = [
    {
      name: "label and control",
      ts: { label: "Name", for: "p-name", control: '<input id="p-name">' },
    },
    {
      name: "optional with a hint",
      ts: {
        label: "Model",
        for: "m",
        optional: true,
        hint: "as the API names it",
        control: '<input id="m">',
      },
    },
    {
      name: "help under the control and an empty error line",
      ts: {
        label: "Repository URL",
        for: "p-url",
        help: "HTTPS or SSH.",
        errorId: "p-url-error",
        control: '<input id="p-url" aria-describedby="p-url-help">',
      },
    },
    {
      name: "an empty warning line, a script fills it",
      ts: {
        label: "Cache key",
        for: "k",
        help: "Files whose change resets it.",
        warnId: "k-warn",
        errorId: "k-error",
        control: '<input id="k">',
      },
    },
    {
      name: "a warning showing, under the derived id",
      ts: { label: "Ports", for: "pt", warn: "Port 22 is taken.", control: '<input id="pt">' },
    },
    {
      name: "an error showing, under the derived id",
      ts: {
        label: "Slug",
        for: "s",
        error: "Already taken.",
        control: '<input id="s" aria-invalid="true">',
      },
    },
    {
      name: "inline layout with extras",
      ts: {
        label: "Effort",
        for: "e",
        layout: "inline",
        id: "effort-field",
        cls: "grow",
        attrs: { "data-kind": "llm", hidden: true },
        control: '<select id="e"><option value="low">Low</option></select>',
      },
    },
    {
      name: "a small field that grows",
      ts: {
        label: "Event",
        for: "ev",
        size: "sm",
        grow: true,
        control: '<select id="ev"><option value="">Choose</option></select>',
      },
    },
    {
      name: "markup in the hint and the help",
      ts: {
        label: "Name",
        for: "n",
        hintHTML: "e.g. <code>my-mcp</code>",
        helpHTML: "Becomes <code>MCP_MY_MCP_URL</code>.",
        helpId: "n-env",
        control: '<input id="n">',
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { control, cls, attrs, hintHTML, helpHTML, ...props } = c.ts;
      const slots: Record<string, string> = { default: control };
      if (hintHTML) slots.hint = hintHTML;
      if (helpHTML) slots.help = helpHTML;
      const astro = await container.renderToString(Field, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(fieldHTML(c.ts)));
    });
  }

  it("keeps the space before (optional), which the normaliser would not see", async () => {
    const astro = await container.renderToString(Field, {
      props: { label: "Model", optional: true },
      slots: { default: "<input>" },
    });
    expect(astro).toContain(`Model <span class="field-optional"`);
    expect(astro).toContain(`>${t("ui.field.optional")}</span>`);
  });

  it("refuses a layout that does not exist, like fieldHTML", async () => {
    await expect(
      container.renderToString(Field, { props: { label: "x", layout: "row" } }),
    ).rejects.toThrow(/no layout "row"/);
  });
});

describe("FormError.astro and formErrorHTML()", () => {
  const cases: Array<{ name: string; ts: FormErrorOptions }> = [
    { name: "empty and hidden", ts: { id: "p-error" } },
    {
      name: "showing, with extras",
      ts: { id: "nt-err", text: "The daemon refused it.", cls: "nt-err" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, ...props } = c.ts;
      const astro = await container.renderToString(FormError, { props: { ...props, class: cls } });
      expect(normalize(astro)).toBe(normalize(formErrorHTML(c.ts)));
    });
  }
});

describe("FormSection.astro and formSectionHTML()", () => {
  const cases: Array<{ name: string; ts: FormSectionOptions }> = [
    { name: "a section", ts: { legend: "Logins", bodyHTML: '<p class="field-help">x</p>' } },
    {
      name: "ruled, with an id and attributes",
      ts: {
        legend: "Schedule",
        ruled: true,
        id: "ef-schedule-section",
        attrs: { hidden: true },
        bodyHTML: '<div class="form-row"><input id="a"></div>',
      },
    },
    { name: "a group, with extras", ts: { legend: "Access", group: true, cls: "x", bodyHTML: "" } },
    {
      name: "a heading as the legend",
      ts: { legendHTML: "<h4>Access</h4>", cls: "detail-section", bodyHTML: "<p>x</p>" },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, bodyHTML, legendHTML, ...props } = c.ts;
      const slots: Record<string, string> = bodyHTML ? { default: bodyHTML } : {};
      if (legendHTML) slots.legend = legendHTML;
      const astro = await container.renderToString(FormSection, {
        props: { ...props, class: cls, ...attrs },
        slots,
      });
      expect(normalize(astro)).toBe(normalize(formSectionHTML(c.ts)));
    });
  }
});

describe("InlineStatus.astro and inlineStatusHTML()", () => {
  const cases: Array<{ name: string; ts: InlineStatusOptions }> = [
    { name: "empty", ts: { id: "save-status" } },
    { name: "ok with words", ts: { id: "s", tone: "ok", text: "Saved" } },
    { name: "danger with extras", ts: { tone: "danger", text: "Refused.", cls: "cap" } },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, ...props } = c.ts;
      const astro = await container.renderToString(InlineStatus, {
        props: { ...props, class: cls },
      });
      expect(normalize(astro)).toBe(normalize(inlineStatusHTML(c.ts)));
    });
  }
});

describe("DateInput.astro and dateInputHTML()", () => {
  const cases: Array<{ name: string; ts: DateInputOptions }> = [
    { name: "an empty moment", ts: { id: "f-from" } },
    { name: "a time with a value", ts: { id: "ef-time", mode: "time", value: "09:00" } },
    {
      name: "a day with a name, a class and more attributes",
      ts: {
        id: "d",
        mode: "date",
        value: "2026-09-17",
        name: "day",
        cls: "grow",
        attrs: { "aria-describedby": "d-help" },
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(DateInput, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(dateInputHTML(c.ts)));
    });
  }
});
