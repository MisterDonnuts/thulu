// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { t } from "../../i18n";
import { chordText, MOD_KEY } from "../shortcuts";
import {
  composerClasses,
  composerHTML,
  composerIds,
  composerInputA11y,
  composerKeyShortcuts,
  composerParts,
  composerSubmits,
  composerText,
  growComposer,
  mdClasses,
  mdHTML,
  messageClasses,
  messageEl,
  messageHTML,
  messageSpeaker,
  setComposerReady,
} from "./conversation";

function el(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

function q(root: ParentNode, sel: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(sel);
  if (!found) throw new Error(`nothing matches ${sel}`);
  return found;
}

describe("mdClasses / mdHTML", () => {
  it("writes md, compact, then the extras", () => {
    expect(mdClasses()).toBe("md");
    expect(mdClasses({ compact: true, cls: "brief-doc" })).toBe("md md-compact brief-doc");
  });

  it("wraps the renderer's HTML as is, with its id and attributes", () => {
    const html = mdHTML("<p><strong>hi</strong></p>", {
      id: "ov-display",
      compact: true,
      attrs: { "data-sec": "plan" },
    });
    const node = el(html);
    expect(node.className).toBe("md md-compact");
    expect(node.id).toBe("ov-display");
    expect(node.dataset.sec).toBe("plan");
    expect(node.innerHTML).toBe("<p><strong>hi</strong></p>");
  });
});

describe("messageClasses", () => {
  it("writes msg, the role, error, folded, then the extras", () => {
    expect(messageClasses({ role: "user" })).toBe("msg msg-user");
    expect(messageClasses({ role: "agent", tone: "error", cls: "x" })).toBe(
      "msg msg-agent msg-error x",
    );
    expect(messageClasses({ role: "user", folded: true })).toBe("msg msg-user msg-folded");
  });

  it("throws on a role or a tone that does not exist", () => {
    // @ts-expect-error a role the type does not allow
    expect(() => messageClasses({ role: "assistant" })).toThrow(/no role "assistant"/);
    // @ts-expect-error a tone the type does not allow
    expect(() => messageClasses({ role: "user", tone: "warn" })).toThrow(/no tone "warn"/);
  });
});

describe("messageSpeaker", () => {
  it("names who wrote it, the error first", () => {
    expect(messageSpeaker({ role: "user" })).toBe(t("ui.message.you"));
    expect(messageSpeaker({ role: "agent" })).toBe(t("ui.message.agent"));
    expect(messageSpeaker({ role: "agent", author: "dev" })).toBe(
      t("ui.message.named", { name: "dev" }),
    );
    expect(messageSpeaker({ role: "system", author: "dev" })).toBe(t("ui.message.system"));
    expect(messageSpeaker({ role: "agent", tone: "error", author: "dev" })).toBe(
      t("ui.message.error"),
    );
  });
});

describe("messageHTML", () => {
  it("writes a person's markdown bubble with the hidden speaker and the meta line", () => {
    const node = el(messageHTML({ role: "user", bodyHTML: "<p>hola</p>", meta: "17:54" }));
    expect(node.className).toBe("msg msg-user");
    expect(node.querySelector(".visually-hidden")?.textContent.trim()).toBe(t("ui.message.you"));
    const body = q(node, ".msg-body");
    expect(body.className).toBe("msg-body md md-compact");
    expect(body.innerHTML).toBe("<p>hola</p>");
    expect(node.querySelector(".msg-meta")?.textContent).toBe("17:54");
    expect(node.querySelector(".msg-ico")).toBeNull();
  });

  it("escapes plain text, the lead, the fold's summary and the meta", () => {
    const html = messageHTML({
      role: "agent",
      text: "<b>x</b>",
      lead: "<i>lead</i>",
      meta: "<u>m</u>",
    });
    expect(html).not.toContain("<b>");
    expect(html).not.toContain("<i>");
    expect(html).not.toContain("<u>");
    const node = el(html);
    expect(node.querySelector(".msg-body")?.className).toBe("msg-body msg-body-plain");
    expect(node.querySelector(".msg-body")?.textContent).toBe("<b>x</b>");
    expect(node.querySelector(".msg-lead")?.textContent).toBe("<i>lead</i>");
  });

  it("folds the body under its summary and marks the message folded", () => {
    const node = el(messageHTML({ role: "user", fold: "Context <ctx>", bodyHTML: "<p>brief</p>" }));
    expect(node.classList.contains("msg-folded")).toBe(true);
    const fold = q(node, "details.msg-fold");
    expect(fold.querySelector("summary")?.textContent).toBe("Context <ctx>");
    expect(fold.querySelector(".msg-body")?.innerHTML).toBe("<p>brief</p>");
  });

  it("gives an error its glyph and its word, so it does not rest on the red", () => {
    const node = el(messageHTML({ role: "agent", tone: "error", text: "Refused." }));
    expect(node.classList.contains("msg-error")).toBe(true);
    expect(node.querySelector("svg.msg-ico use")?.getAttribute("href")).toBe("#i-warn");
    expect(node.querySelector(".visually-hidden")?.textContent.trim()).toBe(t("ui.message.error"));
  });

  it("writes what follows the body and the meta markup as given, and no empty meta", () => {
    const node = el(
      messageHTML({
        role: "agent",
        bodyHTML: "<p>ok</p>",
        afterHTML: '<details class="chat-tools"></details>',
        metaHTML: '<button class="lv-link">open</button>',
        id: "m1",
        attrs: { "data-id": "abc" },
      }),
    );
    expect(node.id).toBe("m1");
    expect(node.dataset.id).toBe("abc");
    const kids = [...node.children].map((c) => c.className);
    expect(kids).toEqual(["visually-hidden", "msg-body md md-compact", "chat-tools", "msg-meta"]);
    expect(node.querySelector(".msg-meta button")?.textContent).toBe("open");
    expect(el(messageHTML({ role: "system", text: "x" })).querySelector(".msg-meta")).toBeNull();
  });

  it("messageEl is the same message as an element", () => {
    const o = { role: "agent" as const, bodyHTML: "<p>a</p>", meta: "1s" };
    expect(messageEl(o).outerHTML).toBe(messageHTML(o));
  });
});

describe("composerClasses / composerIds / composerText", () => {
  it("writes composer and its variant, comment by default", () => {
    expect(composerClasses()).toBe("composer composer-comment");
    expect(composerClasses({ variant: "chat", cls: "sticky" })).toBe(
      "composer composer-chat sticky",
    );
    // @ts-expect-error a variant the type does not allow
    expect(() => composerClasses({ variant: "inline" })).toThrow(/no variant "inline"/);
  });

  it("derives the parts' ids from the root's, and takes the ones given", () => {
    expect(composerIds(undefined)).toEqual({});
    expect(composerIds(undefined, { input: "dr-reply" })).toEqual({ input: "dr-reply" });
    const ids = composerIds("chat", { input: "chat-input" });
    expect(ids.input).toBe("chat-input");
    expect(ids.submit).toBe("chat-submit");
    expect(ids.error).toBe("chat-error");
  });

  it("fills the rows and the words each variant uses", () => {
    const comment = composerText({});
    expect(comment.rows).toBe(1);
    expect(comment.submitLabel).toBe(t("ui.composer.post"));
    expect(comment.submitTitle).toBe(
      t("ui.composer.post_key", { keys: chordText([MOD_KEY, t("common.kbd.enter")]) }),
    );
    expect(comment.cancelLabel).toBe(t("common.buttons.cancel"));
    const chat = composerText({ variant: "chat" });
    expect(chat.rows).toBe(2);
    expect(chat.submitLabel).toBe(t("ui.composer.send"));
    expect(chat.submitTitle).toBe(t("ui.composer.send_key"));
    const thread = composerText({ variant: "thread", cancel: "Close", attach: { label: "Add" } });
    expect(thread.rows).toBe(2);
    expect(thread.cancelLabel).toBe("Close");
    expect(thread.attachLabel).toBe("Add");
    expect(thread.attachTitle).toBe(t("ui.composer.attach_title"));
  });
});

describe("composerInputA11y", () => {
  it("names the box and points it at the audience and the error", () => {
    const ids = composerIds("c");
    expect(composerInputA11y({ label: "Reply", ids, audience: true })).toEqual({
      "aria-label": "Reply",
      "aria-labelledby": undefined,
      "aria-describedby": "c-audience c-error",
    });
    expect(composerInputA11y({ labelledBy: "h", label: "x", ids, audience: false })).toEqual({
      "aria-label": undefined,
      "aria-labelledby": "h",
      "aria-describedby": "c-error",
    });
    expect(composerInputA11y({ label: "x", ids: {}, audience: true })["aria-describedby"]).toBe(
      undefined,
    );
  });

  it("refuses a box nobody named", () => {
    expect(() => composerInputA11y({ ids: {}, audience: false })).toThrow(/needs a label/);
  });
});

describe("composerSubmits", () => {
  const key = (k: Partial<KeyboardEvent>) => ({
    key: "Enter",
    shiftKey: false,
    altKey: false,
    metaKey: false,
    ctrlKey: false,
    isComposing: false,
    ...k,
  });

  it("sends a chat on Enter and keeps Shift+Enter for a new line", () => {
    expect(composerSubmits(key({}), "chat")).toBe(true);
    expect(composerSubmits(key({ shiftKey: true }), "chat")).toBe(false);
    expect(composerSubmits(key({ key: "a" }), "chat")).toBe(false);
    expect(composerKeyShortcuts("chat")).toBe("Enter");
  });

  it("sends a comment or a reply on Cmd or Ctrl with Enter only", () => {
    expect(composerSubmits(key({}))).toBe(false);
    expect(composerSubmits(key({ metaKey: true }))).toBe(true);
    expect(composerSubmits(key({ ctrlKey: true }), "thread")).toBe(true);
    expect(composerSubmits(key({ ctrlKey: true, altKey: true }))).toBe(false);
    expect(composerKeyShortcuts()).toBe("Meta+Enter Control+Enter");
  });

  it("never sends while an input method is composing", () => {
    expect(composerSubmits(key({ isComposing: true }), "chat")).toBe(false);
    expect(composerSubmits(key({ isComposing: true, metaKey: true }))).toBe(false);
  });
});

describe("composerHTML", () => {
  it("writes the comment box: textarea, files, error, audience, Attach and Post", () => {
    const root = el(
      composerHTML({
        id: "act-composer",
        ids: { input: "dr-reply", audience: "act-audience", error: "act-err" },
        labelledBy: "ov-activity-h",
        placeholder: "Write…",
        audience: true,
        attach: true,
      }),
    );
    expect(root.tagName).toBe("DIV");
    expect(root.className).toBe("composer composer-comment");
    const p = composerParts(root);
    expect(p.input?.id).toBe("dr-reply");
    expect(p.input?.getAttribute("rows")).toBe("1");
    expect(p.input?.getAttribute("aria-labelledby")).toBe("ov-activity-h");
    expect(p.input?.getAttribute("aria-label")).toBeNull();
    expect(p.input?.getAttribute("aria-describedby")).toBe("act-audience act-err");
    expect(p.audience?.id).toBe("act-audience");
    expect(p.error?.hidden).toBe(true);
    expect(p.error?.getAttribute("role")).toBe("alert");
    expect(p.atts?.hidden).toBe(true);
    expect(p.attach?.textContent).toBe(t("ui.composer.attach"));
    expect(p.file?.type).toBe("file");
    expect(p.file?.multiple).toBe(true);
    expect(p.cancel).toBeNull();
    expect(p.submit?.textContent).toBe(t("ui.composer.post"));
    expect(p.submit?.type).toBe("button");
    expect(p.submit?.className).toBe("btn btn-sm composer-submit");
    expect(p.submit?.getAttribute("aria-keyshortcuts")).toBe("Meta+Enter Control+Enter");
    const actions = q(root, ".composer-actions");
    expect(actions.lastElementChild).toBe(p.submit);
  });

  it("writes a reply with its draft, Cancel and a primary Reply when there is text", () => {
    const root = el(
      composerHTML({
        variant: "thread",
        label: "Reply in thread",
        value: "draft <b>",
        audience: "Goes to dev",
        cancel: true,
        ready: true,
        submitLabel: "Reply",
        inputCls: "thread-input",
        attrs: { "data-thread": "c1" },
        cancelAttrs: { "data-thread-cancel": true },
        submitAttrs: { "data-thread-post": true },
      }),
    );
    expect(root.dataset.thread).toBe("c1");
    const p = composerParts(root);
    expect(p.input?.className).toBe("composer-input thread-input");
    expect(p.input?.value).toBe("draft <b>");
    expect(p.input?.getAttribute("rows")).toBe("2");
    expect(p.input?.getAttribute("aria-label")).toBe("Reply in thread");
    expect(p.input?.hasAttribute("id")).toBe(false);
    expect(p.audience?.textContent).toBe("Goes to dev");
    expect(p.cancel?.hasAttribute("data-thread-cancel")).toBe(true);
    expect(p.submit?.classList.contains("btn-primary")).toBe(true);
    expect(p.submit?.hasAttribute("data-thread-post")).toBe(true);
    expect(p.attach).toBeNull();
    expect(p.atts).toBeNull();
  });

  it("writes a chat as a form with the send glyph beside the box", () => {
    const root = el(
      composerHTML({
        variant: "chat",
        id: "chat",
        ids: { input: "chat-input", submit: "chat-send" },
        label: "Message",
        placeholder: "Write to the agent…",
      }),
    );
    expect(root.tagName).toBe("FORM");
    const row = q(root, ".composer-row");
    const p = composerParts(root);
    expect(row.contains(p.input)).toBe(true);
    expect(row.contains(p.submit)).toBe(true);
    expect(p.submit?.type).toBe("submit");
    expect(p.submit?.className).toBe(
      "icon-btn icon-btn-lg btn-primary composer-submit composer-send",
    );
    expect(p.submit?.getAttribute("aria-label")).toBe(t("ui.composer.send"));
    expect(p.submit?.getAttribute("aria-keyshortcuts")).toBe("Enter");
    expect(root.querySelector(".composer-foot")).toBeNull();
  });

  it("gives a chat a foot for the extras, and a div one a plain button", () => {
    const root = el(
      composerHTML({
        variant: "chat",
        as: "div",
        label: "Message",
        extraHTML: '<label class="check">interrupt</label>',
      }),
    );
    expect(root.tagName).toBe("DIV");
    expect(root.querySelector(".composer-foot .check")).not.toBeNull();
    expect(composerParts(root).submit?.type).toBe("button");
  });

  it("refuses a composer without a name", () => {
    expect(() => composerHTML({ placeholder: "x" })).toThrow(/needs a label/);
  });
});

describe("setComposerReady", () => {
  it("turns a comment's submit primary and back, and leaves a chat's alone", () => {
    const comment = el(composerHTML({ label: "c" }));
    setComposerReady(comment, true);
    expect(composerParts(comment).submit?.classList.contains("btn-primary")).toBe(true);
    setComposerReady(comment, false);
    expect(composerParts(comment).submit?.classList.contains("btn-primary")).toBe(false);
    const chat = el(composerHTML({ variant: "chat", label: "c" }));
    setComposerReady(chat, false);
    expect(composerParts(chat).submit?.classList.contains("btn-primary")).toBe(true);
  });
});

describe("growComposer", () => {
  it("caps the height at maxLines and keeps the rows when not laid out", () => {
    const ta = document.createElement("textarea");
    ta.style.lineHeight = "20px";
    ta.style.paddingTop = "6px";
    ta.style.paddingBottom = "6px";
    ta.style.borderTopWidth = "1px";
    ta.style.borderBottomWidth = "1px";
    ta.style.borderStyle = "solid";
    document.body.appendChild(ta);

    growComposer(ta);
    expect(ta.style.getPropertyValue("--composer-h")).toBe("");

    Object.defineProperty(ta, "scrollHeight", { configurable: true, value: 1000 });
    growComposer(ta, 3);
    expect(ta.style.getPropertyValue("--composer-h")).toBe(`${20 * 3 + 12 + 2}px`);

    Object.defineProperty(ta, "scrollHeight", { configurable: true, value: 40 });
    growComposer(ta, 3);
    expect(ta.style.getPropertyValue("--composer-h")).toBe("42px");
    ta.remove();
  });
});
