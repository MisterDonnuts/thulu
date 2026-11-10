// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  fillHistoryList,
  historyChangeHTML,
  historyFilesHTML,
  historyHeadingHTML,
  historyListError,
  historyNoteHTML,
  historyPreviewHTML,
  historyRowHTML,
  historyViewClasses,
  historyViewHTML,
  openHistoryView,
} from "./history";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("historyViewHTML", () => {
  it("draws the bar, the loading list and the preview asking for a pick", () => {
    const html = historyViewHTML({ title: "History of <a>.md", pick: "Pick one" });
    expect(html).toMatch(
      /^<div class="rev-view"><div class="md-editor-bar"><button class="btn btn-sm"/,
    );
    expect(html).toContain("data-rev-back");
    expect(html).toContain('<span class="md-editor-path">History of &lt;a&gt;.md</span>');
    expect(html).toContain(
      '<ul class="rev-list" data-rev-list aria-label="History of &lt;a&gt;.md">',
    );
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('<div class="rev-preview" data-rev-preview>');
    expect(html).toContain("Pick one");
  });

  it("drops the bar and boxes the view inside a panel", () => {
    expect(historyViewClasses(true, "x")).toBe("rev-view rev-view-boxed x");
    const html = historyViewHTML({ title: "Init script", pick: "Pick", bar: false, boxed: true });
    expect(html).toMatch(/^<div class="rev-view rev-view-boxed"><div class="rev-body">/);
    expect(html).not.toContain("md-editor-bar");
  });
});

describe("historyRowHTML", () => {
  it("is a small list row button in an item, its index in data-rev", () => {
    expect(historyRowHTML({ action: "Edited", meta: "api · 17 oct" }, 3)).toBe(
      '<li><button class="row row-sm" type="button" data-rev="3"><span class="row-main">' +
        '<span class="row-top"><span class="row-title">Edited</span></span>' +
        '<span class="row-meta">api · 17 oct</span></span></button></li>',
    );
  });

  it("marks a deletion and the revision picked", () => {
    const html = historyRowHTML({ action: "Deleted", tone: "danger", current: true }, 0);
    expect(html).toContain('class="row row-sm rev-row-danger"');
    expect(html).toContain('aria-current="true"');
  });
});

describe("openHistoryView and fillHistoryList", () => {
  it("wires Back and hands back the list and the preview", () => {
    const host = document.createElement("div");
    const onBack = vi.fn();
    const parts = openHistoryView(host, { title: "H", pick: "P", onBack });
    host.querySelector<HTMLButtonElement>("[data-rev-back]")?.click();
    expect(onBack).toHaveBeenCalledTimes(1);
    expect(parts.list.matches("[data-rev-list]")).toBe(true);
    expect(parts.preview.matches("[data-rev-preview]")).toBe(true);
    expect(parts.view.parentElement).toBe(host);
  });

  it("fills the rows; a pick marks the row current and passes its index", () => {
    const host = document.createElement("div");
    const { list } = openHistoryView(host, { title: "H", pick: "P" });
    const onPick = vi.fn();
    fillHistoryList(list, [{ action: "Edited" }, { action: "Created" }], { none: "None", onPick });
    const second = list.querySelector<HTMLButtonElement>('[data-rev="1"]');
    second?.click();
    expect(onPick).toHaveBeenCalledWith(1);
    expect(second?.getAttribute("aria-current")).toBe("true");
    expect(list.querySelectorAll("[aria-current]").length).toBe(1);
  });

  it("says when there are no revisions, or why they did not load", () => {
    const list = document.createElement("ul");
    fillHistoryList(list, [], { none: "No versions yet", onPick: vi.fn() });
    expect(list.textContent).toBe("No versions yet");
    historyListError(list, "boom");
    expect(list.querySelector(".row-empty-danger")?.textContent).toBe("boom");
  });
});

describe("historyPreviewHTML", () => {
  it("shows a file's text under when and a primary sm Restore", () => {
    const html = historyPreviewHTML({
      when: "17 oct",
      body: { text: "# <Prompt>" },
      action: { label: "Restore" },
    });
    expect(html).toBe(
      '<div class="rev-preview-head"><span class="rev-when">17 oct</span>' +
        '<button class="btn btn-primary btn-sm" type="button" data-rev-restore>Restore</button></div>' +
        '<pre class="rev-text"># &lt;Prompt&gt;</pre>',
    );
  });

  it("takes facts as markup, a note for nothing to show, and no action", () => {
    const facts = historyPreviewHTML({ when: "w", body: { html: "<dl></dl>" } });
    expect(facts).toContain('<div class="rev-text rev-facts"><dl></dl></div>');
    expect(facts).not.toContain("<button");
    const empty = historyPreviewHTML({ when: "w", body: { empty: "Deleted" } });
    expect(empty).toContain("empty-state");
    expect(empty).toContain("Deleted");
  });

  it("lets the action be another button: Load, with its own hook", () => {
    const html = historyPreviewHTML({
      when: "w",
      body: { text: "" },
      action: { label: "Load", attrs: { "data-rev-load": true } },
    });
    expect(html).toContain(
      '<button class="btn btn-primary btn-sm" type="button" data-rev-load>Load</button>',
    );
  });
});

describe("historyPreviewHTML with facts and files", () => {
  it("lists the facts above the text and folds each file under it, escaped", () => {
    const html = historyPreviewHTML({
      when: "w",
      body: { text: "npm run dev" },
      facts: ["Ports: 3000", "<b>ready</b>"],
      files: [{ path: "seed.mjs", text: "<x>" }],
    });
    expect(html).toContain(
      '<ul class="rev-lead"><li>Ports: 3000</li><li>&lt;b&gt;ready&lt;/b&gt;</li></ul>',
    );
    expect(html.indexOf("rev-lead")).toBeLessThan(html.indexOf("rev-text"));
    expect(html.indexOf("rev-text")).toBeLessThan(html.indexOf("rev-files"));
    expect(html).toContain('<details class="disclosure disclosure-code rev-file">');
    expect(html).toContain("<span>seed.mjs</span>");
    expect(html).toContain("&lt;x&gt;</pre>");
  });

  it("writes neither list without facts or files", () => {
    const html = historyPreviewHTML({ when: "w", body: { text: "" }, facts: [], files: [] });
    expect(html).not.toContain("rev-lead");
    expect(html).not.toContain("rev-files");
    expect(historyFilesHTML([])).toBe("");
  });

  it("takes when as markup the caller escaped", () => {
    const html = historyPreviewHTML({
      when: "",
      whenHTML: 'by <a href="/t">t</a>',
      body: { text: "" },
    });
    expect(html).toContain('<span class="rev-when">by <a href="/t">t</a></span>');
  });
});

describe("facts helpers", () => {
  it("writes headings, notes and a change as before and after", () => {
    expect(historyHeadingHTML("What changed")).toBe('<h5 class="rev-heading">What changed</h5>');
    expect(historyNoteHTML("Nothing")).toBe('<p class="rev-note">Nothing</p>');
    expect(
      historyChangeHTML("", "opus", { before: "Before:", after: "After:", empty: "(empty)" }),
    ).toBe(
      '<span class="rev-before">Before: <em>(empty)</em></span><span class="rev-after">After: opus</span>',
    );
  });
});
