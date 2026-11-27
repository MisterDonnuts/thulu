// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import type { Visit } from "./rounds";
import type { TestCase, TestCaseRun } from "./test-cases";
import {
  accessFormHTML,
  accessProblem,
  barSegments,
  caseListHTML,
  caseRounds,
  changedWithin,
  criteriaOf,
  criterionState,
  groupCases,
  handDone,
  handRunnerHTML,
  handTally,
  needGroups,
  needIcon,
  needsBlockHTML,
  NO_ROUND_COLUMNS_MAX,
  OBSERVED_MAX,
  personRunFields,
  roundColumns,
  runRound,
  stepsOf,
  testingRounds,
  testsSummaryHTML,
  testsTabCount,
  testsViewHTML,
  verdictNeedsNote,
  visibleRounds,
  type HandState,
  type NeedKindEntry,
  type TestsViewInput,
  type TestsViewState,
} from "./tests-view";

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
    ran_at: "2026-10-01T10:00:00Z",
    ...over,
  };
}

// The daemon's catalog, as GET /test-cases/need-kinds serves it.
const CATALOG: NeedKindEntry[] = [
  {
    kind: "device",
    icon: "smartphone",
    order: 10,
    actions: ["by_hand", "skip"],
    secrets: false,
    title: "Physical device",
  },
  {
    kind: "third_party_account",
    icon: "globe",
    order: 20,
    actions: ["access", "by_hand", "skip"],
    secrets: true,
    title: "Third-party account",
  },
  {
    kind: "environment",
    icon: "server",
    order: 30,
    actions: ["access", "rerun", "skip"],
    secrets: true,
    title: "Environment",
  },
  {
    kind: "account",
    icon: "key",
    order: 40,
    actions: ["access", "by_hand", "skip"],
    secrets: true,
    title: "Account in the app",
  },
  {
    kind: "judgment",
    icon: "eye",
    order: 50,
    actions: ["review_video", "skip"],
    secrets: false,
    title: "A person's judgment",
  },
  {
    kind: "other",
    icon: "help",
    order: 90,
    actions: ["by_hand", "skip"],
    secrets: false,
    title: "Something else",
  },
];

