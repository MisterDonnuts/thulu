// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import type { RunInfo, StreamEvent } from "./activity";
import type { ActivityCtx } from "./activity-html";
import {
  ROUND_STEP,
  buildRounds,
  costCardsHTML,
  filterRounds,
  pageRounds,
  roundWhyText,
  roundsHTML,
  runVerdict,
  shownToInclude,
  verdictText,
  visitEndText,
  visitForComment,
  type RoundsInput,
} from "./activity-rounds";
import type { KindedComment } from "./comments";
import { stageVisits, type VerdictMark } from "./rounds";

const ORDER = ["backlog", "dev", "testing", "done"];
const LABEL: Record<string, string> = {
  backlog: "Backlog",
  dev: "Dev",
  testing: "Testing",
  done: "Done",
};
const stageLabel = (id: string) => LABEL[id] ?? id;

function entered(
  at: string,
  from: string,
  to: string,
  extra: Record<string, unknown> = {},
): StreamEvent {
  return {
    id: `e-${at}`,
    kind: "stage_entered",
    created_at: at,
    payload: { from, to, by_person: "false", ...extra },
  };
}

function run(over: Partial<RunInfo>): RunInfo {
  return {
    id: "run",
    number: 1,
    agent: "dev-agent",
    stage: "dev",
    auditor: false,
    startedAt: "",
    finishedAt: "",
    state: "done",
    exit: 0,
    containerID: "",
    reason: "",
    ...over,
  };
}

function comment(over: Partial<KindedComment>): KindedComment {
  return { id: "c", created_at: "", body: "", source: "user", author: "ana", ...over };
}

// Dev, Testing, back to Dev (agent), Testing, a stop, a person sends it back with a reason, Dev.
const events: StreamEvent[] = [
  entered("2026-10-01T10:00:00Z", "backlog", "dev"),
  entered("2026-10-01T11:00:00Z", "dev", "testing"),
  entered("2026-10-01T12:00:00Z", "testing", "dev", { via: "kick_back" }),
  entered("2026-10-01T13:00:00Z", "dev", "testing"),
  entered("2026-10-01T15:00:00Z", "testing", "dev", { by_person: "true", comment_id: "dec1" }),
  {
    id: "rl",
    kind: "relaunch_requested",
    created_at: "2026-10-01T15:30:00Z",
    actor: "ana",
    payload: { stage: "dev", turns: 80, comment_id: "rl-why" },
  },
  {
    id: "ac",
    kind: "test_cases_access_given",
    created_at: "2026-10-01T14:10:00Z",
    actor: "ana",
    payload: { cases: ["TC-2"], secrets: ["PASS"], comment_id: "acc" },
  },
  {
    id: "pr",
    kind: "pr_opened",
    created_at: "2026-10-01T10:30:00Z",
    payload: { url: "https://github.com/o/r/pull/1" },
  },
];

const runs: RunInfo[] = [
  run({ id: "r1", agent: "dev-agent", stage: "dev", startedAt: "2026-10-01T10:05:00Z" }),
  run({ id: "r2", agent: "qa-agent", stage: "testing", startedAt: "2026-10-01T11:05:00Z" }),
  run({
    id: "r3",
    agent: "dev-agent",
    stage: "dev",
    startedAt: "2026-10-01T12:05:00Z",
    state: "failed",
    exit: 1,
  }),
  run({ id: "r4", agent: "qa-agent", stage: "testing", startedAt: "2026-10-01T13:05:00Z" }),
  run({
    id: "r5",
    agent: "dev-agent",
    stage: "dev",
    startedAt: "2026-10-01T15:05:00Z",
    state: "running",
  }),
];

const comments: KindedComment[] = [
  comment({
    id: "stop1",
    kind: "block",
    source: "agent",
    author: "qa-agent",
    created_at: "2026-10-01T14:00:00Z",
  }),
  comment({
    id: "stop1-r",
    parent_id: "stop1",
    kind: "decision",
    author: "ana",
    created_at: "2026-10-01T14:30:00Z",
    meta: { action: "access", cases: ["TC-2"] },
  }),
  comment({
    id: "dec1",
    kind: "decision",
    author: "ana",
    created_at: "2026-10-01T15:00:00Z",
    meta: { action: "send_back", to_stage: "dev" },
  }),
  comment({
    id: "q1",
    kind: "question",
    source: "agent",
    author: "dev-agent",
    created_at: "2026-10-01T10:20:00Z",
  }),
  comment({
    id: "talk",
    kind: "comment",
    author: "ana",
    body: "Looks close",
    created_at: "2026-10-01T13:20:00Z",
  }),
  comment({
    id: "note",
    source: "system",
    author: "",
    body: "Launched",
    created_at: "2026-10-01T13:01:00Z",
  }),
];

