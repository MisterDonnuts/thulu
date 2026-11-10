// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  confirmDialog,
  confirmDialogHTML,
  confirmValues,
  isConfirmOpen,
  moveTasksBodyHTML,
} from "./confirm";

function dom(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
function must<T>(v: T | null | undefined): T {
  if (v === null || v === undefined) throw new Error("expected a value");
  return v;
}
const okBtn = () => must(q<HTMLButtonElement>("#confirm-ok"));
const cancelBtn = () => must(q<HTMLButtonElement>("#confirm-cancel"));
const key = (target: EventTarget, k: string) =>
  target.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
const flush = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => {
  if (isConfirmOpen()) cancelBtn().click();
  document.body.innerHTML = "";
});

describe("confirmDialogHTML", () => {
  it("draws a neutral question with a primary button", () => {
    const el = dom(confirmDialogHTML({ title: "Rotate the secret?", confirmLabel: "Rotate" }));
    expect(el.className).toBe("confirm-dialog");
    expect(el.getAttribute("role")).toBe("alertdialog");
    expect(el.getAttribute("aria-modal")).toBe("true");
    expect(el.getAttribute("aria-labelledby")).toBe("confirm-title");
    expect(el.hasAttribute("aria-describedby")).toBe(false);
    expect(el.dataset.tone).toBe("neutral");
    expect(el.querySelector("#confirm-title")?.textContent).toBe("Rotate the secret?");
    expect(el.querySelector("#confirm-ok")?.className).toBe("btn btn-primary");
    expect(el.querySelector("#confirm-ok")?.getAttribute("type")).toBe("submit");
    expect(el.querySelector("#confirm-cancel")?.className).toBe("btn btn-ghost");
    expect(el.querySelector("#confirm-cancel")?.textContent).toBe("Cancel");
    expect(el.querySelector(".confirm-dialog-icon use")?.getAttribute("href")).toBe("#i-alert");
    expect(el.querySelector(".confirm-dialog-msg")).toBeNull();
  });

  it("makes a danger one red and describes it by its message", () => {
    const el = dom(
      confirmDialogHTML({
        title: "Delete <b>x</b>?",
        message: "It goes.\nForever.",
        confirmLabel: "Delete",
        danger: true,
        icon: "trash",
      }),
    );
    expect(el.dataset.tone).toBe("danger");
    expect(el.getAttribute("aria-describedby")).toBe("confirm-msg");
    expect(el.querySelector("#confirm-msg")?.textContent).toBe("It goes.\nForever.");
    expect(el.querySelector("#confirm-title")?.textContent).toBe("Delete <b>x</b>?");
    expect(el.querySelector("#confirm-ok")?.className).toBe("btn btn-danger");
    expect(el.querySelector(".confirm-dialog-icon use")?.getAttribute("href")).toBe("#i-trash");
  });

  it("lists the details, the option, the warning and the notes, escaped", () => {
    const el = dom(
      confirmDialogHTML({
        title: "Delete git-flow?",
        message: "2 agents grant it:",
        details: ["backend-dev", "<reviewer>"],
        detailsMono: true,
        option: { label: "Also remove it", hint: "Or keep the grants", checked: true },
        warning: "The key has no write scope.",
        warningLink: { href: "/settings/?s=keys", label: "Change it" },
        notes: ["History stays."],
        confirmLabel: "Delete",
        danger: true,
      }),
    );
    const list = el.querySelector("ul.confirm-dialog-list");
    expect(list?.classList.contains("confirm-dialog-list-mono")).toBe(true);
    expect([...must(list).querySelectorAll("li")].map((li) => li.textContent)).toEqual([
      "backend-dev",
      "<reviewer>",
    ]);
    const box = must(el.querySelector<HTMLInputElement>("#confirm-option"));
    expect(box.type).toBe("checkbox");
    expect(box.name).toBe("option");
    expect(box.checked).toBe(true);
    expect(el.querySelector(".check-hint")?.textContent).toBe("Or keep the grants");
    expect(box.closest("label")?.className).toBe("check");
    expect(box.getAttribute("aria-labelledby")).toBe("confirm-option-label");
    expect(box.getAttribute("aria-describedby")).toBe("confirm-option-hint");
    const warn = must(el.querySelector(".callout.callout-warn"));
    expect(warn.textContent).toContain("The key has no write scope.");
    expect(warn.querySelector("a")?.getAttribute("href")).toBe("/settings/?s=keys");
    expect(el.querySelector(".confirm-dialog-note")?.textContent).toBe("History stays.");
    expect(el.querySelector("#confirm-error")?.hasAttribute("hidden")).toBe(true);
  });

  it("holds the confirm button until the text is typed", () => {
    const el = dom(
      confirmDialogHTML({ title: "Delete?", requireText: "acme<1>", confirmLabel: "Delete" }),
    );
    expect(el.querySelector<HTMLButtonElement>("#confirm-ok")?.disabled).toBe(true);
    const label = must(el.querySelector('label[for="confirm-require"]'));
    expect(label.querySelector("code.code-inline")?.textContent).toBe("acme<1>");
    expect(label.textContent).toBe("Type acme<1> to confirm");
    const input = must(el.querySelector<HTMLInputElement>("#confirm-require"));
    expect(input.name).toBe("");
    expect(input.getAttribute("autocomplete")).toBe("off");
  });

  it("writes the caller's body as is and takes another id prefix", () => {
    const el = dom(
      confirmDialogHTML({
        title: "Move?",
        bodyHTML: '<select name="to"></select>',
        confirmLabel: "Move",
        idPrefix: "ce",
      }),
    );
    expect(el.id).toBe("ce-dialog");
    expect(el.querySelector(".confirm-dialog-body select[name=to]")).not.toBeNull();
    expect(el.querySelector("#ce-ok")).not.toBeNull();
    expect(el.querySelector("#confirm-ok")).toBeNull();
  });
});

