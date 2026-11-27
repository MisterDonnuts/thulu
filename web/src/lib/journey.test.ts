// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import type { RunInfo } from "./activity";
import {
  JOURNEY_MIN_W,
  journeyHTML,
  journeyModel,
  journeySvgHTML,
  labelLines,
  spanText,
  type JourneyInput,
  type JourneyModel,
} from "./journey";
import type { Visit } from "./rounds";

const COLS = [
  { id: "spec", label: "Spec", color: "#56ccf2", agent: "spec-agent" },
  { id: "dev", label: "Dev", color: "#f2994a", agent: "dev-agent" },
  { id: "qa", label: "QA", color: "#27ae60", agent: "qa-agent" },
  { id: "review", label: "Human review and push", color: "#bb6bd9", agent: "" },
];

function visit(stage: string, round: number, at: string, until: string, byPerson = false): Visit {
  return { stage, round, at, until, from: "", via: "", byPerson, verdicts: [] };
}

function run(over: Partial<RunInfo>): RunInfo {
  return {
    id: "r",
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

// spec → dev → qa, QA sends it back (R2), dev → review skipping QA, a person sends it back (R3).
const VISITS: Visit[] = [
  visit("spec", 1, "2026-10-01T10:00:00Z", "2026-10-01T10:10:00Z"),
  visit("dev", 1, "2026-10-01T10:10:00Z", "2026-10-01T10:40:00Z"),
  visit("qa", 1, "2026-10-01T10:40:00Z", "2026-10-01T10:50:00Z"),
  visit("dev", 2, "2026-10-01T10:50:00Z", "2026-10-01T11:00:00Z"),
  visit("review", 2, "2026-10-01T11:00:00Z", "2026-10-01T11:30:00Z"),
  visit("dev", 3, "2026-10-01T11:30:00Z", "", true),
];

function input(over: Partial<JourneyInput> = {}): JourneyInput {
  return {
    columns: COLS,
    visits: VISITS,
    runs: [],
    questions: [],
    stops: [],
    current: "dev",
    running: false,
    now: Date.parse("2026-10-01T12:00:00Z"),
    width: 920,
    ...over,
  };
}

function model(over: JourneyInput): JourneyModel {
  const m = journeyModel(over);
  if (!m) throw new Error("no model"); // i18n-ignore a test failure
  return m;
}

describe("journey model", () => {
  it("draws nothing without columns", () => {
    expect(journeyModel(input({ columns: [] }))).toBeNull();
    expect(journeyHTML(input({ columns: [] }))).toBe("");
  });

  it("is as wide as the drawer, never under 780, with the Lab's gutters", () => {
    expect(journeyModel(input({ width: 920 }))?.W).toBe(870);
    expect(journeyModel(input({ width: 600 }))?.W).toBe(JOURNEY_MIN_W);
    expect(journeyModel(input({ width: 1200 }))?.W).toBe(1150);
    const m = model(input());
    expect(m.nodes.map((n) => n.x)).toEqual([58, 309.33333333333337, 560.6666666666667, 812]);
  });

  it("counts the visits and the time spent in each column", () => {
    const m = model(input());
    expect(m.nodes.map((n) => n.visits)).toEqual([1, 3, 1, 1]);
    // Dev: 30 + 10 min, then 30 min so far in the open visit.
    expect(m.nodes[1].ms).toBe(70 * 60000);
    expect(m.nodes.map((n) => n.now)).toEqual([false, true, false, false]);
  });

  it("merges forward hops, keeps skips, and splits returns by who made them", () => {
    const hops = model(input()).hops.map((h) => ({
      from: h.from,
      to: h.to,
      back: h.back,
      person: h.person,
      rounds: h.rounds,
    }));
    expect(hops).toEqual([
      { from: "spec", to: "dev", back: false, person: false, rounds: [1] },
      { from: "dev", to: "qa", back: false, person: false, rounds: [1] },
      { from: "qa", to: "dev", back: true, person: false, rounds: [2] },
      { from: "dev", to: "review", back: false, person: false, rounds: [2] },
      { from: "review", to: "dev", back: true, person: true, rounds: [3] },
    ]);
  });

  it("names why each return happened, once per round", () => {
    const whys = new Map([[2, "QA (qa-agent) · tests fail"]]);
    const back = model(input({ whys })).hops.find((h) => h.from === "qa");
    expect(back?.whys).toEqual(["R2: QA (qa-agent) · tests fail"]);
  });

  it("puts badges on the column: now, questions, stops and failed runs", () => {
    const m = model(
      input({
        questions: [{ stage: "dev", blocking: true }, { stage: "dev" }],
        stops: ["qa"],
        runs: [run({ stage: "qa", state: "failed" }), run({ stage: "qa", state: "done" })],
      }),
    );
    expect(m.nodes[1].badges.map((b) => [b.kind, b.icon, b.text])).toEqual([
      ["now", "user", t("lib.journey.now")],
      ["q", "help", "2"],
    ]);
    expect(m.nodes[1].waited).toBe(1);
    expect(m.nodes[2].badges.map((b) => [b.kind, b.text])).toEqual([
      ["blk", "1"],
      ["fail", "1"],
    ]);
    // Badge width = characters × 6.5 + 24, centred on the node with 3px between.
    const [a, b] = m.nodes[2].badges;
    expect(a.w).toBe(30.5);
    expect(b.x - a.x).toBe(33.5);
    expect(model(input({ running: true })).nodes[1].badges[0].kind).toBe("run");
  });

  it("draws a task that never moved as unseen columns, and a lone column in the middle", () => {
    const empty = model(input({ visits: [], current: "spec" }));
    expect(empty.hops).toEqual([]);
    expect(empty.nodes.map((n) => n.visits)).toEqual([0, 0, 0, 0]);
    const box = document.createElement("div");
    box.innerHTML = journeySvgHTML(empty);
    expect(box.querySelectorAll(".node.unseen")).toHaveLength(4);
    expect(box.querySelector(".node.now .nv")?.textContent).toBe("");

    const one = model(
      input({
        columns: [COLS[0]],
        visits: [visit("spec", 1, "2026-10-01T10:00:00Z", "")],
        current: "spec",
      }),
    );
    expect(one.nodes[0].x).toBe(one.W / 2);
    expect(one.nodes[0].visits).toBe(1);
  });
});

describe("journey svg", () => {
  const box = document.createElement("div");
  box.innerHTML = journeyHTML(input());

  it("draws a node per column with its visits and a halo on the current one", () => {
    expect([...box.querySelectorAll(".node .nv")].map((n) => n.textContent)).toEqual([
      "1",
      "3",
      "1",
      "1",
    ]);
    expect(box.querySelectorAll(".halo")).toHaveLength(1);
    expect(box.querySelector(".node.now.waiting .nc")?.getAttribute("style")).toBe("--sc:#f2994a");
  });

  it("draws forward arrows, the skip arc and the return arcs with their round labels", () => {
    expect(box.querySelectorAll(".hop.fwd:not(.skip) line")).toHaveLength(2);
    expect(box.querySelector(".hop.skip .cnt")?.textContent).toBe(
      t("lib.journey.skipped", { count: 1 }),
    );
    expect(box.querySelector(".hop.back.agent .arc-lbl")?.textContent).toBe("R2");
    expect(box.querySelector(".hop.back.person .arc-lbl")?.textContent).toBe("R3");
    expect(box.querySelector(".hop.back.person title")?.textContent).toBe(
      t("lib.journey.tip.back_person", { to: "Dev", count: 1 }),
    );
  });

  it("counts repeated forward hops with ×N", () => {
    const again: Visit[] = [
      ...VISITS.slice(0, 4),
      visit("qa", 2, "2026-10-01T11:00:00Z", "2026-10-01T11:10:00Z"),
    ];
    const b = document.createElement("div");
    b.innerHTML = journeyHTML(input({ visits: again, current: "qa" }));
    expect(b.querySelector(".hop.fwd:not(.skip) .cnt")?.textContent).toBe("×2");
  });

  it("explains each column in its tooltip and labels the chart", () => {
    const dev = box.querySelectorAll(".node title")[1].textContent;
    expect(dev.split("\n")).toEqual([
      "Dev · dev-agent",
      t("lib.journey.tip.visited", { count: 3, time: spanText(70 * 60000) }),
    ]);
    expect(box.querySelector("svg")?.getAttribute("aria-label")).toBe(t("lib.journey.label"));
    expect(box.querySelector(".ar-jl")).not.toBeNull();
  });

  it("drops colours that are not hex", () => {
    const b = document.createElement("div");
    b.innerHTML = journeyHTML(
      input({ columns: [{ ...COLS[0], color: "red;x:y" }, ...COLS.slice(1)] }),
    );
    expect(b.querySelector(".node .nc")?.hasAttribute("style")).toBe(false);
  });
});

describe("journey text", () => {
  it("breaks labels at 13 characters", () => {
    expect(labelLines("Human review and push")).toEqual(["Human review", "and push"]);
    expect(labelLines("Dev")).toEqual(["Dev"]);
  });

  it("writes a span in minutes under an hour, else hours and minutes", () => {
    expect(spanText(15 * 60000)).toBe(t("lib.journey.minutes", { m: 15 }));
    expect(spanText(125 * 60000)).toBe(t("lib.journey.hours", { h: 2, mm: "05" }));
  });
});
