import { describe, expect, it } from "vitest";
import { historyActor, historyActorKind, historyText } from "./history";

const ctx = {
  stageLabel: (id: string) => ({ in_progress: "Development", verify: "QA" })[id] ?? id,
};

describe("historyText", () => {
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