describe("moveTasksBodyHTML", () => {
  const dests = [
    { id: "a", label: "Alpha" },
    { id: "b<", label: "B & co" },
  ];
  it("is empty with no tasks or nowhere to go", () => {
    expect(moveTasksBodyHTML({ count: 0, destinations: dests, label: "Move to" })).toBe("");
    expect(moveTasksBodyHTML({ count: 3, destinations: [], label: "Move to" })).toBe("");
  });
  it("labels a named select of the destinations", () => {
    const el = dom(
      moveTasksBodyHTML({ count: 3, destinations: dests, label: "Move the 3 tasks to" }),
    );
    const select = must(el.querySelector("select"));
    expect(select.name).toBe("move_to");
    expect(el.querySelector("label")?.getAttribute("for")).toBe(select.id);
    expect([...select.options].map((o) => [o.value, o.text])).toEqual([
      ["a", "Alpha"],
      ["b<", "B & co"],
    ]);
  });
});

describe("confirmValues", () => {
  it("reads named controls and skips the rest", () => {
    const form = dom(`<form>
      <input type="checkbox" name="a" checked><input type="checkbox" name="b">
      <input type="radio" name="r" value="x"><input type="radio" name="r" value="y" checked>
      <select name="s"><option value="1">1</option><option value="2" selected>2</option></select>
      <input name="t" value="hi"><input value="anon"><input name="off" value="z" disabled>
    </form>`) as HTMLFormElement;
    expect(confirmValues(form)).toEqual({ a: true, b: false, r: "y", s: "2", t: "hi" });
  });
});

