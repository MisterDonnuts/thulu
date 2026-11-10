// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { t } from "../../i18n";
import { attrsHTML } from "./dom";
import {
  clearFormErrors,
  fieldClasses,
  fieldControlAttrs,
  fieldErrorHTML,
  fieldHTML,
  fieldIds,
  formErrorHTML,
  formSectionClasses,
  formSectionHTML,
  inlineStatusHTML,
  setFieldError,
  setFieldWarning,
  setFormError,
  setInlineStatus,
} from "./form";

function el(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

describe("fieldClasses", () => {
  it("writes field, the inline layout, then the extras", () => {
    expect(fieldClasses()).toBe("field");
    expect(fieldClasses({ layout: "stacked", cls: "grow" })).toBe("field grow");
    expect(fieldClasses({ layout: "inline" })).toBe("field field-inline");
    expect(fieldClasses({ size: "sm", grow: true, cls: "x" })).toBe("field field-sm field-grow x");
    expect(fieldClasses({ size: "md" })).toBe("field");
  });

  it("throws on a size that does not exist", () => {
    expect(() => fieldClasses({ size: "lg" as never })).toThrow(/no size "lg"/);
  });

  it("throws on a layout that does not exist", () => {
    expect(() => fieldClasses({ layout: "row" as never })).toThrow(/no layout "row"/);
  });
});

describe("fieldIds", () => {
  it("derives the ids of the lines a field writes from the control's id", () => {
    expect(fieldIds({ for: "p-url", help: true, error: true })).toEqual({
      helpId: "p-url-help",
      warnId: undefined,
      errorId: "p-url-error",
    });
    expect(fieldIds({ for: "p-url", warn: true })).toEqual({
      helpId: undefined,
      warnId: "p-url-warn",
      errorId: undefined,
    });
  });

  it("keeps the ids given and leaves out a line that is not written", () => {
    expect(fieldIds({ for: "p-url", errorId: "url-err", error: true })).toEqual({
      helpId: undefined,
      warnId: undefined,
      errorId: "url-err",
    });
    expect(fieldIds({ help: true, error: true })).toEqual({
      helpId: undefined,
      warnId: undefined,
      errorId: undefined,
    });
  });
});

describe("fieldHTML", () => {
  it("writes the label tied to its control and the control as is", () => {
    expect(fieldHTML({ label: "Name", for: "p-name", control: '<input id="p-name">' })).toBe(
      '<div class="field"><label class="field-label" for="p-name">Name</label>' +
        '<input id="p-name"></div>',
    );
  });

  it("writes (optional) after the label with its space", () => {
    const html = fieldHTML({ label: "Model", optional: true, control: "<input>" });
    expect(html).toContain(
      `Model <span class="field-optional">${t("ui.field.optional")}</span></label>`,
    );
    expect(el(html).querySelector("label")?.textContent).toBe(`Model ${t("ui.field.optional")}`);
  });

  it("puts the hint on the label's line and the help under the control", () => {
    const f = el(
      fieldHTML({
        label: "URL",
        for: "u",
        hint: "SSE",
        help: "Reachable from the pods.",
        control: '<input id="u">',
      }),
    );
    const kids = Array.from(f.children).map((c) => c.className || c.tagName);
    expect(kids).toEqual(["field-label", "INPUT", "field-help"]);
    expect(f.querySelector(".field-label .field-hint")?.textContent).toBe("SSE");
    expect(f.querySelector(".field-help")?.id).toBe("u-help");
  });

  it("writes an empty, hidden error line for a script when only errorId is given", () => {
    const line = el(
      fieldHTML({ label: "Name", errorId: "n-err", control: "<input>" }),
    ).querySelector(".field-error") as HTMLElement;
    expect(line.id).toBe("n-err");
    expect(line.hidden).toBe(true);
    expect(line.textContent).toBe("");
  });

  it("shows an error it is given, under the control's derived id", () => {
    const line = el(
      fieldHTML({ label: "Name", for: "n", error: "Required.", control: '<input id="n">' }),
    ).querySelector(".field-error") as HTMLElement;
    expect(line.id).toBe("n-error");
    expect(line.hidden).toBe(false);
    expect(line.textContent).toBe("Required.");
  });

  it("escapes the label, hint, help and error, and writes the HTML twins as is", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const f = el(
      fieldHTML({ label: evil, hint: evil, help: evil, error: evil, control: "<input>" }),
    );
    expect(f.querySelector("img")).toBeNull();
    expect(f.querySelector(".field-label")?.firstChild?.textContent?.trim()).toBe(evil);
    expect(f.querySelector(".field-error")?.textContent).toBe(evil);
    const rich = el(
      fieldHTML({
        label: "Name",
        hintHTML: "e.g. <code>my-mcp</code>",
        helpHTML: "Becomes <code>MCP_X_URL</code>.",
        control: "<input>",
      }),
    );
    expect(rich.querySelector(".field-hint code")?.textContent).toBe("my-mcp");
    expect(rich.querySelector(".field-help code")?.textContent).toBe("MCP_X_URL");
    expect(
      fieldHTML({ label: "a", hint: "plain", hintHTML: "<b>x</b>", control: "" }),
    ).not.toContain("<b>");
  });

  it("takes the inline layout, an id and extra attributes", () => {
    const f = el(
      fieldHTML({
        label: "Model",
        layout: "inline",
        id: "model-field",
        cls: "grow",
        attrs: { hidden: true, "data-kind": "llm" },
        control: "<select></select>",
      }),
    );
    expect(f.className).toBe("field field-inline grow");
    expect(f.id).toBe("model-field");
    expect(f.hidden).toBe(true);
    expect(f.dataset.kind).toBe("llm");
  });
});