function input(over: Partial<RoundsInput> = {}): RoundsInput {
  return {
    visits: stageVisits(events, ORDER),
    runs,
    comments,
    events,
    running: true,
    waiting: false,
    ...over,
  };
}

const ctx: ActivityCtx = {
  now: Date.parse("2026-10-02T00:00:00Z"),
  stageLabel,
  eventLabel: (k) => k,
};

describe("rounds from the stage moves", () => {
  it("groups visits by round with how each one ended", () => {
    const rounds = buildRounds(input(), { system: false });
    expect(rounds.map((r) => [r.n, r.visits.map((v) => `${v.visit.stage}:${v.end.kind}`)])).toEqual(
      [
        [1, ["dev:passed", "testing:agent_back"]],
        [2, ["dev:passed", "testing:stopped_back"]],
        [3, ["dev:running"]],
      ],
    );
    const back = rounds[0].visits[1].end;
    expect(back).toMatchObject({ who: "qa-agent", to: "dev" });
    const person = rounds[1].visits[1].end;
    expect(person).toMatchObject({ who: "ana", commentID: "dec1" });
  });

  it("says who moved it and when it waits, from the structured fields", () => {
    const rounds = buildRounds(input({ running: false, waiting: true }), { system: false });
    const ends = rounds.flatMap((r) => r.visits.map((v) => visitEndText(v.end, { stageLabel })));
    expect(ends).toEqual([
      t("lib.activity_rounds.end.passed", { stage: "Testing" }),
      t("lib.activity_rounds.end.agent_back", { agent: "qa-agent", stage: "Dev" }),
      t("lib.activity_rounds.end.passed", { stage: "Testing" }),
      t("lib.activity_rounds.end.stopped_back", { who: "ana", stage: "Dev" }),
      t("lib.activity_rounds.end.waiting"),
    ]);
  });

  it("names a person it cannot identify, on an older task with no comment id", () => {
    const old = events.map((e) =>
      e.payload?.comment_id === "dec1"
        ? { ...e, payload: { ...e.payload, comment_id: undefined } }
        : e,
    );
    const rounds = buildRounds(input({ visits: stageVisits(old, ORDER), comments: [] }), {
      system: false,
    });
    const end = rounds[1].visits[1].end;
    expect(end.kind).toBe("person_back");
    expect(visitEndText(end, { stageLabel })).toBe(
      t("lib.activity_rounds.end.person_back", {
        who: t("lib.activity_rounds.a_person"),
        stage: "Dev",
      }),
    );
  });

  it("explains why each round started", () => {
    const rounds = buildRounds(input(), { system: false });
    expect(rounds.map((r) => roundWhyText(r, { stageLabel }))).toEqual([
      t("lib.activity_rounds.why.started", { stage: "Dev" }),
      t("lib.activity_rounds.why.agent_back", {
        agent: t("lib.activity_rounds.why.agent_who", { stage: "Testing", agent: "qa-agent" }),
        stage: "Dev",
      }),
      t("lib.activity_rounds.why.person_back", { who: "ana", stage: "Dev" }),
    ]);
  });

  it("puts runs, questions, stops, access and relaunches in their visit", () => {
    const rounds = buildRounds(input(), { system: false });
    const kinds = (r: number, v: number) => rounds[r].visits[v].items.map((x) => x.kind);
    expect(kinds(0, 0)).toEqual(["run", "question"]);
    expect(kinds(1, 1)).toEqual(["run", "comment", "stop", "access"]);
    expect(kinds(2, 0)).toEqual(["run", "relaunch"]);
    const stop = rounds[1].visits[1].items.find((x) => x.kind === "stop");
    expect(stop?.reply?.id).toBe("stop1-r");
    expect(rounds[1].visits[0].failed).toBe(1);
  });

  it("keeps a move's reason out of the decisions, and shows system events only when asked", () => {
    const plain = buildRounds(input(), { system: false });
    expect(
      plain.flatMap((r) => r.visits.flatMap((v) => v.items)).some((x) => x.comment?.id === "dec1"),
    ).toBe(false);
    const sys = buildRounds(input(), { system: true });
    const all = sys.flatMap((r) => r.visits.flatMap((v) => v.items));
    expect(all.filter((x) => x.kind === "system").map((x) => x.event?.id ?? x.comment?.id)).toEqual(
      ["pr", "note"],
    );
  });
});

