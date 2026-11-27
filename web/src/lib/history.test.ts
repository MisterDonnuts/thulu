import { describe, expect, it } from "vitest";
import { historyActor, historyActorKind, historyText } from "./history";

const ctx = {
  stageLabel: (id: string) => ({ in_progress: "Development", verify: "QA" })[id] ?? id,
};

describe("historyText", () => {
  it("says who skipped a column and why, and why a skip went away", () => {
    const added = { stage: "verify", reason: "docs only", by: "agent:thulu-refiner" };
    expect(historyText({ kind: "stage_skip_added", payload: added }, ctx)).toBe(
      "thulu-refiner skips QA: docs only",
    );
    const removed = (p: Record<string, unknown>) =>
      historyText(
        { kind: "stage_skip_removed", payload: { stage: "verify", by: "ana@example.com", ...p } },
        ctx,
      );
    expect(removed({ cause: "removed", reason: "touches UI" })).toBe(
      "ana@example.com made QA run again: touches UI",
    );
    expect(removed({ cause: "removed", reason: "touches UI", passed: true })).toBe(
      "ana@example.com made QA run again, so the task goes back there: touches UI",
    );
    expect(removed({ cause: "removed", reason: "r", passed: true, deferred: true })).toContain(
      "when the current run ends",
    );
    expect(removed({ cause: "entered", reason: "moved here by ana@example.com" })).toBe(
      "QA runs: ana@example.com moved the task there",
    );
    expect(removed({ cause: "ui_diff", by: "octopus", reason: "x" })).toBe(
      "QA runs after all: the branch changes UI files",
    );
    expect(removed({ cause: "diff_unreadable" })).toContain("could not be read");
    expect(removed({ cause: "refinement_dropped" })).toContain("no longer skips it");
    expect(removed({ cause: "board_changed" })).toContain("another board");
    expect(historyText({ kind: "stage_skip_removed", payload: {} }, ctx)).toBe("");
  });

  it("says why a column was passed over", () => {
    expect(
      historyText(
        { kind: "stage_skipped", payload: { stage: "verify", task_skip: true, reason: "docs" } },
        ctx,
      ),
    ).toBe("Passed over QA, which this task skips: docs");
    expect(
      historyText({ kind: "stage_skipped", payload: { stage: "verify", required: ["ui"] } }, ctx),
    ).toBe("Passed over QA, which waits on ui");
    expect(
      historyText(
        {
          kind: "stage_entered",
          payload: { from: "verify", to: "in_progress", via: "skip_removed" },
        },
        ctx,
      ),
    ).toBe("QA → Development (a skip was removed)");
  });

  it("names stages instead of ids and says how a move happened", () => {
    expect(
      historyText({ kind: "stage_entered", payload: { from: "verify", to: "in_progress" } }, ctx),
    ).toBe("QA → Development");
    expect(
      historyText(
        { kind: "stage_entered", payload: { from: "verify", to: "in_progress", via: "kick_back" } },
        ctx,
      ),
    ).toBe("QA → Development (the auditor sent it back)");
    expect(historyText({ kind: "stage_entered", payload: null }, ctx)).toBe("");
  });

  it("says a run's result did not apply and who waits for it", () => {
    expect(
      historyText(
        {
          kind: "run_outlived",
          payload: { agent: "refiner", stage: "verify", task_stage: "in_progress" },
        },
        ctx,
      ),
    ).toBe("refiner finished in QA after the task moved to Development; its result did not apply");
    expect(
      historyText(
        {
          kind: "stage_launch_deferred",
          payload: { agent: "tester", stage: "in_progress", busy_stage: "verify" },
        },
        ctx,
      ),
    ).toBe("tester waits for the agent of QA before starting in Development");
    expect(historyText({ kind: "run_outlived", payload: {} }, ctx)).toBe("");
  });

  it("lists the checks of an audit the daemon skipped", () => {
    const payload = {
      stage: "in_progress",
      agent: "thulu-developer",
      checks: [
        { check: "exit", state: "clear" },
        { check: "locked_tests", state: "clear" },
        { check: "diff_size", state: "unread" },
        { check: "brand_new", state: "clear" },
      ],
    };
    expect(historyText({ kind: "auditor_skipped", payload }, ctx)).toBe(
      "Audit of thulu-developer skipped in Development: no signals (exit code, locked tests, diff size (not readable), brand_new)",
    );
    expect(historyText({ kind: "auditor_skipped", payload: { stage: "verify" } }, ctx)).toBe("");
  });

  it("says which task a copy came from", () => {
    expect(
      historyText({ kind: "task_created", payload: { cloned_from: "bbbb000000000009" } }, ctx),
    ).toBe("Copy of task bbbb0000");
    expect(historyText({ kind: "task_created", payload: {} }, ctx)).toBe("");
  });

  it("puts verdicts and run ends in words", () => {
    expect(
      historyText(
        { kind: "auditor_verdict", payload: { verdict: "kick_back", kick_back_to: "in_progress" } },
        ctx,
      ),
    ).toBe("Send it back to Development");
    expect(
      historyText({ kind: "auditor_verdict", payload: { verdict: "needs_more_turns" } }, ctx),
    ).toBe("Run it again with more turns");
    expect(
      historyText(
        { kind: "agent_run_finished", payload: { stage: "verify", exit: 1, outcome: "max_turns" } },
        ctx,
      ),
    ).toBe("Ran out of turns in QA");
    expect(
      historyText(
        { kind: "agent_run_finished", payload: { stage: "verify", exit: 143, stopped_by: "ana" } },
        ctx,
      ),
    ).toBe("Stopped by ana in QA");
    expect(
      historyText({ kind: "agent_run_finished", payload: { stage: "verify", exit: 2 } }, ctx),
    ).toBe("Failed in QA (exit code 2)");
  });

  it("says how a message reached the agent, and keeps the summary when the data is old", () => {
    expect(
      historyText(
        { kind: "conversation_message", payload: { delivery: "steered", line: "use v2" } },
        ctx,
      ),
    ).toBe("Handed to the running agent: use v2");
    expect(
      historyText({ kind: "conversation_message", payload: { delivery: "steered" } }, ctx),
    ).toBe("");
    expect(historyText({ kind: "branch_created", payload: {} }, ctx)).toBe("");
  });

  it("reads a queue or launch reason in the reader's language", () => {
    expect(
      historyText(
        { kind: "run_not_started", payload: { reason: "x", reason_i18n: { en: "No agent" } } },
        ctx,
      ),
    ).toBe("No agent");
  });
});

describe("historyActor", () => {
  it("names who did an event the way a reader knows them", () => {
    expect(historyActor("agent:desarrollador")).toBe("desarrollador");
    expect(historyActor("octopus")).toBe("Thulu");
    expect(historyActor("system")).toBe("Thulu");
    expect(historyActor("")).toBe("Thulu");
    expect(historyActor(undefined)).toBe("Thulu");
    expect(historyActor("auditor")).toBe("Auditor");
    expect(historyActor("user")).toBe("Someone");
    expect(historyActor("ana@example.com")).toBe("ana@example.com");
  });

  it("says which avatar the author takes", () => {
    expect(historyActorKind("agent:desarrollador")).toBe("agent");
    expect(historyActorKind("auditor")).toBe("agent");
    expect(historyActorKind("system")).toBe("system");
    expect(historyActorKind(undefined)).toBe("system");
    expect(historyActorKind("ana@example.com")).toBe("person");
  });
});
