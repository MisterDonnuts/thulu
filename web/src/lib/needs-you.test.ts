import { afterEach, describe, expect, it, vi } from "vitest";

import { t } from "../i18n";
import { esc } from "./ui";
import {
  actionChoices,
  actionFormHTML,
  actionProblem,
  cardOptions,
  earlierStages,
  hasAttentionContract,
  headlineOf,
  liveNeedCount,
  needsYouPill,
  refsText,
  rerunLabel,
  runningLineText,
  moveBackReasonHTML,
  statusActions,
  statusLineHTML,
  typicalSeconds,
  verdictLostHTML,
  waiveFormHTML,
  waiveProblem,
  type TaskAttention,
} from "./needs-you";

// A real task as the daemon describes it: Testing passed 5 cases, 7 need a person, verdict lost.
const rb3043: TaskAttention = {
  kind: "decision",
  headline: "Testing passed 5 of 12 cases; 7 need a person to test them or skip them.",
  stage: "testing",
  run_id: "runner-1",
  verdict_lost: true,
  items: [
    {
      ref: "TC-3",
      title: "Login with Google",
      ask: "Sign in with a real account",
      why: "real_account",
    },
    { ref: "TC-4", title: "Login with Apple", why: "real_account" },
    { ref: "TC-5", title: "Email login", why: "real_account" },
    { ref: "TC-6", title: "Register", why: "real_account" },
    { ref: "TC-9", title: "Return URL", why: "data_write" },
    { ref: "TC-10", title: "Profile save", why: "data_write" },
    { ref: "TC-12", title: "Native shell", why: "device" },
  ],
  options: ["test_by_hand", "waive_cases", "send_back", "rerun"],
};

const needs = rb3043.items ?? [];

describe("hasAttentionContract", () => {
  it("tells a daemon that sends attention, even null, from an older one", () => {
    expect(hasAttentionContract({ attention: null })).toBe(true);
    expect(hasAttentionContract({ attention: rb3043 })).toBe(true);
    expect(hasAttentionContract({ id: "x" })).toBe(false);
    expect(hasAttentionContract(null)).toBe(false);
  });
});

describe("refsText", () => {
  it("folds three or more consecutive cases into a range and lists the rest", () => {
    expect(refsText(["TC-3", "TC-4", "TC-5", "TC-6"])).toBe("TC-3–6");
    expect(refsText(["TC-9", "TC-10"])).toBe("TC-9, TC-10");
    expect(refsText(["TC-1", "TC-2", "TC-3", "TC-7", "AC-8"])).toBe("TC-1–3, TC-7, AC-8");
    expect(refsText(["setup", "TC-2"])).toBe("setup, TC-2");
  });
});

describe("cardOptions", () => {
  it("keeps the daemon's options in a fixed order and drops unknown ones", () => {
    expect(cardOptions({ ...rb3043, options: ["rerun", "bogus", "test_by_hand"] })).toEqual([
      "test_by_hand",
      "rerun",
    ]);
  });

  it("falls back to what the kind allows when the daemon sends none", () => {
    expect(cardOptions({ ...rb3043, options: undefined })).toEqual([
      "test_by_hand",
      "waive_cases",
      "send_back",
    ]);
    expect(cardOptions({ kind: "question", question_id: "q1" })).toEqual(["answer_question"]);
    expect(cardOptions({ kind: "approval" })).toEqual(["decide_approval"]);
    expect(cardOptions({ kind: "failure" })).toEqual(["send_back", "rerun"]);
  });

  it("never offers case actions without cases, nor an answer without a question", () => {
    expect(
      cardOptions({ kind: "decision", items: [], options: ["test_by_hand", "waive_cases"] }),
    ).toEqual([]);
    expect(cardOptions({ kind: "question", options: ["answer_question"] })).toEqual([]);
  });
});

describe("headlineOf", () => {
  it("uses the daemon's sentence and falls back per kind", () => {
    expect(headlineOf(rb3043)).toBe(rb3043.headline);
    expect(headlineOf({ kind: "question", headline: "  " })).toBe(
      t("lib.needs_you.fallback.question"),
    );
    expect(headlineOf({ kind: "whatever" })).toBe(t("lib.needs_you.fallback.decision"));
  });
});

describe("status line", () => {
  it("leads with what is missing, then skip; Send back and Re-run live in the menu", () => {
    expect(statusActions(rb3043)).toEqual(["missing", "skip"]);
    expect(statusActions({ ...rb3043, thread_id: "c1" })).toEqual(["missing", "stop", "skip"]);
  });

  it("answers a question, reviews an approval and re-runs a failure", () => {
    expect(statusActions({ kind: "question", question_id: "q1", thread_id: "c9" })).toEqual([
      "answer",
    ]);
    expect(statusActions({ kind: "approval", thread_id: "c2" })).toEqual(["approval"]);
    expect(statusActions({ kind: "failure" })).toEqual(["rerun"]);
  });

  it("offers a read-only viewer only what reads", () => {
    expect(statusActions(rb3043, true)).toEqual(["missing"]);
    expect(statusActions({ kind: "failure" }, true)).toEqual([]);
  });

  it("counts the cases still blocked, live, and falls back to the attention's list", () => {
    expect(liveNeedCount(rb3043)).toBe(7);
    expect(
      liveNeedCount(rb3043, [
        { key: "TC-3", status: "queued" },
        { key: "TC-4", status: "pass" },
        { key: "TC-5", status: "blocked" },
      ]),
    ).toBe(5);
    expect(liveNeedCount(null)).toBe(0);
    expect(needsYouPill(5)).toBe(t("lib.needs_you.pill_count", { count: 5 }));
    expect(needsYouPill(0)).toBe(t("lib.needs_you.title"));
  });

  it("lays out the pill, the sentence and the actions, and escapes the sentence", () => {
    const html = statusLineHTML({
      tone: "warn",
      pill: needsYouPill(7),
      text: "<img src=x> 7 need a person",
      actionsHTML: "<button>x</button>",
      footHTML: verdictLostHTML(),
    });
    expect(html).toContain('data-tone="warn"');
    expect(html).toContain(esc(t("lib.needs_you.pill_count", { count: 7 })));
    expect(html).not.toContain("<img");
    expect(html).toContain('<div class="ny-acts"><button>x</button></div>');
    expect(html).toContain(esc(t("lib.needs_you.verdict_lost")));
  });
});