describe("filters and pages", () => {
  const rounds = buildRounds(input(), { system: false });

  it("keeps the visits of a column or an agent, and drops rounds left empty", () => {
    expect(filterRounds(rounds, { stage: "testing", agent: "" }).map((r) => r.n)).toEqual([1, 2]);
    expect(
      filterRounds(rounds, { stage: "", agent: "dev-agent" }).flatMap((r) =>
        r.visits.map((v) => v.visit.stage),
      ),
    ).toEqual(["dev", "dev", "dev"]);
  });

  it("shows the newest rounds, three at a time", () => {
    const many = Array.from({ length: 7 }, (_, i) => ({ ...rounds[0], n: i + 1 }));
    expect(ROUND_STEP).toBe(3);
    const first = pageRounds(many, ROUND_STEP, true);
    expect(first.cards.map((r) => r.n)).toEqual([7, 6, 5]);
    expect(first.hidden).toBe(4);
    expect(pageRounds(many, 6, false).cards.map((r) => r.n)).toEqual([2, 3, 4, 5, 6, 7]);
  });

  it("offers earlier rounds and opens the visits the person opened", () => {
    const many = Array.from({ length: 5 }, (_, i) => ({ ...rounds[0], n: i + 1 }));
    const box = document.createElement("div");
    box.innerHTML = roundsHTML(
      many,
      {
        filter: { stage: "", agent: "" },
        newestFirst: true,
        system: false,
        shown: 3,
        stages: ORDER,
        agents: ["dev-agent"],
      },
      { ctx, stageLabel, open: new Set([rounds[0].visits[0].key]) },
    );
    expect(box.querySelectorAll(".ar-round-card")).toHaveLength(3);
    expect(box.querySelector("[data-ar-more]")?.textContent).toContain(
      t("lib.activity_rounds.more", { count: 2 }),
    );
    expect(box.querySelector<HTMLDetailsElement>("details.ar-cl")?.open).toBe(true);
  });
});

describe("see in Activity", () => {
  const visits = stageVisits(events, ORDER);

  it("finds the visit a person's reason ended, else the one a comment was written in", () => {
    // dec1 sent the task back from the second Testing visit (round 2, fourth visit).
    expect(visitForComment(visits, { id: "dec1", at: "2026-10-01T15:00:00Z" })).toEqual({
      key: "2:3",
      round: 2,
    });
    expect(visitForComment(visits, { id: "talk", at: "2026-10-01T13:20:00Z" })).toEqual({
      key: "2:3",
      round: 2,
    });
    expect(visitForComment(visits, { id: "q1", at: "2026-10-01T10:20:00Z" })).toEqual({
      key: "1:0",
      round: 1,
    });
    expect(visitForComment(visits, { id: "gone", at: "" })).toBeNull();
  });

  it("pages back far enough to show the round, in whole pages", () => {
    const many = Array.from({ length: 8 }, (_, i) => ({ round: i + 1 }));
    expect(shownToInclude(many, 8, ROUND_STEP)).toBe(3);
    expect(shownToInclude(many, 5, ROUND_STEP)).toBe(6);
    expect(shownToInclude(many, 1, ROUND_STEP)).toBe(9);
    expect(shownToInclude(many, 7, 6)).toBe(6);
    expect(shownToInclude(many, 42, ROUND_STEP)).toBe(ROUND_STEP);
  });
});