describe("fieldControlAttrs", () => {
  it("points the control at the help, and at the error only while it shows", () => {
    expect(fieldControlAttrs({ for: "u", help: true, error: true })).toEqual({
      "aria-describedby": "u-help",
      "aria-invalid": undefined,
    });
    expect(fieldControlAttrs({ for: "u", help: true, error: true, errorText: "Bad" })).toEqual({
      "aria-describedby": "u-help u-error",
      "aria-invalid": "true",
    });
    expect(attrsHTML(fieldControlAttrs({ for: "u" }))).toBe("");
  });
});

describe("setFieldError", () => {
  beforeEach(() => {
    document.body.innerHTML = fieldHTML({
      label: "Name",
      for: "n",
      help: "Lowercase.",
      errorId: "n-error",
      control: `<input id="n"${attrsHTML(fieldControlAttrs({ for: "n", help: true }))}>`,
    });
  });
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("fills the line, marks the control and adds the error to its description", () => {
    const input = document.getElementById("n") as HTMLInputElement;
    setFieldError(input, "Already taken.", { focus: true });
    const line = document.getElementById("n-error") as HTMLElement;
    expect(line.hidden).toBe(false);
    expect(line.textContent).toBe("Already taken.");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.getAttribute("aria-describedby")).toBe("n-help n-error");
    expect(document.activeElement).toBe(input);
  });

  it("clears it all again with an empty message, from the field as well", () => {
    const input = document.getElementById("n") as HTMLInputElement;
    setFieldError(input, "Already taken.");
    setFieldError(input, "Still taken.");
    expect(input.getAttribute("aria-describedby")).toBe("n-help n-error");
    setFieldError(document.querySelector(".field") as HTMLElement, "");
    expect((document.getElementById("n-error") as HTMLElement).hidden).toBe(true);
    expect(input.hasAttribute("aria-invalid")).toBe(false);
    expect(input.getAttribute("aria-describedby")).toBe("n-help");
  });

  it("does nothing outside a field", () => {
    document.body.innerHTML = '<input id="loose">';
    const input = document.getElementById("loose") as HTMLInputElement;
    setFieldError(input, "x");
    expect(input.hasAttribute("aria-invalid")).toBe(false);
  });
});

