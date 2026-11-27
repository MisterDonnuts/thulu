import { describe, expect, it } from "vitest";

import {
  currentRound,
  reportHeadline,
  roundAt,
  stageVisits,
  visitsWithVerdicts,
  type FlowEvent,
  type FlowReport,
} from "./rounds";

const order = ["refinement", "in_progress", "testing", "review"];
const at = (m: number) => new Date(Date.UTC(2026, 10, 20, 10, m)).toISOString();

// The shape the daemon writes today: stage_entered {from,to,via,by_person} with by_person
// as a string, and stage_verdict {agent,kick_back_to,reason,stage,verdict}. No round number.
const entered = (m: number, from: string, to: string, byPerson = false): FlowEvent => ({
  kind: "stage_entered",
  created_at: at(m),
  payload: { from, to, via: byPerson ? "manual" : "auto", by_person: String(byPerson) },
});
const verdict = (m: number, stage: string, v: string, extra: Record<string, string> = {}) => ({
  kind: "stage_verdict",
  created_at: at(m),
  payload: { agent: "thulu-evidence", stage, verdict: v, kick_back_to: "", reason: "", ...extra },
});

const events: FlowEvent[] = [
  entered(0, "", "refinement"),
  entered(5, "refinement", "in_progress"),
  entered(20, "in_progress", "testing"),
  verdict(40, "testing", "kick_back", { kick_back_to: "in_progress", reason: "tests_failing" }),
  entered(41, "testing", "in_progress"),
  entered(50, "in_progress", "testing"),
  verdict(70, "testing", "block", { reason: "needs_person" }),
  // A comment-like event that is not part of the flow.
  { kind: "run_started", created_at: at(21), payload: { stage: "testing" } },
];

const reports: FlowReport[] = [
  {
    id: "rep-1",
    kind: "stage_verdict",
    stage: "testing",
    created_at: new Date(Date.parse(at(40)) + 300).toISOString(),
    body: "---\nverdict: kick_back\nheadline: TC-7 fails on the return URL\n---\n\n# Testing\n",
  },
  {
    id: "rep-2",
    kind: "stage_verdict",
    stage: "testing",
    created_at: new Date(Date.parse(at(70)) + 120).toISOString(),
    body: "anything",
    verdict_meta: {
      verdict: "block",
      headline: "5 pass, 7 need a person",
      needs_person: [{ ref: "TC-3" }, { ref: "TC-4" }],
    },
  },
  // A report of another stage at the same instant must not attach to Testing.
  { id: "rep-x", kind: "stage_verdict", stage: "review", created_at: at(70), body: "x" },
];

describe("stageVisits", () => {
  it("opens a new round when the task enters a stage earlier than the one it left", () => {
    const v = stageVisits(events, order);
    expect(v.map((x) => `${x.round}:${x.stage}`)).toEqual([
      "1:refinement",
      "1:in_progress",
      "1:testing",
      "2:in_progress",
      "2:testing",
    ]);
    expect(currentRound(v)).toBe(2);
    expect(v[2].next).toMatchObject({ stage: "in_progress", back: true, byPerson: false });
    expect(v.at(-1)?.next).toBeUndefined();
  });

  it("prefers the round the daemon stamps", () => {
    const stamped = events.map((e) =>
      e.kind === "stage_entered" && e.created_at >= at(41)
        ? { ...e, payload: { ...e.payload, round: 4 } }
        : e,
    );
    expect(currentRound(stageVisits(stamped, order))).toBe(4);
  });

  it("reads by_person sent as a string", () => {
    const v = stageVisits(
      [entered(0, "", "testing"), entered(9, "testing", "review", true)],
      order,
    );
    expect(v[0].next?.byPerson).toBe(true);
    expect(v[1].byPerson).toBe(true);
  });

  it("never counts a stage missing from the board as earlier", () => {
    const v = stageVisits([entered(0, "", "testing"), entered(9, "testing", "gone")], order);
    expect(v.map((x) => x.round)).toEqual([1, 1]);
  });
});

describe("visitsWithVerdicts", () => {
  const v = visitsWithVerdicts(events, order, reports);

  it("attaches each verdict to the visit it ended, with the report its run wrote", () => {
    expect(v[2].verdicts).toHaveLength(1);
    expect(v[2].verdicts[0]).toMatchObject({
      verdict: "kick_back",
      kickBackTo: "in_progress",
      reportId: "rep-1",
      headline: "TC-7 fails on the return URL",
    });
    expect(v[4].verdicts[0]).toMatchObject({
      verdict: "block",
      reportId: "rep-2",
      needsPerson: 2,
      headline: "5 pass, 7 need a person",
    });
  });

  it("works without reports, as an older daemon gives", () => {
    const bare = visitsWithVerdicts(events, order);
    expect(bare[2].verdicts[0]).toMatchObject({ verdict: "kick_back", reportId: "", headline: "" });
  });
});

describe("reportHeadline", () => {
  it("falls back to the first line of the body without markdown", () => {
    expect(
      reportHeadline({ id: "r", kind: "x", created_at: at(0), body: "## **Verified** the fix\n" }),
    ).toBe("Verified the fix");
    expect(reportHeadline(undefined)).toBe("");
  });
});

describe("roundAt", () => {
  it("puts an instant in the round open at that time", () => {
    const v = stageVisits(events, order);
    expect(roundAt(v, at(-5))).toBe(1);
    expect(roundAt(v, at(30))).toBe(1);
    expect(roundAt(v, at(45))).toBe(2);
    expect(roundAt([], at(45))).toBe(1);
  });
});