describe("waive form", () => {
  it("starts with every needs-person case checked", () => {
    const html = waiveFormHTML(needs);
    expect(html.match(/name="case"/g)).toHaveLength(7);
    expect(html.match(/<input[^>]* checked/g)).toHaveLength(7);
    expect(html).toMatch(/<textarea id="ny-waive-reason" name="reason"[^>]* required/);
  });

  it("keeps what the person unchecked and typed across repaints", () => {
    const html = waiveFormHTML(needs, { cases: ["TC-9"], reason: "No <prod> data" });
    expect(html.match(/<input[^>]* checked/g)).toHaveLength(1);
    expect(html).toMatch(/value="TC-9" checked/);
    expect(html).toContain("No &lt;prod&gt; data");
  });

  it("requires at least one case and a reason", () => {
    expect(waiveProblem({ cases: [], reason: "x" })).toBe("cases");
    expect(waiveProblem({ cases: ["TC-3"], reason: "   " })).toBe("reason");
    expect(waiveProblem({ cases: ["TC-3"], reason: "covered by staging" })).toBeNull();
  });
});

describe("action forms", () => {
  const flow = [
    { id: "refinement", label: "Spec" },
    { id: "in_progress", label: "Fix local" },
    { id: "testing", label: "Testing" },
    { id: "review", label: "Human review" },
  ];

  it("sends back only to earlier stages, the nearest picked", () => {
    const choices = actionChoices("send_back", flow, "testing");
    expect(choices.map((c) => c.id)).toEqual(["refinement", "in_progress"]);
    expect(earlierStages(flow, "refinement")).toEqual([]);
    expect(earlierStages(flow, "gone")).toEqual([]);
    expect(actionFormHTML("send_back", choices)).toMatch(/<option value="in_progress" selected>/);
  });

  it("moves to any other stage and re-runs where the task is, without a stage field", () => {
    expect(actionChoices("move", flow, "testing").map((c) => c.id)).toEqual([
      "refinement",
      "in_progress",
      "review",
    ]);
    expect(actionChoices("rerun", flow, "testing")).toEqual([]);
    const rerun = actionFormHTML("rerun", []);
    expect(rerun).not.toContain("<select");
    expect(rerun).toMatch(/<textarea id="act-why" name="reason"[^>]* required/);
  });

  it("requires a stage it offers and a reason", () => {
    const choices = actionChoices("send_back", flow, "testing");
    expect(actionProblem("send_back", { stage: "review", reason: "x" }, choices)).toBe("stage");
    expect(actionProblem("send_back", { stage: "in_progress", reason: " " }, choices)).toBe(
      "reason",
    );
    expect(
      actionProblem("send_back", { stage: "in_progress", reason: "fix TC-7" }, choices),
    ).toBeNull();
    expect(actionProblem("rerun", { reason: "staging is back" }, [])).toBeNull();
    expect(actionProblem("rerun", { reason: "" }, [])).toBe("reason");
  });

  it("asks the board's move-back reason in a required field named reason", () => {
    const html = moveBackReasonHTML("mb");
    expect(html).toContain('<textarea id="mb" name="reason" rows="4" required></textarea>');
    expect(html).toContain(t("lib.needs_you.send_back.note"));
  });
});

describe("running line and rerun length", () => {
  afterEach(() => vi.useRealTimers());

  it("names the stage, the agent and when it started", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-21T02:30:00Z"));
    const line = runningLineText({
      stage: "Testing",
      agent: "thulu-evidence",
      startedAt: "2026-11-21T02:18:00Z",
    });
    expect(line).toContain("Testing");
    expect(line).toContain("thulu-evidence");
    expect(line).toContain("12");
    expect(runningLineText({ stage: "Testing", agent: "" })).toBe(
      t("lib.needs_you.running.line", {
        stage: "Testing",
        agent: t("lib.needs_you.running.someone"),
      }),
    );
  });

  it("quotes the median clean run of the stage", () => {
    const run = (stage: string, mins: number, state = "done") => ({
      stage,
      state,
      startedAt: "2026-11-20T10:00:00Z",
      finishedAt: new Date(Date.parse("2026-11-20T10:00:00Z") + mins * 60000).toISOString(),
    });
    const runs = [
      run("testing", 40),
      run("testing", 45),
      run("testing", 300, "failed"),
      run("testing", 50),
      run("dev", 5),
    ];
    expect(typicalSeconds(runs, "testing")).toBe(45 * 60);
    expect(typicalSeconds(runs, "nowhere")).toBe(0);
    expect(rerunLabel("Testing", 0)).toBe(t("lib.needs_you.actions.rerun", { stage: "Testing" }));
    expect(rerunLabel("Testing", 45 * 60)).toContain("45");
  });
});
