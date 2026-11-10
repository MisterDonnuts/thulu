// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { TestCase, TestCaseRun } from "./test-cases";
import {
  caseFormProblem,
  groupFilesByReport,
  hasTestCasesSection,
  offersRemoveSection,
  reportsNewestFirst,
  runCasesFormHTML,
  runCasesProblem,
  setByHand,
  withoutEmbeds,
  movedPosition,
  plainReportTitle,
  runHTML,
  testCaseFormHTML,
  testCasesBlockHTML,
  testCasesSignature,
  type TestCasesInput,
} from "./test-cases-drawer";

const deps = {
  md: (s: string) => `<p>${s}</p>`,
  attachmentURL: (id: string) => `http://d/task-attachments/${id}`,
  openKeys: new Set<string>(),
  openReports: new Set<string>(),
};

function tc(over: Partial<TestCase> = {}): TestCase {
  return {
    id: "c1",
    task_id: "t1",
    key: "TC-1",
    title: "Login works",
    description: "",
    steps: "",
    expected: "",
    priority: "medium",
    status: "pending",
    covered: false,
    position: 1,
    created_by: "",
    updated_by: "",
    created_at: "2026-09-30T10:00:00Z",
    updated_at: "2026-09-30T10:00:00Z",
    runs: [],
    ...over,
  };
}

function run(over: Partial<TestCaseRun> = {}): TestCaseRun {
  return {
    id: "r1",
    test_case_id: "c1",
    task_id: "t1",
    verdict: "pass",
    observed: "",
    note: "",
    video_attachment_id: "",
    take_attachment_id: "",
    report_id: "",
    comment_id: "",
    capture_attachment_ids: [],
    author: "tester",
    ran_at: new Date().toISOString(),
    ...over,
  };
}

function input(over: Partial<TestCasesInput> = {}): TestCasesInput {
  return { cases: [], attachments: [], comments: [], body: "", ...over };
}

