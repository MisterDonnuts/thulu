// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { blankStage } from "./board-editor";
import {
  activeSkip,
  bindSkipEditor,
  skipChanges,
  skipEditorHTML,
  skipEditorRows,
  skippableReason,
  skippedIDs,
  skipsAhead,
  skipsEditable,
  skipWhyText,
  type StageSkip,
} from "./stage-skips";
import type { Stage } from "./stages";

const col = (id: string, extra: Partial<Stage> = {}): Stage => ({
  ...blankStage(id, id.toUpperCase(), "#5d6477"),
  ...extra,
});

// The Thulu flow's shape: refinement decides the skips, verification opens the draft PR, the two
// reviews are marked never skipped.
const FLOW: Stage[] = [
  col("backlog", { initial: true }),
  col("refinement", { ingest_flags: true }),
  col("test_design", { lock_tests: true }),
  col("in_progress", { push_branch: true }),
  col("verification", { pr_draft: true }),
  col("ai_review", { no_skip: true, agent_verdict: true }),
  col("testing"),
  col("review", { no_skip: true, required_approvals: [{ key: "ok", label: "OK" }] }),
  col("done", { terminal: true }),
];

function must<T>(v: T | null): T {
  if (v === null) throw new Error("missing element");
  return v;
}

const skip = (stage: string, extra: Partial<StageSkip> = {}): StageSkip => ({
  stage,
  reason: `no ${stage}`,
  by: "agent:thulu-refiner",
  at: "2026-11-25T10:00:00Z",
  ...extra,
});

describe("skippableReason", () => {
  // The daemon's table (internal/stage/skippable_test.go).
  it.each([
    [{ initial: true }, "initial"],
    [{ terminal: true }, "terminal"],
    [{ virtual: true }, "virtual"],
    [{ no_skip: true }, "no_skip"],
    [{ ingest_flags: true }, "refinement"],
    [{ required_approvals: [{ key: "a", label: "A" }] }, "approvals"],
    [{ open_pr: true }, "pull_request"],
    [{ pr_draft: true }, "pull_request"],
    [{ pr_ready: true }, "pull_request"],
    [{ merge_target: true }, "pull_request"],
    [{ release_resources: true }, "pull_request"],
    [{ lock_tests: true }, ""],
    [{ push_branch: true }, ""],
    [{ agent_verdict: true }, ""],
    [{ required_approvals: [] }, ""],
    [{}, ""],
  ] as const)("%o is %s", (flags, why) => {
    expect(skippableReason(flags as Partial<Stage>)).toBe(why);
  });

  it("puts each reason in words, and none for a skippable column", () => {
    expect(skipWhyText("no_skip")).toBe("The board marks this column as never skipped.");
    expect(skipWhyText("approvals")).toContain("approval");
    expect(skipWhyText("")).toBe("");
  });
});

describe("activeSkip", () => {
  it("finds the task's skip of a skippable column", () => {
    const task = { stage_skips: [skip("testing")] };
    expect(activeSkip(task, FLOW[6])?.reason).toBe("no testing");
    expect(activeSkip(task, FLOW[2])).toBeNull();
    expect(activeSkip({ stage_skips: null }, FLOW[6])).toBeNull();
    expect(activeSkip(task, undefined)).toBeNull();
  });

  it("ignores a skip on a column the board made unskippable since", () => {
    const task = { stage_skips: [skip("testing")] };
    expect(activeSkip(task, col("testing", { no_skip: true }))).toBeNull();
  });
});

describe("skipsAhead", () => {
  it("counts the skipped columns after the current one only", () => {
    const task = {
      stage: "in_progress",
      stage_skips: [skip("test_design"), skip("testing"), skip("ai_review")],
    };
    expect(skipsAhead(task, FLOW).map((s) => s.id)).toEqual(["testing"]);
    expect([...skippedIDs(task, FLOW)]).toEqual(["test_design", "testing"]);
  });

  it("is empty for a task in a column the flow does not know", () => {
    expect(skipsAhead({ stage: "gone", stage_skips: [skip("testing")] }, FLOW)).toEqual([]);
  });
});

describe("skipEditorRows", () => {
  it("sorts the columns into passed, current, skippable and locked", () => {
    const task = { stage: "in_progress", stage_skips: [skip("test_design")] };
    expect(skipEditorRows(task, FLOW).map((r) => [r.stage, r.kind, r.why])).toEqual([
      ["test_design", "passed", ""],
      ["in_progress", "current", ""],
      ["verification", "locked", "pull_request"],
      ["ai_review", "locked", "no_skip"],
      ["testing", "ahead", ""],
      ["review", "locked", "no_skip"],
    ]);
  });

  it("leaves a passed column that ran out", () => {
    const rows = skipEditorRows({ stage: "testing", stage_skips: [] }, FLOW);
    expect(rows.map((r) => r.stage)).toEqual(["testing", "review"]);
    expect(skipsEditable(rows)).toBe(false);
  });

  it("has nothing to edit once the task finished", () => {
    expect(skipEditorRows({ stage: "done", stage_skips: [skip("testing")] }, FLOW)).toEqual([]);
  });
});

