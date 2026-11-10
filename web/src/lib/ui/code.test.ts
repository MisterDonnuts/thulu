// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { t } from "../../i18n";
import {
  bindCodeCopy,
  codeBlockClasses,
  codeBlockHTML,
  COPY_FEEDBACK_MS,
  copyCodeBlock,
  copyText,
  codeTextAttrs,
} from "./code";

function parse(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

function mount(html: string): HTMLButtonElement {
  document.body.innerHTML = html;
  return document.querySelector<HTMLButtonElement>("[data-code-copy]") as HTMLButtonElement;
}

function secureClipboard(write: (text: string) => Promise<void>) {
  vi.stubGlobal("isSecureContext", true);
  const writeText = vi.fn(write);
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  return writeText;
}

function legacyCopy(ok: boolean) {
  const exec = vi.fn(() => ok);
  Object.defineProperty(document, "execCommand", { value: exec, configurable: true });
  return exec;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("codeBlockClasses", () => {
  it("writes code-block, the shape, the modifiers, then the extras", () => {
    expect(codeBlockClasses()).toBe("code-block code-block-line");
    expect(codeBlockClasses({ multiline: true })).toBe(
      "code-block code-block-multi code-block-wrap",
    );
    expect(codeBlockClasses({ multiline: true, wrap: false, maxHeight: "sm" })).toBe(
      "code-block code-block-multi code-block-h-sm",
    );
    expect(codeBlockClasses({ wrap: true, tone: "danger", cls: "ev-log" })).toBe(
      "code-block code-block-line code-block-wrap code-block-danger ev-log",
    );
  });

  it("throws on a tone or a height that does not exist", () => {
    expect(() => codeBlockClasses({ tone: "warn" as never })).toThrow(/no tone "warn"/);
    expect(() => codeBlockClasses({ maxHeight: "md" as never })).toThrow(/no max height "md"/);
  });
});

describe("codeTextAttrs", () => {
  it("makes a block that can scroll a tab stop, and one that wraps whole not", () => {
    expect(codeTextAttrs({}).tabindex).toBe(0);
    expect(codeTextAttrs({ multiline: true }).tabindex).toBeUndefined();
    expect(codeTextAttrs({ multiline: true, maxHeight: "lg" }).tabindex).toBe(0);
    expect(codeTextAttrs({ wrap: true }, "v").id).toBe("v");
  });
});

describe("codeBlockHTML", () => {
  it("writes one line in a <code> and nothing else by default", () => {
    expect(codeBlockHTML({ text: "thulu update" })).toBe(
      '<div class="code-block code-block-line">' +
        '<code class="code-block-text" tabindex="0">thulu update</code></div>',
    );
  });

  it("writes several lines in a <pre>", () => {
    const el = parse(codeBlockHTML({ text: "a\nb", multiline: true }));
    const text = el.querySelector(".code-block-text");
    expect(text?.tagName).toBe("PRE");
    expect(text?.textContent).toBe("a\nb");
  });

  it("escapes the text", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const el = parse(codeBlockHTML({ text: evil, copy: true, label: evil }));
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector(".code-block-text")?.textContent).toBe(evil);
  });

  it("adds a sm Copy button that carries no value", () => {
    const el = parse(codeBlockHTML({ text: "sk_live_secret", copy: true }));
    const btn = el.querySelector("button");
    expect(btn?.className).toBe("btn btn-sm code-block-copy");
    expect(btn?.textContent).toBe(t("common.buttons.copy"));
    expect(btn?.hasAttribute("data-code-copy")).toBe(true);
    expect(btn?.getAttribute("aria-live")).toBe("polite");
    expect(el.outerHTML.split("sk_live_secret")).toHaveLength(2);
  });

  it("names the button after what it copies", () => {
    const el = parse(codeBlockHTML({ text: "x", copy: true, label: "the signing secret" }));
    expect(el.querySelector("button")?.getAttribute("aria-label")).toBe(
      t("ui.code.copy_named", { name: "the signing secret" }),
    );
  });

  it("leaves the text empty for a script to fill, with ids on both ends", () => {
    const el = parse(
      codeBlockHTML({ id: "wh", textId: "wh-value", copy: true, attrs: { hidden: true } }),
    );
    expect(el.id).toBe("wh");
    expect(el.hidden).toBe(true);
    expect(el.querySelector("#wh-value")?.textContent).toBe("");
  });
});