describe("badges a screen reader announces", () => {
  it("labels a visit's questions, stop and failed runs, not just their tooltip", () => {
    const rounds = buildRounds(input(), { system: false });
    const box = document.createElement("div");
    box.innerHTML = roundsHTML(
      rounds,
      {
        filter: { stage: "", agent: "" },
        newestFirst: false,
        system: false,
        shown: 3,
        stages: ORDER,
        agents: [],
      },
      { ctx, stageLabel, open: new Set() },
    );
    const labels = [...box.querySelectorAll(".ar-bdg-i")].map((el) => [
      el.getAttribute("role"),
      el.getAttribute("aria-label"),
    ]);
    expect(labels).toContainEqual(["img", t("lib.activity_rounds.badge.failed", { count: 1 })]);
    expect(labels).toContainEqual(["img", t("lib.activity_rounds.badge.stopped")]);
    expect(labels).toContainEqual(["img", t("lib.activity_rounds.badge.questions", { count: 1 })]);
  });
});

function mark(over: Partial<VerdictMark>): VerdictMark {
  return {
    verdict: "fail",
    reason: "",
    kickBackTo: "",
    agent: "",
    at: "",
    reportId: "",
    headline: "",
    needsPerson: 0,
    ...over,
  };
}

describe("what a run left", () => {
  it("takes a run's last note from its agent while it ran, not its questions", () => {
    const note = comment({
      id: "n1",
      source: "agent",
      author: "qa-agent",
      body: "All green but one",
      created_at: "2026-10-01T13:30:00Z",
    });
    const r4 = { ...runs[3], finishedAt: "2026-10-01T13:40:00Z" };
    const rounds = buildRounds(
      input({ runs: [runs[0], runs[1], runs[2], r4, runs[4]], comments: [...comments, note] }),
      { system: false },
    );
    const item = rounds[1].visits[1].items.find((x) => x.kind === "run");
    expect(item?.note?.id).toBe("n1");
    expect(rounds[0].visits[0].items.find((x) => x.kind === "run")?.note).toBeUndefined();
  });

  it("stands a stop event in for a stop with no comment", () => {
    const blocked: StreamEvent = {
      id: "blk",
      kind: "task_blocked",
      created_at: "2026-10-01T11:30:00Z",
      payload: { stage: "testing", agent: "qa-agent" },
    };
    const rounds = buildRounds(input({ events: [...events, blocked] }), { system: false });
    const stop = rounds[0].visits[1].items.find((x) => x.kind === "stop");
    expect(stop?.event?.id).toBe("blk");
    expect(rounds[0].visits[1].stops).toBe(1);
    // The typed stop comment already covers the event written next to it.
    const near: StreamEvent = { ...blocked, id: "blk2", created_at: "2026-10-01T14:01:00Z" };
    const again = buildRounds(input({ events: [...events, near] }), { system: false });
    expect(again[1].visits[1].items.filter((x) => x.kind === "stop")).toHaveLength(1);
  });

  it("says a verdict in words, and matches it to the run that wrote it", () => {
    expect(verdictText({ reason: "branch_behind", headline: "Branch is 75 commits behind" })).toBe(
      "Branch is 75 commits behind",
    );
    expect(verdictText({ reason: "Tests fail on login", headline: "Blocked" })).toBe(
      "Tests fail on login",
    );
    expect(verdictText({ reason: "branch_behind", headline: "" })).toBe("branch_behind");
    expect(verdictText(undefined)).toBe("");
    const v = {
      ...stageVisits(events, ORDER)[1],
      verdicts: [
        mark({ reason: "x", agent: "qa-agent", at: "2026-10-01T11:20:00Z" }),
        mark({ reason: "y", agent: "other", at: "2026-10-01T11:25:00Z" }),
      ],
    };
    const r = { ...runs[1], finishedAt: "2026-10-01T11:30:00Z" };
    expect(runVerdict(r, v)?.reason).toBe("x");
  });

  it("lays out the cost cards", () => {
    const box = document.createElement("div");
    box.innerHTML = costCardsHTML({ cost: "$1.20", runs: 3, tokens: "40k", span: "2 h 05" });
    expect([...box.querySelectorAll(".ar-cost-c .v")].map((e) => e.textContent)).toEqual([
      "$1.20",
      "3",
      "40k",
      "2 h 05",
    ]);
    expect(box.querySelectorAll(".ar-cost-c .l")[1].textContent).toBe(
      t("lib.activity_rounds.cost.runs"),
    );
  });
});