describe("skipEditorHTML", () => {
  const task = {
    stage: "in_progress",
    stage_skips: [skip("test_design"), skip("testing", { by: "ana@example.com" })],
  };
  const rows = skipEditorRows(task, FLOW);
  const root = () => {
    const el = document.createElement("form");
    el.innerHTML = skipEditorHTML(rows);
    bindSkipEditor(el);
    return el;
  };

  it("checks the columns skipped ahead and says who skipped them and why", () => {
    const el = root();
    const testing = must(el.querySelector<HTMLInputElement>('input[name="skip:testing"]'));
    expect(testing.checked).toBe(true);
    expect(testing.closest(".check")?.textContent).toContain(
      "Skipped by ana@example.com: no testing",
    );
    expect(el.querySelector('input[name="run:test_design"]')).not.toBeNull();
    expect(el.textContent).toContain("Skipped by thulu-refiner: no test_design");
  });

  it("disables the locked and current columns with the reason", () => {
    const el = root();
    const review = must(el.querySelector<HTMLInputElement>("#skip-review"));
    expect(review.disabled).toBe(true);
    expect(review.name).toBe("");
    expect(review.closest(".check")?.textContent).toContain(
      "The board marks this column as never skipped.",
    );
    const current = must(el.querySelector<HTMLInputElement>("#skip-in_progress"));
    expect(current.disabled).toBe(true);
    expect(current.closest(".check")?.textContent).toContain("The task is in this column.");
  });

  it("asks for a reason only once a toggle changes, and warns before a move back", () => {
    const el = root();
    const reason = must(el.querySelector<HTMLElement>('[data-skip-reason="test_design"]'));
    const back = must(el.querySelector<HTMLElement>('[data-skip-back="test_design"]'));
    expect(reason.hidden).toBe(true);
    expect(back.hidden).toBe(true);
    expect(back.textContent).toContain("The task goes back to TEST_DESIGN");
    const run = must(el.querySelector<HTMLInputElement>('input[name="run:test_design"]'));
    run.checked = true;
    run.dispatchEvent(new Event("change", { bubbles: true }));
    expect(reason.hidden).toBe(false);
    expect(back.hidden).toBe(false);
    run.checked = false;
    run.dispatchEvent(new Event("change", { bubbles: true }));
    expect(reason.hidden).toBe(true);
  });

  it("escapes column labels", () => {
    const flow = [
      col("b", { initial: true }),
      col("x", { label: "<b>X</b>" }),
      col("d", { terminal: true }),
    ];
    const html = skipEditorHTML(skipEditorRows({ stage: "b", stage_skips: [] }, flow));
    expect(html).not.toContain("<b>X</b>");
    expect(html).toContain("&lt;b&gt;X&lt;/b&gt;");
  });
});

describe("skipChanges", () => {
  const task = { stage: "test_design", stage_skips: [skip("testing")] };
  const flow = [
    col("backlog", { initial: true }),
    col("test_design"),
    col("in_progress"),
    col("testing"),
    col("done", { terminal: true }),
  ];
  const rows = skipEditorRows(task, flow);

  it("adds a newly checked column and removes an unchecked one, with their reasons", () => {
    const out = skipChanges(rows, {
      "skip:in_progress": true,
      "reason:in_progress": "  docs only  ",
      "reason:testing": "touches the API",
    });
    expect(out).toEqual({
      changes: [
        { op: "add", stage: "in_progress", reason: "docs only", back: false },
        { op: "remove", stage: "testing", reason: "touches the API", back: false },
      ],
      missing: [],
    });
  });

  it("lists the changes that have no reason", () => {
    const out = skipChanges(rows, { "skip:in_progress": true, "skip:testing": true });
    expect(out.missing).toEqual(["in_progress"]);
  });

  it("changes nothing when the toggles match the task", () => {
    expect(skipChanges(rows, { "skip:testing": true })).toEqual({ changes: [], missing: [] });
  });

  it("runs a passed column again, which sends the task back", () => {
    const passed = skipEditorRows({ stage: "testing", stage_skips: [skip("in_progress")] }, flow);
    expect(skipChanges(passed, { "run:in_progress": true, "reason:in_progress": "UI" })).toEqual({
      changes: [{ op: "remove", stage: "in_progress", reason: "UI", back: true }],
      missing: [],
    });
  });
});
