// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import type { TestCase, TestCaseRun } from "./test-cases";
import {
  caseFormProblem,
  groupFilesByReport,
  hasTestCasesSection,
  offersRemoveSection,
  reportsNewestFirst,
  runCasesFormHTML,
  runCasesProblem,
  withoutEmbeds,
  movedPosition,
  plainReportTitle,
  runHTML,
  testCaseFormHTML,
  testCasesToolsHTML,
  caseBarHTML,
  readOnlyBarHTML,
  testCasesSignature,
  type TestCasesInput,
} from "./test-cases-drawer";

const deps = {
  md: (s: string) => `<p>${s}</p>`,
  attachmentURL: (id: string) => `http://d/task-attachments/${id}`,
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

describe("testCasesToolsHTML", () => {
  it("offers the import only when the body has the section and there are no rows", () => {
    const body = "## Test Cases\n- [ ] a";
    expect(testCasesToolsHTML(input({ body }), {}).actions).toContain('data-tc-act="import"');
    expect(testCasesToolsHTML(input({ body, cases: [tc()] }), {}).actions).not.toContain(
      'data-tc-act="import"',
    );
    expect(testCasesToolsHTML(input(), {}).actions).not.toContain('data-tc-act="import"');
  });

  it("always offers to add a case, with the button component", () => {
    const { actions } = testCasesToolsHTML(input(), {});
    expect(actions).toMatch(/<button class="btn btn-sm" type="button" data-tc-act="add">/);
  });

  it("gives a reader no tool", () => {
    const tools = testCasesToolsHTML(input({ body: "## Test Cases\n- [ ] a", cases: [tc()] }), {
      ...deps,
      readOnly: true,
      runCases: { block: "", reason: "" },
    });
    expect(tools).toEqual({ actions: "", notes: "" });
  });
});

describe("the management bar of a case", () => {
  it("draws status, covered and the moves, with the moves that can't happen off", () => {
    const box = dom(caseBarHTML(tc({ status: "fail", covered: true }), 0, 2));
    const sel = q<HTMLSelectElement>(box, 'select[data-tc-act="status"]');
    expect(sel.value).toBe("fail");
    expect(sel.className).toBe("badge badge-danger badge-pill tc-status-select");
    const cov = q<HTMLInputElement>(box, 'label.check > input[data-tc-act="covered"]');
    expect(cov.checked).toBe(true);
    expect(cov.getAttribute("aria-label")).toContain("TC-1");
    expect(q<HTMLButtonElement>(box, '[data-tc-act="up"]').disabled).toBe(true);
    expect(q<HTMLButtonElement>(box, '[data-tc-act="down"]').disabled).toBe(false);
    const del = q<HTMLButtonElement>(box, '[data-tc-act="delete"]');
    expect(del.className).toBe("icon-btn icon-btn-danger");
    expect(del.getAttribute("aria-label")).toContain("TC-1");
    expect(q(box, '[data-tc-act="edit"] svg').getAttribute("aria-hidden")).toBe("true");
  });

  it("does not offer waived in the status picker of a case that is not waived", () => {
    const box = dom(caseBarHTML(tc({ status: "pass" }), 0, 1));
    const values = Array.from(q<HTMLSelectElement>(box, "select").options, (o) => o.value);
    expect(values.length).toBeGreaterThan(0);
    expect(values).not.toContain("waived");
  });

  it("gives a reader the state with no control that changes it", () => {
    const box = dom(readOnlyBarHTML(tc({ status: "fail", covered: true })));
    expect(box.querySelector("select, input, button")).toBeNull();
    expect(q(box, ".tc-bar").textContent).toContain("Tested by someone");
    expect(q(box, ".tc-bar").textContent).toContain(t("lib.test_cases.status.fail"));
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

  it("changes when the criteria a case covers or its preconditions change", () => {
    const sig = testCasesSignature(base);
    const c = base.cases[0];
    expect(testCasesSignature({ ...base, cases: [tc({ ...c, covers: ["AC-1"] })] })).not.toBe(sig);
    expect(
      testCasesSignature({ ...base, cases: [tc({ ...c, preconditions: "Logged in" })] }),
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
    const { notes } = testCasesToolsHTML(
      { cases: [tc()], attachments: [], comments: [], body },
      {},
    );
    expect(notes).toContain('data-tc-act="remove-section"');
    const box = dom(notes);
    const note = box.querySelector('.callout-warn[role="status"]');
    expect(note?.querySelector('[data-tc-act="remove-section"]')).not.toBeNull();
  });
});

describe("buttons", () => {
  it("draws the form's foot with the button component", () => {
    const form = testCaseFormHTML();
    expect(form).toContain(
      '<button class="btn btn-ghost btn-sm" type="button" data-tc-act="cancel">',
    );
    expect(form).toContain('<button class="btn btn-primary btn-sm" type="submit">');
  });
});

describe("run cases", () => {
  const agents = [
    { type: "qa", name: "QA", stage: "mention" as const, missing: [] },
    { type: "dev", name: "Dev", stage: "mention" as const, missing: ["thulu-browser"] },
  ];

  it("turns the button off with the reason", () => {
    const html = testCasesToolsHTML(
      { cases: [tc()], attachments: [], comments: [], body: "" },
      { ...deps, runCases: { block: "no_browser", reason: "nobody has the browser" } },
    ).actions;
    const box = document.createElement("div");
    box.innerHTML = html;
    const btn = box.querySelector<HTMLButtonElement>('[data-tc-act="run"]');
    expect(btn?.disabled).toBe(true);
    expect(btn?.title).toBe("nobody has the browser");
  });

  it("says why it is off in the block, not only in the tooltip", () => {
    const input = { cases: [tc()], attachments: [], comments: [], body: "" };
    const off = testCasesToolsHTML(input, {
      ...deps,
      runCases: { block: "no_browser", reason: "nobody has the browser" },
    }).notes;
    const note = dom(off).querySelector<HTMLElement>('.callout-warn[role="status"]');
    expect(note?.querySelector(".callout-text")?.textContent).toBe("nobody has the browser");
    expect(note?.querySelector<HTMLAnchorElement>('a.btn[href="/agents/"]')).not.toBeNull();
    const on = testCasesToolsHTML(input, { ...deps, runCases: { block: "", reason: "" } }).notes;
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