describe("setFieldWarning", () => {
  beforeEach(() => {
    document.body.innerHTML = fieldHTML({
      label: "Cache key",
      for: "k",
      help: "Files whose change resets it.",
      warnId: "k-warn",
      errorId: "k-error",
      control: `<input id="k"${attrsHTML(fieldControlAttrs({ for: "k", help: true }))}>`,
    });
  });
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("writes the warning line between the help and the error, hidden while empty", () => {
    const lines = Array.from(document.querySelectorAll(".field > p"), (p) => p.className);
    expect(lines).toEqual(["field-help", "field-warn", "field-error"]);
    expect((document.getElementById("k-warn") as HTMLElement).hidden).toBe(true);
    expect(fieldHTML({ label: "x", for: "x", warn: "<careful>", control: "" })).toContain(
      '<p class="field-warn" id="x-warn">&lt;careful&gt;</p>',
    );
  });

  it("fills the line and describes the control with it, without marking it invalid", () => {
    const input = document.getElementById("k") as HTMLInputElement;
    setFieldWarning(input, "go.sum is not in the repository.");
    const line = document.getElementById("k-warn") as HTMLElement;
    expect(line.hidden).toBe(false);
    expect(line.textContent).toBe("go.sum is not in the repository.");
    expect(input.hasAttribute("aria-invalid")).toBe(false);
    expect(input.getAttribute("aria-describedby")).toBe("k-help k-warn");
    setFieldWarning(input, "Still missing.");
    expect(input.getAttribute("aria-describedby")).toBe("k-help k-warn");
  });

  it("clears the line and its description, from the field as well", () => {
    const input = document.getElementById("k") as HTMLInputElement;
    setFieldWarning(input, "x");
    setFieldWarning(document.querySelector(".field") as HTMLElement, "");
    expect((document.getElementById("k-warn") as HTMLElement).hidden).toBe(true);
    expect(input.getAttribute("aria-describedby")).toBe("k-help");
  });

  it("leaves a field without a warning line alone", () => {
    document.body.innerHTML = fieldHTML({ label: "x", for: "x", control: '<input id="x">' });
    const input = document.getElementById("x") as HTMLInputElement;
    setFieldWarning(input, "x");
    expect(input.hasAttribute("aria-describedby")).toBe(false);
  });
});

describe("fieldErrorHTML", () => {
  it("writes the field's error line on its own: an alert, hidden while empty", () => {
    expect(fieldErrorHTML({ id: "files-err" })).toBe(
      '<p id="files-err" class="field-error" role="alert" hidden></p>',
    );
    expect(fieldErrorHTML({ tag: "span", row: true, cls: "x-error", text: "<b>no</b>" })).toBe(
      '<span class="field-error field-error-row x-error" role="alert">&lt;b&gt;no&lt;/b&gt;</span>',
    );
  });

  it("is filled and hidden by setFormError", () => {
    const line = el(fieldErrorHTML());
    setFormError(line, "Upload failed.");
    expect(line.hidden).toBe(false);
    expect(line.textContent).toBe("Upload failed.");
    setFormError(line, "");
    expect(line.hidden).toBe(true);
  });
});

describe("form sections", () => {
  it("writes form-section, group, ruled, then the extras", () => {
    expect(formSectionClasses()).toBe("form-section");
    expect(formSectionClasses({ ruled: true, cls: "x" })).toBe("form-section form-section-ruled x");
    expect(formSectionClasses({ group: true, ruled: true })).toBe(
      "form-section form-section-group form-section-ruled",
    );
  });

  it("is a fieldset named by its legend, the body in a column", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const box = el(
      formSectionHTML({
        legend: evil,
        ruled: true,
        id: "ef-schedule-section",
        attrs: { hidden: true },
        bodyHTML: '<div class="form-row"></div>',
      }),
    );
    expect(box.tagName).toBe("FIELDSET");
    expect(box.id).toBe("ef-schedule-section");
    expect(box.hidden).toBe(true);
    expect(box.className).toBe("form-section form-section-ruled");
    const legend = box.firstElementChild;
    expect(legend?.tagName).toBe("LEGEND");
    expect(legend?.className).toBe("form-section-legend");
    expect(legend?.textContent).toBe(evil);
    expect(box.querySelector("img")).toBeNull();
    expect(box.querySelector(":scope > .form-section-body > .form-row")).not.toBeNull();
  });
});