function q<T extends Element = Element>(root: ParentNode, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

function dom(html: string): HTMLElement {
  const box = document.createElement("div");
  box.innerHTML = html;
  return box;
}

describe("hasTestCasesSection", () => {
  it("finds the section by its slug", () => {
    expect(hasTestCasesSection("intro\n\n## Test Cases\n- [ ] a")).toBe(true);
    expect(hasTestCasesSection("## test-cases\n")).toBe(true);
  });

  it("ignores other levels, other sections and fenced code", () => {
    expect(hasTestCasesSection("### Test Cases\n- [ ] a")).toBe(false);
    expect(hasTestCasesSection("## Goal\n- [ ] a")).toBe(false);
    expect(hasTestCasesSection("```\n## Test Cases\n```")).toBe(false);
  });
});

describe("testCasesBlockHTML", () => {
  it("offers the import only when the body has the section and there are no rows", () => {
    const body = "## Test Cases\n- [ ] a";
    expect(testCasesBlockHTML(input({ body }), deps)).toContain('data-tc-act="import"');
    expect(testCasesBlockHTML(input({ body, cases: [tc()] }), deps)).not.toContain(
      'data-tc-act="import"',
    );
    expect(testCasesBlockHTML(input(), deps)).not.toContain('data-tc-act="import"');
  });

  it("says so when there are no cases, without a progress bar", () => {
    const html = testCasesBlockHTML(input(), deps);
    expect(html).toContain("No test cases yet.");
    expect(html).toContain('class="empty-state empty-state-inline empty-state-dashed"');
    expect(html).not.toContain("seg-progress");
    expect(html).toContain('data-tc-act="add"');
  });

  it("gives a reader the cases and their state with no control that changes them", () => {
    const html = testCasesBlockHTML(
      input({
        body: "## Test Cases\n- [ ] a",
        cases: [tc({ key: "TC-1", status: "fail", covered: true })],
      }),
      { ...deps, readOnly: true, runCases: { block: "", reason: "" } },
    );
    const box = dom(html);
    expect(box.querySelectorAll("[data-tc-act]")).toHaveLength(0);
    expect(box.querySelector("select, input")).toBeNull();
    expect(q(box, ".tc-bar").textContent).toContain("Tested by someone");
    expect(q(box, ".tc-key").textContent).toBe("TC-1");
  });

  it("counts the cases per status in the header", () => {
    const html = testCasesBlockHTML(
      input({
        cases: [
          tc({ key: "TC-1", status: "pass", covered: true }),
          tc({ key: "TC-2", status: "fail", covered: true }),
          tc({ key: "TC-3" }),
        ],
      }),
      deps,
    );
    const tally = q(dom(html), ".tc-tally");
    expect(tally.textContent).toBe("1 pass1 fail1 pending");
    expect(tally.querySelectorAll(".tc-tally-item svg")).toHaveLength(3);
    expect(html).toContain('class="seg-progress seg-progress-xs"');
    expect(q(dom(html), ".sec-title .count").textContent).toBe("3");
  });

  it("keeps the row to status, key, title and last run; controls live in the detail", () => {
    const box = dom(
      testCasesBlockHTML(
        input({ cases: [tc({ status: "fail", covered: true }), tc({ id: "c2", key: "TC-2" })] }),
        { ...deps, openKeys: new Set(["TC-2"]) },
      ),
    );
    const first = q<HTMLDetailsElement>(box, "#tc-TC-1");
    expect(first.open).toBe(false);
    expect(q<HTMLDetailsElement>(box, "#tc-TC-2").open).toBe(true);
    const summary = q(first, "summary");
    expect(summary.querySelector("select, input, button")).toBeNull();
    expect(q(summary, ".tc-status").getAttribute("aria-label")).toBe("Fail");
    expect(q(summary, ".tc-last").textContent).toBe("Not run yet");
    const sel = q<HTMLSelectElement>(first, '.tc-bar select[data-tc-act="status"]');
    expect(sel.value).toBe("fail");
    expect(sel.className).toBe("badge badge-danger badge-pill tc-status-select");
    expect(q<HTMLInputElement>(first, '.tc-bar [data-tc-act="covered"]').checked).toBe(true);
    expect(q<HTMLButtonElement>(first, '[data-tc-act="up"]').disabled).toBe(true);
    expect(q<HTMLButtonElement>(box, '#tc-TC-2 [data-tc-act="down"]').disabled).toBe(true);
  });

  it("shows priority in the row only when it is high or critical", () => {
    const box = dom(
      testCasesBlockHTML(
        input({
          cases: [
            tc({ key: "TC-1", priority: "medium" }),
            tc({ key: "TC-2", priority: "high" }),
            tc({ key: "TC-3", priority: "critical" }),
          ],
        }),
        deps,
      ),
    );
    expect(box.querySelector("#tc-TC-1 summary .tag")).toBeNull();
    expect(q(box, "#tc-TC-2 summary .tag").textContent).toBe("High");
    expect(q(box, "#tc-TC-2 summary .tag").className).toBe("tag tag-warn tag-inline");
    expect(q(box, "#tc-TC-3 summary .tag").getAttribute("title")).toBe("Priority");
  });

  it("sums up the runs in the row, newest first in the detail", () => {
    const older = run({ id: "r1", verdict: "fail", ran_at: "2026-09-01T10:00:00Z" });
    const newer = run({ id: "r2", verdict: "pass", ran_at: new Date().toISOString() });
    const box = dom(testCasesBlockHTML(input({ cases: [tc({ runs: [older, newer] })] }), deps));
    expect(q(box, ".tc-last").textContent).toBe("2 runs · just now");
    expect(q(box, ".tc-runs > .tc-run").className).toContain("tc-run-pass");
    const folded = q<HTMLDetailsElement>(box, "details.tc-older");
    expect(folded.open).toBe(false);
    expect(q(folded, "summary").textContent).toBe("1 earlier run");
    expect(q(folded, ".tc-run").className).toContain("tc-run-fail");
  });

  it("renders description, steps and expected through the page's markdown", () => {
    const html = testCasesBlockHTML(
      input({ cases: [tc({ steps: "1. open", expected: "it opens" })] }),
      deps,
    );
    expect(html).toContain("<p>1. open</p>");
    expect(html).toContain("<p>it opens</p>");
    expect(html).not.toContain("Description</div>");
  });

  it("escapes titles and keys", () => {
    const html = testCasesBlockHTML(
      input({ cases: [tc({ title: "<img src=x onerror=alert(1)>" })] }),
      deps,
    );
    expect(html).not.toContain("<img");
  });
});

describe("runHTML", () => {
  const atts = [
    { id: "v1", filename: "run.webm", mime_type: "video/webm" },
    { id: "k1", filename: "take.json", mime_type: "application/json" },
    { id: "p1", filename: "a.png", mime_type: "image/png" },
  ];

  it("shows the video and captures as thumbnails and links the take and the report", () => {
    const box = dom(
      runHTML(
        run({
          video_attachment_id: "v1",
          take_attachment_id: "k1",
          capture_attachment_ids: ["p1", "gone"],
          comment_id: "cm1",
          observed: "the page loaded",
        }),
        input({ attachments: atts, comments: [{ id: "cm1" }] }),
        deps,
      ),
    );
    const video = q(box, ".tc-thumb-video video");
    expect(video.getAttribute("src")).toBe("http://d/task-attachments/v1#t=0.1");
    expect(video.hasAttribute("controls")).toBe(false);
    expect(video.getAttribute("preload")).toBe("metadata");
    expect(q(box, ".tc-run-links a").getAttribute("href")).toBe("http://d/task-attachments/k1");
    expect(q(box, ".tc-run-links a").textContent).toBe("Take");
    const thumbs = box.querySelectorAll<HTMLElement>("[data-lightbox]");
    expect([...thumbs].map((b) => b.dataset.lightbox)).toEqual(["v1", "p1"]);
    expect(thumbs[1].dataset.lightboxSet).toBe("v1,p1");
    expect(box.textContent).toContain("Some captures were deleted");
    expect(q(box, "[data-tc-report]").getAttribute("data-tc-report")).toBe("cm1");
    expect(box.textContent).toContain("the page loaded");
    expect(box.textContent).toContain("just now");
  });

  it("signs a run the way Activity does: You for the viewer, never the bare dev-mode user", () => {
    const author = (who: string, viewer?: { name: string } | null) =>
      q(dom(runHTML(run({ author: who }), input(), { ...deps, viewer })), ".tc-run-author")
        .textContent;
    expect(author("user", { name: "user" })).toBe("You");
    expect(author("user")).toBe("Someone");
    expect(author("ana@example.com", { name: "user" })).toBe("ana@example.com");
    expect(author("qa", { name: "user" })).toBe("qa");
    expect(dom(runHTML(run({ author: "" }), input(), deps)).querySelector(".tc-run-author")).toBe(
      null,
    );
  });

  it("folds the run's report under it and links it in the Reports tab", () => {
    const rep = { id: "rp1", title: "✅ SHOP-142 · coupon", body: "| Case | Expected |" };
    const folded = dom(runHTML(run({ report_id: "rp1" }), input({ reports: [rep] }), deps));
    const details = q<HTMLDetailsElement>(folded, "details.tc-report");
    expect(details.open).toBe(false);
    expect(details.getAttribute("data-run")).toBe("r1");
    expect(q(details, ".tc-report-title").textContent).toBe("SHOP-142 · coupon");
    expect(q(details, ".md.md-compact").innerHTML).toBe("<p>| Case | Expected |</p>");
    expect(q(folded, "[data-tc-open-report]").getAttribute("data-tc-open-report")).toBe("rp1");
    expect(folded.querySelector("[data-tc-report]")).toBeNull();

    const open = dom(
      runHTML(run({ report_id: "rp1" }), input({ reports: [rep] }), {
        ...deps,
        openReports: new Set(["r1"]),
      }),
    );
    expect(q<HTMLDetailsElement>(open, "details.tc-report").open).toBe(true);
  });

  it("links a report that has not loaded yet and marks one that is gone", () => {
    const loading = dom(runHTML(run({ report_id: "rp1" }), input(), deps));
    expect(loading.querySelector("details.tc-report")).toBeNull();
    expect(q(loading, "[data-tc-open-report]").textContent).toBe("Open in Reports");

    const gone = dom(runHTML(run({ report_id: "rp1" }), input({ reports: [] }), deps));
    expect(gone.querySelector("[data-tc-open-report]")).toBeNull();
    expect(gone.textContent).toContain("report deleted");
  });

  it("repaints when a run's report arrives", () => {
    const cases = [tc({ runs: [run({ report_id: "rp1" })] })];
    const before = testCasesSignature(input({ cases }));
    const after = testCasesSignature(
      input({ cases, reports: [{ id: "rp1", title: "t", body: "b" }] }),
    );
    expect(after).not.toBe(before);
  });

  it("marks evidence whose file or comment is gone", () => {
    const text = dom(
      runHTML(
        run({ video_attachment_id: "v9", take_attachment_id: "k9", comment_id: "cm9" }),
        input(),
        deps,
      ),
    ).textContent;
    expect(text).toContain("video: file deleted");
    expect(text).toContain("The take was deleted");
    expect(text).toContain("report deleted");
  });

  it("links a video attachment the browser cannot play inline", () => {
    const html = runHTML(run({ video_attachment_id: "k1" }), input({ attachments: atts }), deps);
    expect(html).not.toContain("<video");
    expect(html).toContain("<span>take.json</span></a>");
  });

  it("shows a capture that is not an image as a file chip", () => {
    const box = dom(
      runHTML(run({ capture_attachment_ids: ["k1"] }), input({ attachments: atts }), deps),
    );
    expect(box.querySelector("[data-lightbox]")).toBeNull();
    expect(q(box, "a.tc-file").textContent).toBe("take.json");
  });
});

describe("plainReportTitle", () => {
  it("drops the status emoji older reports start with", () => {
    expect(plainReportTitle("✅ SHOP-142 · coupon")).toBe("SHOP-142 · coupon");
    expect(plainReportTitle("❌ SHOP-142-C")).toBe("SHOP-142-C");
    expect(plainReportTitle("⚠️ x")).toBe("x");
    expect(plainReportTitle("SHOP-142 · coupon · pass")).toBe("SHOP-142 · coupon · pass");
    expect(plainReportTitle("")).toBe("");
  });
});

describe("testCasesSignature", () => {
  const base = input({ cases: [tc({ runs: [run({ video_attachment_id: "v1" })] })] });

  it("is stable for the same data", () => {
    expect(testCasesSignature(base)).toBe(testCasesSignature({ ...base }));
  });

  it("changes on a status flip, a new run, or a file disappearing", () => {
    const sig = testCasesSignature(base);
    expect(
      testCasesSignature({ ...base, cases: [tc({ ...base.cases[0], status: "pass" })] }),
    ).not.toBe(sig);
    expect(
      testCasesSignature({
        ...base,
        cases: [tc({ runs: [run({ id: "r2" }), ...(base.cases[0].runs ?? [])] })],
      }),
    ).not.toBe(sig);
    expect(
      testCasesSignature({ ...base, attachments: [{ id: "v1", filename: "v.webm" }] }),
    ).not.toBe(sig);
  });

  it("changes when the import offer appears", () => {
    expect(testCasesSignature(input({ body: "## Test Cases\n- [ ] a" }))).not.toBe(
      testCasesSignature(input()),
    );
  });
});

describe("testCaseFormHTML", () => {
  it("prefills an edit form and escapes it", () => {
    const box = dom(
      testCaseFormHTML({ title: 'a "quoted" <b>', priority: "high", steps: "1. go" }),
    );
    expect(q<HTMLInputElement>(box, '[name="title"]').value).toBe('a "quoted" <b>');
    expect(q<HTMLSelectElement>(box, '[name="priority"]').value).toBe("high");
    expect(q<HTMLTextAreaElement>(box, '[name="steps"]').value).toBe("1. go");
  });

  it("defaults to medium on an add form", () => {
    const box = dom(testCaseFormHTML());
    expect(q<HTMLSelectElement>(box, '[name="priority"]').value).toBe("medium");
    expect(q<HTMLInputElement>(box, '[name="title"]').value).toBe("");
  });

  it("writes Fields whose labels name their controls, with ids of their own per form", () => {
    const box = dom(testCaseFormHTML(undefined, "tc-add") + testCaseFormHTML({}, "tc-edit-TC-1"));
    const labels = [...box.querySelectorAll<HTMLLabelElement>("label.field-label")];
    expect(labels.length).toBe(10);
    for (const l of labels) expect(box.querySelector(`[id="${l.htmlFor}"]`)).not.toBeNull();
    expect(new Set(labels.map((l) => l.htmlFor)).size).toBe(10);
    expect(q<HTMLElement>(box, "#tc-add-title-error").hidden).toBe(true);
    expect(box.querySelectorAll(".form-error[role=alert]").length).toBe(2);
    expect(box.querySelector(".tc-form-err")).toBeNull();
  });
});

describe("case detail", () => {
  it("draws covered as a Check and the parts under section heads", () => {
    const box = dom(
      testCasesBlockHTML(
        {
          cases: [tc({ covered: true, steps: "1. go", expected: "in" })],
          attachments: [],
          comments: [],
          body: "",
        },
        deps,
      ),
    );
    const cov = q<HTMLInputElement>(box, 'label.check > input[data-tc-act="covered"]');
    expect(cov.checked).toBe(true);
    expect(cov.getAttribute("aria-label")).toContain("TC-1");
    const heads = [...box.querySelectorAll(".tc-detail .sec-head.sec-head-sm > h4.sec-title")];
    expect(heads.map((h) => h.textContent)).toEqual(["Steps", "Expected result", "Latest run"]);
    expect(box.querySelector(".tc-sub, .tc-covered")).toBeNull();
  });
});

describe("form problems", () => {
  it("asks for a title", () => {
    expect(caseFormProblem({ title: "  " })).toBe("title");
    expect(caseFormProblem({ title: "Login" })).toBeNull();
  });

  it("points at the first field of the run form that cannot go", () => {
    const ok = { url: "https://a.test", compare: "", keys: ["TC-1"] };
    expect(runCasesProblem(ok)).toBeNull();
    expect(runCasesProblem({ ...ok, url: "a.test" })).toBe("url");
    expect(runCasesProblem({ ...ok, compare: "ftp://b" })).toBe("compare");
    expect(runCasesProblem({ ...ok, compare: "http://b.test" })).toBeNull();
    expect(runCasesProblem({ ...ok, keys: [] })).toBe("cases");
    expect(runCasesProblem({ url: "", compare: "x", keys: [] })).toBe("url");
  });
});

describe("movedPosition", () => {
  const cases = [{ key: "TC-1" }, { key: "TC-2" }, { key: "TC-3" }];

  it("targets the neighbour's slot, 1-based", () => {
    expect(movedPosition(cases, "TC-2", -1)).toBe(1);
    expect(movedPosition(cases, "TC-2", 1)).toBe(3);
  });

  it("is null past either end or for an unknown key", () => {
    expect(movedPosition(cases, "TC-1", -1)).toBeNull();
    expect(movedPosition(cases, "TC-3", 1)).toBeNull();
    expect(movedPosition(cases, "TC-9", 1)).toBeNull();
  });
});

describe("after the import", () => {
  const body = "Intro\n\n## Test Cases\n\n- [ ] TC-1: login\n";
  it("offers to remove the section once the cases are rows", () => {
    expect(offersRemoveSection({ cases: [tc()], body })).toBe(true);
    expect(offersRemoveSection({ cases: [], body })).toBe(false);
    expect(offersRemoveSection({ cases: [tc()], body: "Intro" })).toBe(false);
    const html = testCasesBlockHTML({ cases: [tc()], attachments: [], comments: [], body }, deps);
    expect(html).toContain('data-tc-act="remove-section"');
    const box = dom(html);
    const note = box.querySelector('.callout-warn[role="status"]');
    expect(note?.querySelector('[data-tc-act="remove-section"]')).not.toBeNull();
  });
});

describe("statuses set by hand", () => {
  it("reads a status that differs from the latest run, or one with no run", () => {
    expect(setByHand(tc({ status: "pass", runs: [run({ verdict: "pass" })] }))).toBe(false);
    expect(setByHand(tc({ status: "pass", runs: [run({ verdict: "fail" })] }))).toBe(true);
    expect(setByHand(tc({ status: "pending" }))).toBe(false);
    expect(setByHand(tc({ status: "pass" }))).toBe(true);
  });

  it("marks the row", () => {
    const html = testCasesBlockHTML(
      { cases: [tc({ status: "fail" })], attachments: [], comments: [], body: "" },
      deps,
    );
    expect(html).toContain("tc-by-hand");
  });
});

describe("buttons", () => {
  it("draws the block's actions and the form's foot with the button component", () => {
    const input = { cases: [tc()], attachments: [], comments: [], body: "## Test cases\n" };
    const html = testCasesBlockHTML(input, deps);
    expect(html).toMatch(/<button class="btn btn-sm" type="button" data-tc-act="add">/);
    expect(html).not.toContain("btn-flat");
    const form = testCaseFormHTML();
    expect(form).toContain(
      '<button class="btn btn-ghost btn-sm" type="button" data-tc-act="cancel">',
    );
    expect(form).toContain('<button class="btn btn-primary btn-sm" type="submit">');
  });

  it("names every icon button of a case and turns off the moves that can't happen", () => {
    const box = document.createElement("div");
    box.innerHTML = testCasesBlockHTML(
      { cases: [tc()], attachments: [], comments: [], body: "" },
      deps,
    );
    const del = box.querySelector<HTMLButtonElement>('[data-tc-act="delete"]');
    expect(del?.className).toBe("icon-btn icon-btn-danger");
    expect(del?.getAttribute("aria-label")).toContain("TC-1");
    expect(box.querySelector<HTMLButtonElement>('[data-tc-act="up"]')?.disabled).toBe(true);
    expect(box.querySelector('[data-tc-act="edit"] svg')?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("run cases", () => {
  const agents = [
    { type: "qa", name: "QA", stage: "mention" as const, missing: [] },
    { type: "dev", name: "Dev", stage: "mention" as const, missing: ["thulu-browser"] },
  ];

  it("turns the button off with the reason", () => {
    const html = testCasesBlockHTML(
      { cases: [tc()], attachments: [], comments: [], body: "" },
      { ...deps, runCases: { block: "no_browser", reason: "nobody has the browser" } },
    );
    const box = document.createElement("div");
    box.innerHTML = html;
    const btn = box.querySelector<HTMLButtonElement>('[data-tc-act="run"]');
    expect(btn?.disabled).toBe(true);
    expect(btn?.title).toBe("nobody has the browser");
  });

  it("says why it is off in the block, not only in the tooltip", () => {
    const input = { cases: [tc()], attachments: [], comments: [], body: "" };
    const off = testCasesBlockHTML(input, {
      ...deps,
      runCases: { block: "no_browser", reason: "nobody has the browser" },
    });
    const note = dom(off).querySelector<HTMLElement>('.callout-warn[role="status"]');
    expect(note?.querySelector(".callout-text")?.textContent).toBe("nobody has the browser");
    expect(note?.querySelector<HTMLAnchorElement>('a.btn[href="/agents/"]')).not.toBeNull();
    const on = testCasesBlockHTML(input, { ...deps, runCases: { block: "", reason: "" } });
    expect(on).not.toContain("tc-run-off");
  });

  it("names what the picked agent lacks and keeps Launch off", () => {
    const ok = dom(runCasesFormHTML([tc()], agents, "qa"));
    expect(q<HTMLElement>(ok, "#tc-run-agent-error").hidden).toBe(true);
    expect(q<HTMLElement>(ok, "#tc-run-agent").hasAttribute("aria-invalid")).toBe(false);
    const off = dom(runCasesFormHTML([tc()], agents, "dev"));
    const line = q<HTMLElement>(off, "#tc-run-agent-error");
    expect(line.hidden).toBe(false);
    expect(line.textContent).toContain("thulu-browser");
    const select = q<HTMLElement>(off, "#tc-run-agent");
    expect(select.getAttribute("aria-invalid")).toBe("true");
    expect(select.getAttribute("aria-describedby")).toBe("tc-run-agent-error");
    expect(q<HTMLButtonElement>(off, '[type="submit"]').disabled).toBe(true);
    expect(q<HTMLInputElement>(off, 'input[name="case"][value="TC-1"]').checked).toBe(true);
  });

  it("lists the cases as checks in a group named by its label", () => {
    const box = dom(runCasesFormHTML([tc(), tc({ key: "TC-2", title: "<b>" })], agents, "qa"));
    const group = q<HTMLElement>(box, '[role="group"].check-list');
    expect(group.getAttribute("aria-labelledby")).toBe("tc-run-cases-label");
    expect(q<HTMLElement>(box, "#tc-run-cases-label").textContent).toBe("Cases");
    expect(group.querySelectorAll("label.check").length).toBe(2);
    expect(group.textContent).toContain("<b>");
    expect(q<HTMLLabelElement>(box, 'label[for="tc-run-url"]')).toBeTruthy();
  });

  it("marks the address to compare with as optional once", () => {
    const box = dom(runCasesFormHTML([tc()], agents, "qa"));
    const label = q<HTMLLabelElement>(box, 'label[for="tc-run-compare"]');
    expect(label.querySelectorAll(".field-optional").length).toBe(1);
    expect(label.textContent.match(/\(optional\)/g)?.length).toBe(1);
    expect(label.textContent).toBe("Compare with (optional)");
  });
});

describe("reports", () => {
  it("hides the run's own video and captures in the report under it", () => {
    const body =
      "ctx\n\n![v.mp4](/task-attachments/v1)\n\n![s.png](/task-attachments/s1)\n\n![b.png](/task-attachments/b1)";
    expect(withoutEmbeds(body, ["v1"])).not.toContain("v1");
    expect(withoutEmbeds(body, ["v1"])).toContain("s1");
    expect(withoutEmbeds(body, [""])).toBe(body);
    const both = withoutEmbeds(body, ["v1", "s1"]);
    expect(both).not.toContain("s1");
    expect(both).toContain("b1");
  });

  it("lists reports newest first", () => {
    const r = (id: string, at: string) => ({ id, created_at: at });
    expect(
      reportsNewestFirst([r("a", "2026-10-01"), r("b", "2026-10-03"), r("c", "2026-10-02")]).map(
        (x) => x.id,
      ),
    ).toEqual(["b", "c", "a"]);
  });

  it("groups the files each report published", () => {
    const files = [{ id: "v1" }, { id: "s1" }, { id: "x" }, { id: "s2" }];
    const reports = [
      {
        id: "r1",
        title: "old",
        body: "![v](/task-attachments/v1) ![s](/task-attachments/s1)",
        created_at: "2026-10-01",
      },
      {
        id: "r2",
        title: "new",
        body: "[s](/task-attachments/s1) [s2](/task-attachments/s2)",
        created_at: "2026-10-02",
      },
    ];
    const { groups, loose } = groupFilesByReport(files, reports);
    expect(groups.map((g) => [g.report.id, g.files.map((f) => f.id)])).toEqual([
      ["r2", ["s1", "s2"]],
      ["r1", ["v1"]],
    ]);
    expect(loose.map((f) => f.id)).toEqual(["x"]);
  });
});