describe("confirmDialog", () => {
  it("answers true on the confirm button and closes", async () => {
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    expect(isConfirmOpen()).toBe(true);
    expect(document.activeElement).toBe(okBtn());
    okBtn().click();
    await expect(p).resolves.toBe(true);
    expect(isConfirmOpen()).toBe(false);
    expect(q("#confirm-overlay")?.children.length).toBe(0);
  });

  it("answers false on Cancel, Escape and the backdrop", async () => {
    let p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    cancelBtn().click();
    await expect(p).resolves.toBe(false);

    p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    key(okBtn(), "Escape");
    await expect(p).resolves.toBe(false);

    p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    must(q("#confirm-overlay")).dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await expect(p).resolves.toBe(false);
  });

  it("keeps its keys from the page behind it", async () => {
    const page = vi.fn();
    document.addEventListener("keydown", page);
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    key(okBtn(), "Escape");
    await p;
    const p2 = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    key(okBtn(), "n");
    expect(page).not.toHaveBeenCalled();
    cancelBtn().click();
    await p2;
    document.removeEventListener("keydown", page);
  });

  it("does not take Enter on Cancel as a yes", async () => {
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    cancelBtn().focus();
    key(cancelBtn(), "Enter");
    expect(isConfirmOpen()).toBe(true);
    cancelBtn().click();
    await expect(p).resolves.toBe(false);
  });

  it("gives focus back to what had it", async () => {
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    expect(document.activeElement).not.toBe(trigger);
    cancelBtn().click();
    await p;
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps Tab inside the dialog", async () => {
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    okBtn().focus();
    key(okBtn(), "Tab");
    expect(document.activeElement).toBe(cancelBtn());
    cancelBtn().click();
    await p;
  });

  it("answers the option's state with the values", async () => {
    const p = confirmDialog({
      title: "Delete?",
      confirmLabel: "Delete",
      danger: true,
      option: { label: "Also revoke", checked: true },
    });
    must(q<HTMLInputElement>("#confirm-option")).click();
    okBtn().click();
    await expect(p).resolves.toEqual({ ok: true, values: { option: false } });

    const p2 = confirmDialog({ title: "Delete?", confirmLabel: "Delete", option: { label: "x" } });
    cancelBtn().click();
    await expect(p2).resolves.toEqual({ ok: false, values: {} });
  });

  it("reads the destination from a move body and focuses it", async () => {
    const p = confirmDialog({
      title: "Delete the column?",
      confirmLabel: "Delete column",
      danger: true,
      bodyHTML: moveTasksBodyHTML({
        count: 2,
        destinations: [
          { id: "todo", label: "To do" },
          { id: "done", label: "Done" },
        ],
        label: "Move 2 tasks to",
      }),
    });
    const select = must(q<HTMLSelectElement>("#confirm-move-to"));
    expect(document.activeElement).toBe(select);
    select.value = "done";
    okBtn().click();
    await expect(p).resolves.toEqual({ ok: true, values: { move_to: "done" } });
  });

  it("appends a body element and reads its controls", async () => {
    const body = document.createElement("div");
    body.innerHTML = '<input name="reason" value="cleanup">';
    const p = confirmDialog({ title: "Archive?", confirmLabel: "Archive", body });
    expect(q(".confirm-dialog-body")?.contains(body)).toBe(true);
    okBtn().click();
    await expect(p).resolves.toEqual({ ok: true, values: { reason: "cleanup" } });
  });

  it("waits for the exact text before it confirms", async () => {
    const p = confirmDialog({
      title: "Delete the workspace?",
      confirmLabel: "Delete workspace",
      danger: true,
      requireText: "acme",
    });
    const input = must(q<HTMLInputElement>("#confirm-require"));
    expect(document.activeElement).toBe(input);
    must(q<HTMLFormElement>("#confirm-form")).dispatchEvent(
      new Event("submit", { cancelable: true }),
    );
    await flush();
    expect(isConfirmOpen()).toBe(true);
    input.value = "acm";
    input.dispatchEvent(new Event("input"));
    expect(okBtn().disabled).toBe(true);
    input.value = "acme";
    input.dispatchEvent(new Event("input"));
    expect(okBtn().disabled).toBe(false);
    okBtn().click();
    await expect(p).resolves.toBe(true);
  });

  it("lets the caller say what typed text confirms", async () => {
    const p = confirmDialog({
      title: "Delete your account?",
      confirmLabel: "Delete my account",
      danger: true,
      requireText: "Ana@Example.com",
      requireMatch: (typed) => typed.trim().toLowerCase() === "ana@example.com",
    });
    const input = must(q<HTMLInputElement>("#confirm-require"));
    input.value = " ana@example.COM ";
    input.dispatchEvent(new Event("input"));
    expect(okBtn().disabled).toBe(false);
    input.value = "ana@example.co";
    input.dispatchEvent(new Event("input"));
    expect(okBtn().disabled).toBe(true);
    cancelBtn().click();
    await expect(p).resolves.toBe(false);
  });

  it("stays open with the reason when the action fails, and closes when it works", async () => {
    let calls = 0;
    const action = vi.fn(() => {
      calls += 1;
      return calls === 1 ? Promise.reject(new Error("board has 3 tasks")) : Promise.resolve();
    });
    const p = confirmDialog({ title: "Delete?", confirmLabel: "Delete", danger: true, action });
    okBtn().click();
    expect(okBtn().disabled).toBe(true);
    expect(cancelBtn().disabled).toBe(true);
    await flush();
    expect(isConfirmOpen()).toBe(true);
    const err = must(q<HTMLElement>("#confirm-error"));
    expect(err.hidden).toBe(false);
    expect(err.querySelector(".callout-danger")?.textContent).toContain("board has 3 tasks");
    expect(okBtn().disabled).toBe(false);
    okBtn().click();
    await expect(p).resolves.toBe(true);
    expect(action).toHaveBeenCalledTimes(2);
  });

  it("answers an open question no when another opens", async () => {
    const first = confirmDialog({ title: "First?", confirmLabel: "Go" });
    const second = confirmDialog({ title: "Second?", confirmLabel: "Go" });
    await expect(first).resolves.toBe(false);
    expect(isConfirmOpen()).toBe(true);
    expect(q("#confirm-title")?.textContent).toBe("Second?");
    okBtn().click();
    await expect(second).resolves.toBe(true);
  });

  it("follows the warning's link as a no", async () => {
    const p = confirmDialog({
      title: "Go?",
      confirmLabel: "Go",
      warning: "No write scope.",
      warningLink: { href: "#keys", label: "Fix it" },
    });
    must(q<HTMLAnchorElement>(".callout-warn a")).click();
    await expect(p).resolves.toBe(false);
  });

  it("uses the overlay Layout renders", async () => {
    document.body.innerHTML =
      '<div class="confirm-overlay" id="confirm-overlay" data-open="false"></div>';
    const p = confirmDialog({ title: "Go?", confirmLabel: "Go" });
    expect(document.querySelectorAll(".confirm-overlay").length).toBe(1);
    expect(q("#confirm-overlay .confirm-dialog")).not.toBeNull();
    cancelBtn().click();
    await p;
  });
});