function visit(stage: string, round: number, at: string): Visit {
  return { stage, round, at, until: "", from: "", via: "", byPerson: false, verdicts: [] };
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

function viewInput(over: Partial<TestsViewInput> = {}): TestsViewInput {
  return {
    cases: [],
    criteria: [],
    catalog: CATALOG,
    visits: [],
    attachments: [],
    environments: [],
    stopThread: "",
    md: (s) => `<p>${s}</p>`,
    attachmentURL: (id) => `http://d/a/${id}`,
    authorName: (a) => a || "someone",
    manageHTML: (c) => `<div class="manage" data-manage="${c.key}"></div>`,
    runHTML: (r) => `<article class="run" data-run="${r.id}"></article>`,
    capturesHTML: () => "",
    ...over,
  };
}

function viewState(over: Partial<TestsViewState> = {}): TestsViewState {
  return {
    open: new Set(),
    criterion: "",
    criteriaOpen: false,
    form: "",
    roundCols: 4,
    readOnly: false,
    hand: null,
    stageLabel: "Testing",
    canSendBack: true,
    toolsHTML: "",
    notesHTML: "",
    ...over,
  };
}

describe("round columns by width", () => {
  it("shows none on a phone, 4 in the drawer and 6 wide", () => {
    expect(roundColumns(390)).toBe(0);
    expect(roundColumns(640)).toBe(0);
    expect(roundColumns(NO_ROUND_COLUMNS_MAX)).toBe(0);
    expect(roundColumns(NO_ROUND_COLUMNS_MAX + 1)).toBe(4);
    expect(roundColumns(920)).toBe(4);
    expect(roundColumns(1099)).toBe(4);
    expect(roundColumns(1100)).toBe(6);
    expect(roundColumns(1600)).toBe(6);
  });

  it("gives a task whose cases never ran a list without round columns", () => {
    const cases = [tc({ key: "TC-1" }), tc({ id: "c2", key: "TC-2", status: "pass" })];
    const box = dom(caseListHTML(viewInput({ cases }), viewState({ roundCols: 4 })));
    const list = box.querySelector<HTMLElement>(".tv-list");
    expect(list?.dataset.rounds).toBe("0");
    expect(box.querySelectorAll(".tv-rc")).toHaveLength(0);
    expect(box.querySelectorAll(".tv-row")).toHaveLength(2);
    expect(box.querySelector(".tv-legend")).toBeNull();
  });

  it("counts the rounds the legend shows, not the columns that would fit", () => {
    const runs = [1, 2].map((n) =>
      run({ id: `r${n}`, round: n, ran_at: `2026-10-0${n}T10:00:00Z` }),
    );
    const box = dom(
      caseListHTML(
        viewInput({ cases: [tc({ status: "pass", runs })] }),
        viewState({ roundCols: 6 }),
      ),
    );
    expect(box.querySelector(".tv-legend")?.textContent).toContain(
      t("lib.tests_view.legend.columns", { count: 2 }),
    );
  });

  it("keeps the newest rounds and counts the ones left out", () => {
    const all = [1, 2, 3, 4, 5, 6, 7];
    expect(visibleRounds(all, 4)).toEqual({ shown: [4, 5, 6, 7], hidden: 3 });
    expect(visibleRounds(all, 6)).toEqual({ shown: [2, 3, 4, 5, 6, 7], hidden: 1 });
    expect(visibleRounds(all, 0)).toEqual({ shown: [], hidden: 7 });
    expect(visibleRounds([2, 3], 6)).toEqual({ shown: [2, 3], hidden: 0 });
  });

  it("reads a run's round from the daemon, else from the stage moves", () => {
    const visits = [
      visit("dev", 1, "2026-10-01T00:00:00Z"),
      visit("dev", 2, "2026-10-02T00:00:00Z"),
    ];
    expect(runRound(run({ round: 5 }), visits)).toBe(5);
    expect(runRound(run({ ran_at: "2026-10-01T12:00:00Z" }), visits)).toBe(1);
    expect(runRound(run({ ran_at: "2026-10-03T00:00:00Z" }), visits)).toBe(2);
  });

  it("marks a round whose result changed within it", () => {
    const c = tc({
      runs: [
        run({ id: "c", round: 2, verdict: "pass", ran_at: "2026-10-02T12:00:00Z" }),
        run({ id: "b", round: 2, verdict: "fail", ran_at: "2026-10-02T10:00:00Z" }),
        run({ id: "a", round: 1, verdict: "fail", ran_at: "2026-10-01T10:00:00Z" }),
      ],
    });
    const cells = caseRounds(c, []);
    expect(cells.get(2)).toEqual({ last: "pass", all: ["fail", "pass"] });
    expect(changedWithin(cells.get(2) ?? { last: "pass", all: [] })).toBe(true);
    expect(changedWithin(cells.get(1) ?? { last: "pass", all: ["pass", "fail"] })).toBe(false);
    expect(testingRounds([c, tc({ runs: [run({ round: 4 })] })], [])).toEqual([1, 2, 4]);
  });

  it("draws one column per visible round with +N for the rest, and none on a phone", () => {
    const runs = [1, 2, 3, 4, 5, 6].map((n) =>
      run({ id: `r${n}`, round: n, ran_at: `2026-10-0${n}T10:00:00Z` }),
    );
    const cases = [tc({ status: "pass", runs })];
    const drawer = dom(caseListHTML(viewInput({ cases }), viewState({ roundCols: 4 })));
    expect(drawer.querySelectorAll(".tv-thead .tv-rc")).toHaveLength(4);
    expect(drawer.querySelector(".tv-more-r")?.textContent).toBe(
      t("lib.tests_view.list.more", { count: 2 }),
    );
    expect(drawer.querySelectorAll(".tv-row .tv-cell")).toHaveLength(4);
    expect(drawer.querySelector(".tv-legend")?.textContent).toContain(
      t("lib.tests_view.legend.columns", { count: 4 }),
    );
    const phone = dom(caseListHTML(viewInput({ cases }), viewState({ roundCols: 0 })));
    expect(phone.querySelectorAll(".tv-rc")).toHaveLength(0);
    expect(phone.querySelector(".tv-legend")).toBeNull();
  });
});

describe("grouping by need", () => {
  const blocked = (key: string, r: Partial<TestCaseRun>) =>
    tc({ key, status: "blocked", runs: [run({ verdict: "blocked", ...r })] });

  it("puts each blocked case under its hardest need, in catalog order", () => {
    const groups = needGroups(
      [
        blocked("TC-1", { needs: [{ kind: "account", label: "Admin login" }] }),
        blocked("TC-2", {
          needs: [
            { kind: "account", label: "Admin login" },
            { kind: "device", label: "An iPhone" },
          ],
        }),
        blocked("TC-3", { needs: [{ kind: "environment", label: "Staging URL" }] }),
        tc({ key: "TC-4", status: "pass" }),
      ],
      CATALOG,
    );
    expect(groups.map((g) => g.kind.kind)).toEqual(["device", "environment", "account"]);
    expect(groups[0].cases.map((c) => c.key)).toEqual(["TC-2"]);
    expect(groups[2].cases.map((c) => c.key)).toEqual(["TC-1"]);
    expect(groups[2].labels).toEqual(["Admin login"]);
  });

  it("files a case from an older agent, with no needs, under other", () => {
    const groups = needGroups([blocked("TC-1", {})], CATALOG);
    expect(groups.map((g) => g.kind.kind)).toEqual(["other"]);
    expect(groups[0].kind.actions).toEqual(["by_hand", "skip"]);
  });

  it("keeps a kind the catalog does not know, last, with by hand and skip", () => {
    const groups = needGroups(
      [blocked("TC-1", { needs: [{ kind: "satellite", label: "A dish" }] })],
      CATALOG,
    );
    expect(groups[0].kind).toMatchObject({ kind: "satellite", actions: ["by_hand", "skip"] });
  });

  it("collects the secrets once and counts what the agent tried and what writes data", () => {
    const secrets = [{ name: "ADMIN_PASSWORD", label: "Admin password" }];
    const groups = needGroups(
      [
        blocked("TC-1", {
          needs: [{ kind: "account", label: "Admin", secrets }],
          tried: "simulated",
          tried_result: "the form accepts a fake user",
          writes_data: true,
        }),
        blocked("TC-2", { needs: [{ kind: "account", label: "Admin", secrets }], tried: "none" }),
      ],
      CATALOG,
    );
    expect(groups[0]).toMatchObject({
      secrets,
      tried: 1,
      triedResult: "the form accepts a fake user",
      writesData: 1,
    });
  });

  it("reads the newest blocked run, not an older one", () => {
    const c = tc({
      key: "TC-1",
      status: "blocked",
      runs: [
        run({
          id: "new",
          verdict: "blocked",
          ran_at: "2026-10-02T00:00:00Z",
          needs: [{ kind: "device", label: "Phone" }],
        }),
        run({
          id: "old",
          verdict: "blocked",
          ran_at: "2026-10-01T00:00:00Z",
          needs: [{ kind: "account", label: "Login" }],
        }),
      ],
    });
    expect(needGroups([c], CATALOG)[0].kind.kind).toBe("device");
  });

  it("offers the catalog's actions on each row", () => {
    const cases = [
      blocked("TC-1", { needs: [{ kind: "environment", label: "Staging" }] }),
      blocked("TC-2", { needs: [{ kind: "judgment", label: "Copy" }] }),
    ];
    const box = dom(needsBlockHTML(viewInput({ cases }), viewState()));
    const env = q(box, '[data-kind="environment"]');
    expect(env.querySelector('[data-tv-form="access:environment"]')).not.toBeNull();
    expect(env.querySelector('[data-tv-action="rerun"]')).not.toBeNull();
    expect(env.querySelector("[data-tv-hand]")).toBeNull();
    const judgment = q(box, '[data-kind="judgment"]');
    expect(judgment.querySelector('.tv-need-acts [data-tv-open="TC-2"]')?.textContent).toContain(
      t("lib.tests_view.needs.review_video"),
    );
    expect(box.querySelector('[data-tv-action="send_back"]')).not.toBeNull();
  });

  it("gives a reader the needs with no action", () => {
    const cases = [blocked("TC-1", { needs: [{ kind: "account", label: "Admin" }] })];
    const box = dom(
      needsBlockHTML(viewInput({ cases, stopThread: "c9" }), viewState({ readOnly: true })),
    );
    expect(box.querySelector("[data-tv-form]")).toBeNull();
    expect(box.querySelector('[data-tv-stop="c9"]')).not.toBeNull();
  });

  it("maps catalog icons the sprite lacks", () => {
    expect(needIcon("smartphone")).toBe("monitor");
    expect(needIcon("key")).toBe("lock");
    expect(needIcon("server")).toBe("server");
    expect(needIcon("rocket")).toBe("help");
  });

  it("counts the tab by what waits for a person", () => {
    expect(testsTabCount([tc({ status: "blocked" }), tc({ status: "pass" })])).toEqual({
      n: 1,
      warn: true,
    });
    expect(testsTabCount([tc({ status: "pass" }), tc({ status: "fail" })])).toEqual({
      n: 2,
      warn: false,
    });
  });
});

describe("the access form", () => {
  const g = needGroups(
    [
      tc({
        key: "TC-1",
        status: "blocked",
        runs: [
          run({
            verdict: "blocked",
            needs: [
              {
                kind: "account",
                label: "Admin",
                secrets: [
                  { name: "ADMIN_PASSWORD", label: "Admin password" },
                  { name: "ADMIN_USER", label: "Admin user" },
                ],
              },
            ],
          }),
        ],
      }),
    ],
    CATALOG,
  )[0];

  it("asks for each secret, hiding the ones that look like passwords", () => {
    const box = dom(
      accessFormHTML(g, { environments: [{ id: "e1", label: "Staging", allowed: true }] }),
    );
    expect(q<HTMLInputElement>(box, '[data-tv-secret="ADMIN_PASSWORD"]').type).toBe("password");
    expect(q<HTMLInputElement>(box, '[data-tv-secret="ADMIN_USER"]').type).toBe("text");
    expect(box.querySelector("[data-tv-env] option")?.getAttribute("value")).toBe("e1");
  });

  it("says so and does not submit when there is no environment to keep secrets in", () => {
    const box = dom(
      accessFormHTML(g, { environments: [{ id: "e1", label: "Prod", allowed: false }] }),
    );
    expect(box.querySelector("[data-tv-env]")).toBeNull();
    expect(q<HTMLButtonElement>(box, '[type="submit"]').disabled).toBe(true);
  });

  it("waits for the environments instead of saying there is none", () => {
    const box = dom(accessFormHTML(g, { environments: [], environmentsLoading: true }));
    expect(box.querySelector('[role="status"]')?.textContent).toBe(
      t("lib.tests_view.access.environments_loading"),
    );
    expect(box.textContent).not.toContain(t("lib.tests_view.access.no_environment"));
    expect(q<HTMLButtonElement>(box, '[type="submit"]').disabled).toBe(true);
  });

  it("names the first missing secret, then the environment", () => {
    const names = ["A", "B"];
    expect(accessProblem(names, { secrets: { A: "x" }, environment: "e" })).toEqual({
      problem: "secret",
      name: "B",
    });
    expect(accessProblem(names, { secrets: { A: "x", B: "y" }, environment: "" })).toEqual({
      problem: "environment",
    });
    expect(accessProblem([], { secrets: {}, environment: "" })).toEqual({ problem: null });
  });
});

describe("criteria", () => {
  const cases = [
    tc({ key: "TC-1", covers: ["AC-1"], status: "pass" }),
    tc({ key: "TC-2", covers: ["AC-1", "AC-2"], status: "pass" }),
    tc({ key: "TC-3", covers: ["AC-2"], status: "waived" }),
    tc({ key: "TC-4", covers: ["AC-3"], status: "fail" }),
    tc({ key: "TC-5", covers: ["AC-4"], status: "blocked" }),
    tc({ key: "TC-6", covers: ["AC-5"], status: "pass" }),
    tc({ key: "TC-7", covers: ["AC-5"], status: "pending" }),
    tc({ key: "TC-8", covers: ["AC-6"], status: "pending" }),
  ];

  it("reads each criterion's state from the cases that cover it", () => {
    expect(criterionState("AC-1", cases)).toBe("ok");
    expect(criterionState("AC-2", cases)).toBe("waived");
    expect(criterionState("AC-3", cases)).toBe("fail");
    expect(criterionState("AC-4", cases)).toBe("wait");
    expect(criterionState("AC-5", cases)).toBe("part");
    expect(criterionState("AC-6", cases)).toBe("untested");
    expect(criterionState("AC-9", cases)).toBe("none");
  });

  it("takes the daemon's criteria, else the brief's AC-n lines", () => {
    expect(criteriaOf({ criteria: [{ key: "AC-1", text: "Saved" }], body: "" })).toEqual([
      { key: "AC-1", text: "Saved" },
    ]);
    const body =
      "Intro\n\n## Acceptance criteria\n\n- **AC-1** The form saves\n- [ ] AC-2: It shows an error\n\n## Notes\nAC-3 not a criterion";
    expect(criteriaOf({ criteria: null, body })).toEqual([
      { key: "AC-1", text: "The form saves" },
      { key: "AC-2", text: "It shows an error" },
    ]);
    expect(criteriaOf({ body: "no criteria" })).toEqual([]);
  });

  it("draws one square per criterion that filters the list, and the counts", () => {
    const criteria = [1, 2, 3].map((n) => ({ key: `AC-${n}`, text: `Criterion ${n}` }));
    const box = dom(
      testsSummaryHTML(viewInput({ cases, criteria }), viewState({ criterion: "AC-2" })),
    );
    expect(box.textContent).toContain(t("lib.tests_view.summary.pass", { pass: 3, total: 8 }));
    expect(box.textContent).toContain(t("lib.tests_view.summary.proven", { proven: 1, total: 3 }));
    const squares = box.querySelectorAll<HTMLButtonElement>("button.tv-ac");
    expect(squares).toHaveLength(3);
    expect(squares[1].getAttribute("aria-pressed")).toBe("true");
    expect(squares[2].className).toContain("tv-ac-fail");
  });

  it("lists only the cases of the picked criterion", () => {
    const criteria = [{ key: "AC-2", text: "Second" }];
    const box = dom(caseListHTML(viewInput({ cases, criteria }), viewState({ criterion: "AC-2" })));
    expect([...box.querySelectorAll<HTMLElement>(".tv-case")].map((e) => e.dataset.key)).toEqual([
      "TC-2",
      "TC-3",
    ]);
    expect(box.querySelector('.tv-filter [data-tv-ac="AC-2"]')).not.toBeNull();
    expect(box.querySelector(".tv-ac-text")?.textContent).toBe("AC-2 Second");
  });

  it("sizes the bar's segments by the share of each status", () => {
    const segs = barSegments([
      tc({ status: "pass" }),
      tc({ status: "pass" }),
      tc({ status: "blocked" }),
      tc({ status: "pending" }),
    ]);
    expect(segs).toEqual([
      { group: "pass", pct: 50 },
      { group: "blocked", pct: 25 },
    ]);
    expect(barSegments([])).toEqual([]);
  });

  it("keys the squares by proven, partly and waiting", () => {
    const criteria = [{ key: "AC-1", text: "One" }];
    const box = dom(testsSummaryHTML(viewInput({ cases, criteria }), viewState()));
    expect(box.querySelector(".tv-acs-key")?.textContent).toBe(
      [
        t("lib.tests_view.criteria.key.ok"),
        t("lib.tests_view.criteria.key.part"),
        t("lib.tests_view.criteria.key.wait"),
      ].join(""),
    );
  });

  it("names who wrote the criteria beside Read the criteria, when it is known", () => {
    const criteria = [{ key: "AC-1", text: "One" }];
    const summary = (by?: string) =>
      dom(testsSummaryHTML(viewInput({ cases, criteria, criteriaBy: by }), viewState()))
        .querySelector(".tv-ac-list summary")
        ?.textContent.trim();
    expect(summary("thulu-refiner")).toBe(
      `${t("lib.tests_view.criteria.read")} ${t("lib.tests_view.criteria.written_by", { who: "thulu-refiner" })}`,
    );
    expect(summary()).toBe(t("lib.tests_view.criteria.read"));
  });
});

describe("the case list", () => {
  it("groups the cases in the order a person reads them", () => {
    const cases = ["pass", "pending", "fail", "blocked", "waived", "queued", "skipped"].map(
      (status, i) => tc({ key: `TC-${i}`, status: status as TestCase["status"] }),
    );
    expect(groupCases(cases).map((g) => [g.group, g.cases.length])).toEqual([
      ["blocked", 1],
      ["queued", 1],
      ["fail", 1],
      ["pass", 1],
      ["waived", 1],
      ["untested", 2],
    ]);
  });

  it("opens a case into how to test and what the agent saw, with the management bar", () => {
    const c = tc({
      key: "TC-1",
      status: "fail",
      preconditions: "Logged in as admin",
      steps: "1. Open settings\n2. Save",
      expected: "A toast says saved",
      runs: [run({ id: "r1", verdict: "fail", observed: "Nothing happened", note: "Long note" })],
    });
    const box = dom(
      caseListHTML(viewInput({ cases: [c] }), viewState({ open: new Set(["TC-1"]) })),
    );
    const detail = q(box, "#tv-detail-TC-1");
    expect(detail.querySelectorAll(".tv-steps li")).toHaveLength(2);
    expect(detail.querySelector(".tv-pre")?.textContent).toBe("Logged in as admin");
    expect(detail.querySelector(".tv-saw")?.textContent).toBe("Nothing happened");
    expect(detail.querySelector(".tv-full-note")).not.toBeNull();
    expect(detail.querySelector('[data-run="r1"]')).not.toBeNull();
    expect(detail.querySelector('[data-manage="TC-1"]')).not.toBeNull();
    expect(detail.querySelector('[data-tv-verdict="fail"]')).not.toBeNull();
  });

  it("shows who skipped a waived case and why, with no verdict buttons", () => {
    const c = tc({
      key: "TC-1",
      status: "waived",
      runs: [run({ verdict: "waived", author: "ana", note: "No sandbox for payments" })],
    });
    const box = dom(
      caseListHTML(viewInput({ cases: [c] }), viewState({ open: new Set(["TC-1"]) })),
    );
    expect(box.textContent).toContain(t("lib.tests_view.detail.skipped_by", { who: "ana" }));
    expect(box.textContent).toContain("No sandbox for payments");
    expect(box.querySelector("[data-tv-verdict]")).toBeNull();
  });

  it("plays the video of the last run with evidence, with its subtitles", () => {
    const c = tc({
      key: "TC-1",
      status: "pass",
      runs: [
        run({ id: "new", verdict: "pass", ran_at: "2026-10-03T00:00:00Z" }),
        run({
          id: "old",
          verdict: "fail",
          ran_at: "2026-10-02T00:00:00Z",
          video_attachment_id: "v1",
          vtt_attachment_id: "s1",
        }),
      ],
    });
    const attachments = [
      { id: "v1", filename: "run.webm", mime_type: "video/webm" },
      { id: "s1", filename: "run.vtt", mime_type: "text/vtt" },
    ];
    const box = dom(
      caseListHTML(viewInput({ cases: [c], attachments }), viewState({ open: new Set(["TC-1"]) })),
    );
    expect(box.querySelector("video.tv-video")?.getAttribute("src")).toBe("http://d/a/v1");
    expect(box.querySelector("video.tv-video track")?.getAttribute("src")).toBe("http://d/a/s1");
  });

  it("splits steps into lines without their numbers", () => {
    expect(stepsOf({ steps: "1. Open\n2) Click\n- Save\n\n" })).toEqual(["Open", "Click", "Save"]);
  });

  it("puts the summary, the needs and the list in one tab", () => {
    const cases = [tc({ key: "TC-1", status: "blocked", runs: [run({ verdict: "blocked" })] })];
    const box = dom(testsViewHTML(viewInput({ cases }), viewState()));
    expect(box.querySelector(".tv-sum")).not.toBeNull();
    expect(box.querySelector("#tv-needs")).not.toBeNull();
    expect(box.querySelector(".tv-cases")).not.toBeNull();
  });
});

describe("testing by hand", () => {
  const cases = [
    tc({
      key: "TC-1",
      status: "blocked",
      steps: "1. Plug the reader\n2. Tap the card",
      expected: "Paid",
    }),
    tc({ key: "TC-2", status: "blocked" }),
  ];
  const hand = (over: Partial<HandState> = {}): HandState => ({
    kind: "device",
    title: "Physical device",
    icon: "smartphone",
    keys: ["TC-1", "TC-2"],
    at: 0,
    ticks: {},
    results: {},
    ...over,
  });

  it("asks for a note to fail or skip, not to pass", () => {
    expect(verdictNeedsNote("pass", "")).toBe(false);
    expect(verdictNeedsNote("fail", " ")).toBe(true);
    expect(verdictNeedsNote("waived", "no reader")).toBe(false);
  });

  it("walks the cases with checkable steps", () => {
    const box = dom(handRunnerHTML(hand({ ticks: { "TC-1": [1] } }), viewInput({ cases })));
    const checks = box.querySelectorAll<HTMLButtonElement>("[data-tv-tick]");
    expect(checks).toHaveLength(2);
    expect(checks[1].getAttribute("aria-pressed")).toBe("true");
    expect(box.querySelector('[data-tv-verdict="pass"][data-mode="hand"]')).not.toBeNull();
    expect(q<HTMLButtonElement>(box, '[data-tv-hgo="-1"]').disabled).toBe(true);
  });

  it("offers to save once every case has a result", () => {
    const results = {
      "TC-1": { v: "pass" as const, note: "" },
      "TC-2": { v: "fail" as const, note: "Declined" },
    };
    expect(handDone({ keys: ["TC-1", "TC-2"], results })).toBe(true);
    expect(handDone({ keys: ["TC-1", "TC-2"], results: { "TC-1": results["TC-1"] } })).toBe(false);
    expect(handTally({ keys: ["TC-1", "TC-2"], results })).toEqual({ pass: 1, fail: 1, waived: 0 });
    const box = dom(handRunnerHTML(hand({ results }), viewInput({ cases })));
    expect(box.querySelector("[data-tv-hand-save]")).not.toBeNull();
  });

  it("reviews a case with its result and note kept, and goes back to the summary", () => {
    const results = {
      "TC-1": { v: "pass" as const, note: "" },
      "TC-2": { v: "fail" as const, note: "Declined at the reader" },
    };
    const box = dom(
      handRunnerHTML(hand({ results, at: 1, reviewing: true }), viewInput({ cases })),
    );
    expect(box.querySelector("[data-tv-hand-save]")).toBeNull();
    expect(q<HTMLInputElement>(box, '[data-tv-note][data-mode="hand"]').value).toBe(
      "Declined at the reader",
    );
    expect(q(box, '[data-tv-verdict="fail"]').getAttribute("aria-pressed")).toBe("true");
    expect(q(box, '[data-tv-verdict="pass"]').getAttribute("aria-pressed")).toBe("false");
    expect(box.querySelector("[data-tv-hand-summary]")).not.toBeNull();
    // A case without a result yet offers no way back to a summary that does not exist.
    const fresh = dom(handRunnerHTML(hand(), viewInput({ cases })));
    expect(fresh.querySelector("[data-tv-hand-summary]")).toBeNull();
    expect(q(fresh, '[data-tv-verdict="pass"]').getAttribute("aria-pressed")).toBe("false");
  });
});

describe("a person's note as run fields", () => {
  it("keeps a short note as observed", () => {
    expect(personRunFields("  Paid with the reader. ")).toEqual({
      observed: "Paid with the reader.",
    });
  });

  it("sends a long note whole in note with its first sentence as observed", () => {
    const long =
      `The reader declined the card. ${"Then it showed a long error. ".repeat(20)}`.trim();
    const out = personRunFields(long);
    expect(out).toEqual({ observed: "The reader declined the card.", note: long });
  });

  it("cuts a long first sentence to the limit", () => {
    const long = "x".repeat(OBSERVED_MAX + 50);
    const out = personRunFields(long);
    expect([...out.observed]).toHaveLength(OBSERVED_MAX);
    expect(out.observed.endsWith("…")).toBe(true);
    expect(out.note).toBe(long);
  });
});