describe("formErrorHTML and setFormError", () => {
  it("writes an alert box, hidden while empty", () => {
    expect(formErrorHTML({ id: "p-error" })).toBe(
      '<p id="p-error" class="callout callout-danger callout-bare form-error" role="alert" hidden></p>',
    );
    expect(formErrorHTML({ text: "<b>refused</b>", cls: "nt-err" })).toBe(
      '<p class="callout callout-danger callout-bare form-error nt-err" role="alert">&lt;b&gt;refused&lt;/b&gt;</p>',
    );
  });

  it("fills and hides the box", () => {
    const box = el(formErrorHTML());
    setFormError(box, "Network down.");
    expect(box.hidden).toBe(false);
    expect(box.textContent).toBe("Network down.");
    setFormError(box, "");
    expect(box.hidden).toBe(true);
  });
});

describe("clearFormErrors", () => {
  it("clears every field error and the form error inside the root", () => {
    document.body.innerHTML =
      "<form>" +
      fieldHTML({ label: "A", for: "a", errorId: "a-error", control: '<input id="a">' }) +
      fieldHTML({ label: "B", for: "b", errorId: "b-error", control: '<input id="b">' }) +
      formErrorHTML({ id: "f-error" }) +
      "</form>";
    setFieldError(document.getElementById("a") as HTMLElement, "Bad A");
    setFieldError(document.getElementById("b") as HTMLElement, "Bad B");
    setFormError(document.getElementById("f-error") as HTMLElement, "Refused");
    clearFormErrors(document.querySelector("form") as HTMLFormElement);
    expect(document.querySelectorAll(".field-error:not([hidden])")).toHaveLength(0);
    expect(document.querySelectorAll("[aria-invalid]")).toHaveLength(0);
    expect((document.getElementById("f-error") as HTMLElement).hidden).toBe(true);
    document.body.innerHTML = "";
  });
});

describe("inline status", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("writes a polite status region that is never hidden", () => {
    expect(inlineStatusHTML({ id: "save-status" })).toBe(
      '<span id="save-status" class="inline-status" data-tone="neutral" role="status"></span>',
    );
    expect(inlineStatusHTML({ tone: "danger", text: "<x>", cls: "cap" })).toBe(
      '<span class="inline-status cap" data-tone="danger" role="status">&lt;x&gt;</span>',
    );
    expect(() => inlineStatusHTML({ tone: "err" as never })).toThrow(/no tone "err"/);
  });

  it("sets the tone and the words, and fades only when asked", () => {
    const s = el(inlineStatusHTML());
    setInlineStatus(s, "danger", "Could not save.");
    expect(s.dataset.tone).toBe("danger");
    vi.advanceTimersByTime(10_000);
    expect(s.textContent).toBe("Could not save.");
    setInlineStatus(s, "ok", "Saved", { fadeMs: 2000 });
    vi.advanceTimersByTime(1999);
    expect(s.textContent).toBe("Saved");
    vi.advanceTimersByTime(1);
    expect(s.textContent).toBe("");
  });

  it("lets a later status cancel the pending fade", () => {
    const s = el(inlineStatusHTML());
    setInlineStatus(s, "ok", "Saved", { fadeMs: 2000 });
    vi.advanceTimersByTime(1000);
    setInlineStatus(s, "danger", "Refused.");
    vi.advanceTimersByTime(5000);
    expect(s.textContent).toBe("Refused.");
    expect(s.hidden).toBe(false);
  });
});