describe("copyText", () => {
  it("uses the Clipboard API on a secure origin", async () => {
    const write = secureClipboard(() => Promise.resolve());
    expect(await copyText("hi")).toBe(true);
    expect(write).toHaveBeenCalledWith("hi");
  });

  it("falls back to the copy command over plain http", async () => {
    vi.stubGlobal("isSecureContext", false);
    const exec = legacyCopy(true);
    expect(await copyText("hi")).toBe(true);
    expect(exec).toHaveBeenCalledWith("copy");
    expect(document.querySelector(".code-copy-buffer")).toBeNull();
  });

  it("falls back when the Clipboard API refuses, and says when both fail", async () => {
    secureClipboard(() => Promise.reject(new Error("denied")));
    legacyCopy(false);
    expect(await copyText("hi")).toBe(false);
  });
});

describe("copyCodeBlock", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  it("copies its own block's text and says Copied, then Copy again", async () => {
    const write = secureClipboard(() => Promise.resolve());
    document.body.innerHTML =
      codeBlockHTML({ text: "first", copy: true }) + codeBlockHTML({ text: "second", copy: true });
    const second = document.querySelectorAll<HTMLButtonElement>("[data-code-copy]")[1];
    expect(await copyCodeBlock(second)).toBe(true);
    expect(write).toHaveBeenCalledWith("second");
    expect(second.textContent).toBe(t("ui.code.copied"));
    expect(second.dataset.state).toBe("copied");
    vi.advanceTimersByTime(COPY_FEEDBACK_MS);
    expect(second.textContent).toBe(t("common.buttons.copy"));
    expect(second.dataset.state).toBeUndefined();
  });

  it("reads the text as it is now, after a script filled it", async () => {
    const write = secureClipboard(() => Promise.resolve());
    const btn = mount(codeBlockHTML({ textId: "v", copy: true }));
    (document.getElementById("v") as HTMLElement).textContent = "filled later";
    await copyCodeBlock(btn);
    expect(write).toHaveBeenCalledWith("filled later");
  });

  it("selects the text when it cannot copy, and says how", async () => {
    vi.stubGlobal("isSecureContext", false);
    legacyCopy(false);
    const btn = mount(codeBlockHTML({ text: "by hand", copy: true }));
    expect(await copyCodeBlock(btn)).toBe(false);
    expect(btn.dataset.state).toBe("failed");
    expect(btn.textContent).toMatch(/C/);
    expect(window.getSelection()?.toString()).toBe("by hand");
  });

  it("keeps the original label across two clicks in a row", async () => {
    secureClipboard(() => Promise.resolve());
    const btn = mount(codeBlockHTML({ text: "x", copy: true }));
    await copyCodeBlock(btn);
    await copyCodeBlock(btn);
    vi.advanceTimersByTime(COPY_FEEDBACK_MS);
    expect(btn.textContent).toBe(t("common.buttons.copy"));
  });
});

describe("bindCodeCopy", () => {
  it("copies from any block under the root, added before or after", async () => {
    const write = secureClipboard(() => Promise.resolve());
    const root = document.createElement("div");
    document.body.appendChild(root);
    bindCodeCopy(root);
    bindCodeCopy(root);
    root.innerHTML = codeBlockHTML({ text: "late", copy: true });
    root.querySelector<HTMLButtonElement>("[data-code-copy]")?.click();
    await vi.waitFor(() => expect(write).toHaveBeenCalledTimes(1));
    expect(write).toHaveBeenCalledWith("late");
  });
});
