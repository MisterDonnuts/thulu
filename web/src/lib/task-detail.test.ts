import { describe, expect, it } from "vitest";

import { t } from "../i18n";
import {
  agentHint,
  approvalsTabName,
  attentionItems,
  attentionRowHTML,
  cancelWaitQuestion,
  causeLink,
  causePicksAgent,
  waitCanceled,
  foldIndex,
  orphanAgentsHTML,
  orphanSummary,
  primaryAction,
  taskPriorityTagHTML,
  quotaLink,
  reportsTabName,
  reasonLine,
  runBadgeTone,
  runPillHTML,
  runsLine,
  runsTitle,
  runWord,
  siblingNav,
  boardCrumbAction,
  type AttentionInput,
} from "./task-detail";

describe("runWord", () => {
  it("reads Done in a terminal stage whatever the last run did", () => {
    expect(runWord("failed", true)?.id).toBe("done");
  });

  it("maps each run status to a word and a tone", () => {
    expect(runWord("running", false)).toMatchObject({ id: "running", tone: "info", icon: "" });
    expect(runWord("queued", false)).toMatchObject({ id: "queued", tone: "muted" });
    expect(runWord("awaiting_input", false)).toMatchObject({ id: "needs_answer", tone: "warning" });
    expect(runWord("awaiting_approval", false)).toMatchObject({
      id: "needs_approval",
      tone: "warning",
    });
    expect(runWord("failed", false)).toMatchObject({ id: "failed", tone: "danger" });
    expect(runWord("needs_more_turns", false)?.id).toBe("out_of_turns");
    expect(runWord("out_of_turns", false)?.id).toBe("out_of_turns");
    expect(runWord("blocked", false)?.id).toBe("blocked_auditor");
  });

  it("tells a run that never started, a lost credential and a stop apart from a failure", () => {
    expect(runWord("not_started", false)).toMatchObject({ id: "not_started", tone: "danger" });
    expect(runWord("not_started", false)?.label).toBe("Did not start");
    expect(runWord("credential_blocked", false)).toMatchObject({
      id: "credential_blocked",
      label: "Credential expired",
    });
    expect(runWord("no_agent", false)).toMatchObject({ id: "no_agent", tone: "warning" });
    expect(runWord("stopped", false)).toMatchObject({ id: "stopped", tone: "muted" });
    expect(runWord("resume_failed", false)?.label).toBe("Could not resume");
  });

  it("puts why the task waits in the pill's tooltip", () => {
    const pill = runPillHTML(
      runWord("not_started", false),
      "**Run did not start**\nNo LLM credential for claude-code.",
    );
    expect(pill).toContain('title="No LLM credential for claude-code."');
    expect(runPillHTML(runWord("failed", false))).not.toContain("title=");
  });

  it("shows nothing for an idle task", () => {
    expect(runWord("idle", false)).toBeNull();
    expect(runWord(undefined, false)).toBeNull();
    expect(runPillHTML(null)).toBe("");
  });

  it("draws the pulse dot for a running task and an icon otherwise", () => {
    expect(runPillHTML(runWord("running", false))).toContain("dot-pulse");
    const failed = runPillHTML(runWord("failed", false));
    expect(failed).toMatch(/^<span class="badge badge-danger badge-pill" data-run="failed">/);
    expect(failed).toContain("#i-x-circle");
    expect(failed).toContain("Failed");
  });

  it("paints each run tone with its badge tone", () => {
    expect(runBadgeTone("info")).toBe("info");
    expect(runBadgeTone("muted")).toBe("muted");
    expect(runBadgeTone("warning")).toBe("warn");
    expect(runBadgeTone("danger")).toBe("danger");
    expect(runBadgeTone("success")).toBe("ok");
  });

  it("takes a tooltip, placement classes and data-* of its own", () => {
    const pill = runPillHTML(runWord("stopped", false), "", {
      title: "Archived",
      cls: "card-kids-word",
      attrs: { "data-kids": "x" },
    });
    expect(pill).toContain('class="badge badge-muted badge-pill card-kids-word"');
    expect(pill).toContain('title="Archived"');
    expect(pill).toContain('data-run="stopped" data-kids="x"');
  });
});

describe("taskPriorityTagHTML", () => {
  it("draws the priority as an inline Tag in its level's tone", () => {
    expect(taskPriorityTagHTML("urgent")).toMatch(/^<span class="tag tag-danger tag-inline">/);
    expect(taskPriorityTagHTML("urgent")).toContain("#i-alert");
    expect(taskPriorityTagHTML("high")).toContain('<span class="tag-label">High</span>');
    expect(taskPriorityTagHTML("low", { pill: true })).toMatch(/^<span class="tag tag-muted">/);
  });

  it("is empty with no priority or one off the scale", () => {
    expect(taskPriorityTagHTML(undefined)).toBe("");
    expect(taskPriorityTagHTML("whenever")).toBe("");
  });
});

describe("taskPriorityTagHTML glyphs", () => {
  it("pairs each priority with its own icon and word", () => {
    expect(taskPriorityTagHTML("urgent")).toContain("#i-alert");
    expect(taskPriorityTagHTML("high")).toContain("#i-arrow-up");
    expect(taskPriorityTagHTML("medium")).toContain("#i-minus");
    expect(taskPriorityTagHTML("low")).toContain("#i-arrow-down");
    expect(taskPriorityTagHTML("high")).toContain("High");
  });
});

function attention(over: Partial<AttentionInput> = {}): AttentionInput {
  return { questions: [], pendingApprovals: [], failingCases: [], unmetHard: [], ...over };
}

describe("attentionItems", () => {
  it("is empty when nothing waits on a person", () => {
    expect(attentionItems(attention())).toEqual([]);
  });

  it("lists a question first, then approvals, failing cases and blockers", () => {
    const items = attentionItems(
      attention({
        questions: [{ id: "q1", status: "open", agent: "backend" }],
        pendingApprovals: [{ id: "a1", label: "Deploy" }],
        failingCases: [{ key: "c1", title: "Checkout" }],
        unmetHard: [{ task_id: "t2", title: "Schema" }],
      }),
    );
    expect(items.map((i) => i.kind)).toEqual(["question", "approval", "test", "blocked"]);
    expect(items[0].text).toBe("backend asked a question");
    expect(items[0].target).toBe("q1");
    expect(items[1].text).toBe("Waiting for your approval: Deploy");
    expect(items[2].tone).toBe("danger");
    expect(items[3].text).toBe("Blocked by Schema");
    expect(items[3].target).toBe("t2");
  });

  it("prefers a blocking question and ignores answered ones", () => {
    const items = attentionItems(
      attention({
        questions: [
          { id: "q0", status: "answered", agent: "a" },
          { id: "q1", status: "open", agent: "a", blocking: false },
          { id: "q2", status: "open", agent: "b" },
        ],
      }),
    );
    expect(items).toHaveLength(1);
    expect(items[0].target).toBe("q2");
  });

  it("says a non-blocking question keeps the run working", () => {
    const items = attentionItems(
      attention({ questions: [{ id: "q1", status: "open", agent: "backend", blocking: false }] }),
    );
    expect(items[0].text).toBe("backend asked a question and keeps working");
  });

  it("says a blocker is blocked, ahead of a plain question", () => {
    const items = attentionItems(
      attention({
        questions: [
          { id: "q1", status: "open", agent: "a" },
          { id: "q2", status: "open", agent: "backend", blocker: true },
        ],
      }),
    );
    expect(items[0].target).toBe("q2");
    expect(items[0].text).toBe("backend is blocked and needs you");
  });

  it("keeps an approval with changes requested in the strip", () => {
    const items = attentionItems(attention({ changesRequested: [{ id: "a1", label: "Design" }] }));
    expect(items.map((i) => i.kind)).toEqual(["approval"]);
    expect(items[0].text).toBe("Changes requested: Design");
    expect(items[0].target).toBe("a1");
  });

  it("counts several approvals, cases and blockers", () => {
    const items = attentionItems(
      attention({
        pendingApprovals: [
          { id: "a1", label: "x" },
          { id: "a2", label: "y" },
        ],
        failingCases: [
          { key: "c1", title: "a" },
          { key: "c2", title: "b" },
        ],
        unmetHard: [
          { task_id: "t1", title: "One" },
          { task_id: "t2", title: "Two" },
          { task_id: "t3", title: "Three" },
        ],
      }),
    );
    expect(items[0].text).toBe("2 approvals waiting");
    expect(items[1].text).toBe("2 test cases failing");
    expect(items[2].text).toBe("Blocked by One and 2 more");
  });

  it("rolls the subtasks that need someone up to the parent", () => {
    const items = attentionItems(
      attention({
        kids: [
          { id: "s1", title: "Field", state: "blocked", blocker: "Schema" },
          { id: "s2", title: "Review", state: "question" },
          { id: "s3", title: "E2E", state: "failed" },
          { id: "s4", title: "Docs", state: "failed" },
        ],
      }),
    );
    expect(items.map((i) => [i.kind, i.target, i.text])).toEqual([
      ["subtask", "s2", "Subtask Review needs an answer"],
      ["subtask", "s3", "2 subtasks failed"],
      ["subtask", "s1", "Field waits for Schema"],
    ]);
    expect(
      attentionItems(attention({ kids: [{ id: "s1", title: "Field", state: "blocked" }] }))[0].text,
    ).toBe("Subtask Field is blocked");
  });

  it("offers the parent's next stage once every subtask is done", () => {
    const sub = { total: 3, done: 3, parentTerminal: false, nextLabel: "Review" };
    const items = attentionItems(attention({ subtasks: sub }));
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "subtasks", tone: "success", action: "Move to Review" });
    expect(items[0].text).toBe("All 3 subtasks are done");
    expect(attentionItems(attention({ subtasks: { ...sub, done: 2 } }))).toEqual([]);
    expect(attentionItems(attention({ subtasks: { ...sub, parentTerminal: true } }))).toEqual([]);
    expect(attentionItems(attention({ subtasks: { ...sub, nextLabel: "" } }))).toEqual([]);
  });
});

describe("primaryAction", () => {
  const base = {
    pendingApprovals: 0,
    children: 0,
    childrenDone: 0,
    terminal: false,
    next: "review",
  };

  it("picks by what the task waits for", () => {
    expect(primaryAction({ ...base, status: "awaiting_input" })).toBe("answer");
    expect(primaryAction({ ...base, status: "awaiting_approval", pendingApprovals: 1 })).toBe(
      "review",
    );
    expect(primaryAction({ ...base, status: "awaiting_approval" })).toBeNull();
    expect(primaryAction({ ...base, status: "out_of_turns" })).toBe("more_turns");
    expect(primaryAction({ ...base, status: "needs_more_turns" })).toBe("more_turns");
  });

  it("answers a run that ended badly by its cause", () => {
    expect(primaryAction({ ...base, status: "failed" })).toBe("retry");
    expect(primaryAction({ ...base, status: "not_started" })).toBe("retry");
    expect(primaryAction({ ...base, status: "blocked" })).toBe("review_retry");
    expect(primaryAction({ ...base, status: "resume_failed" })).toBe("relaunch_stage");
    expect(primaryAction({ ...base, status: "credential_blocked" })).toBe("credentials");
    expect(primaryAction({ ...base, status: "no_agent" })).toBe("pick_agent");
    expect(primaryAction({ ...base, status: "idle", waitCanceled: true })).toBe("relaunch_stage");
    expect(primaryAction({ ...base, status: "running", waitCanceled: true })).toBeNull();
    expect(primaryAction({ ...base, status: "stopped" })).toBeNull();
  });

  it("moves a parent on once its subtasks are done", () => {
    expect(primaryAction({ ...base, children: 2, childrenDone: 2 })).toBe("move_parent");
    expect(primaryAction({ ...base, children: 2, childrenDone: 1 })).toBeNull();
    expect(primaryAction({ ...base, children: 2, childrenDone: 2, terminal: true })).toBeNull();
    expect(primaryAction({ ...base, children: 2, childrenDone: 2, next: "" })).toBeNull();
  });

  it("offers nothing for a running or idle task", () => {
    expect(primaryAction({ ...base, status: "running" })).toBeNull();
    expect(primaryAction(base)).toBeNull();
  });
});

describe("agentHint", () => {
  const flow = [
    { id: "backlog", label: "Backlog" },
    { id: "dev", label: "In progress" },
    { id: "qa", label: "QA" },
  ];

  it("explains an empty agent on a subtask waiting in the initial stage", () => {
    const h = agentHint({
      stage: "backlog",
      assignments: { dev: "frontend" },
      flow,
      isSubtask: true,
      initial: "backlog",
    });
    expect(h).toEqual({
      agent: "",
      line: "None in Backlog. Next: frontend in In progress.",
      replaces: true,
    });
  });

  it("names who comes next, skipping stages without an agent", () => {
    const h = agentHint({
      stage: "backlog",
      assignments: { backlog: "planner", qa: "qa" },
      flow,
      isSubtask: false,
      initial: "backlog",
    });
    expect(h).toEqual({ agent: "planner", line: "Next: qa in QA", replaces: false });
  });

  it("adds nothing at the end of the flow", () => {
    const h = agentHint({
      stage: "qa",
      assignments: { qa: "qa" },
      flow,
      isSubtask: false,
      initial: "backlog",
    });
    expect(h).toEqual({ agent: "qa", line: "", replaces: false });
  });
});

describe("boardCrumbAction", () => {
  const plain = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false };
  it("closes the drawer onto the board behind it, or switches to another board", () => {
    expect(boardCrumbAction(plain, "b1", "b1")).toBe("close");
    expect(boardCrumbAction(plain, "b2", "b1")).toBe("switch");
  });
  it("leaves a click that opens a tab or a window to the browser", () => {
    for (const k of ["metaKey", "ctrlKey", "shiftKey", "altKey"] as const)
      expect(boardCrumbAction({ ...plain, [k]: true }, "b1", "b1")).toBe("browser");
    expect(boardCrumbAction({ ...plain, button: 1 }, "b2", "b1")).toBe("browser");
  });
});

describe("siblingNav", () => {
  it("gives the position and both neighbours", () => {
    expect(siblingNav(["a", "b", "c"], "b")).toEqual({ index: 2, total: 3, prev: "a", next: "c" });
  });

  it("leaves the ends empty", () => {
    expect(siblingNav(["a", "b"], "a")).toEqual({ index: 1, total: 2, prev: "", next: "b" });
    expect(siblingNav(["a", "b"], "b")).toEqual({ index: 2, total: 2, prev: "a", next: "" });
  });

  it("is null for a task outside the list", () => {
    expect(siblingNav(["a"], "z")).toBeNull();
  });
});

describe("foldIndex", () => {
  it("shows four rows of two", () => {
    expect(foldIndex(new Array<boolean>(10).fill(false))).toBe(8);
  });

  it("gives a wide property its own row", () => {
    expect(foldIndex([false, false, true, false, false, true, false])).toBe(6);
  });

  it("closes a half-filled row before a wide property", () => {
    expect(foldIndex([false, true, true, true, true])).toBe(4);
  });

  it("shows everything that fits", () => {
    expect(foldIndex([false, true])).toBe(2);
    expect(foldIndex([])).toBe(0);
  });
});

describe("tab names and the runs line", () => {
  it("names the counts for screen readers", () => {
    expect(approvalsTabName(0)).toBe("Approvals");
    expect(approvalsTabName(2)).toBe("Approvals, 2 pending");
    expect(reportsTabName(0)).toBe("Reports");
    expect(reportsTabName(1)).toBe("Reports, 1 report");
  });

  it("says runs and compact tokens, and nothing before the first run", () => {
    expect(runsLine(0, 0)).toBe("");
    expect(runsLine(1, 900)).toBe("1 run, 900 tokens");
    expect(runsLine(3, 12_400)).toBe("3 runs, 12.4k tokens");
    expect(runsLine(5, 1_250_000)).toBe("5 runs, 1.3M tokens");
  });

  it("adds the cost once there is one, and says when it is an estimate", () => {
    expect(runsLine(2, 900, 0)).toBe("2 runs, 900 tokens");
    expect(runsLine(2, 900, 1.5)).toBe("2 runs, 900 tokens, $1.50");
    expect(runsLine(2, 900, 1.5, 0.5)).toBe("2 runs, 900 tokens, ≈$1.50");
    expect(runsTitle(1.5, 0.5)).toContain("$0.50");
    expect(runsLine(2, 550_400, 0.34)).toBe("2 runs, 550.4k tokens, $0.34");
    expect(runsLine(2, 550_400, 0.001)).toBe("2 runs, 550.4k tokens");
  });

  it("says when runs used a model with no price instead of hiding the cost", () => {
    expect(runsLine(2, 900, 0, 0, 1)).toBe("2 runs, 900 tokens, no price");
    expect(runsLine(2, 900, 1.5, 0, 1)).toBe("2 runs, 900 tokens, $1.50 · 1 without price");
    expect(runsTitle(1.5, 0, 1)).toContain("1 session");
  });
});

describe("reasonLine and causeLink", () => {
  it("keeps the note's last line without marks", () => {
    expect(reasonLine("**Run finished**\nSee [Credentials](/credentials/).")).toBe(
      "See Credentials.",
    );
    expect(reasonLine("")).toBe("");
    expect(reasonLine("x".repeat(300)).length).toBe(200);
  });

  it("keeps identifiers whole and drops only the marks that wrap text", () => {
    expect(
      reasonLine(
        "no LLM credential: add a profile, or set OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 on the daemon",
      ),
    ).toBe(
      "no LLM credential: add a profile, or set OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 on the daemon",
    );
    expect(
      reasonLine("set `OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1` and run_id _now_ in *Settings*"),
    ).toBe("set OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 and run_id now in Settings");
    expect(reasonLine("## Heading\n> quoted **bold** line")).toBe("quoted bold line");
  });

  it("names each unusable credential, not the fix that closes the note", () => {
    const note =
      "Did not start: 2 credentials this task needs cannot be used:\n\n" +
      "- **gh_token** (github) — expired\n  Tools affected: `github`\n" +
      "- **aws-prod** (aws) — rejected\n  Tools affected: `aws`\n" +
      "\nFix or rotate the credential in **Credentials** and relaunch.";
    expect(reasonLine(note)).toBe("gh_token (github) — expired; aws-prod (aws) — rejected");
  });

  it("points each cause at where it is fixed", () => {
    expect(causeLink("llm_credential")?.href).toBe("/settings/#llm");
    expect(causeLink("llm_credential")?.label).toBe("Open AI engines");
    expect(causeLink("credential")?.href).toBe("/credentials/");
    expect(causeLink("agent_missing")).toBeNull();
    for (const c of ["no_agent", "agent_missing", "agent_invalid"])
      expect(causePicksAgent(c)).toBe(true);
    expect(causePicksAgent("credential")).toBe(false);
    expect(causeLink("quota")).toBeNull();
    expect(quotaLink().href).toBe("/infra/#workspaces");
    expect(causeLink("runtime")).toBeNull();
    expect(causeLink(undefined)).toBeNull();
  });
});

describe("waitCanceled", () => {
  const ev = (kind: string, at: string) => ({ kind, created_at: `2026-10-01T00:00:0${at}Z` });
  it("is true while the canceled wait is the stage's newest run event", () => {
    expect(waitCanceled([ev("run_queued", "1"), ev("run_queue_canceled", "2")])).toBe(true);
    expect(
      waitCanceled([
        ev("run_queued", "1"),
        ev("run_queue_canceled", "2"),
        ev("question_asked", "3"),
      ]),
    ).toBe(true);
  });
  it("is false once a run started again or the task moved", () => {
    expect(waitCanceled([ev("run_queue_canceled", "2"), ev("agent_run_started", "3")])).toBe(false);
    expect(waitCanceled([ev("run_queue_canceled", "2"), ev("stage_entered", "4")])).toBe(false);
    expect(waitCanceled([])).toBe(false);
  });
});

describe("attentionRowHTML", () => {
  const item = {
    kind: "question" as const,
    tone: "warning" as const,
    icon: "i-help",
    text: "Which <coupon>?",
    age: "4 min ago",
    action: "Answer",
    target: "q1",
  };

  it("draws a band callout in the item's tone", () => {
    const html = attentionRowHTML(item);
    expect(html).toMatch(
      /^<div class="callout callout-warn callout-band" data-attn-row="question">/,
    );
    expect(html).toContain('<span class="attn-text">Which &lt;coupon&gt;?</span>');
    expect(html).toContain('<span class="attn-age">4 min ago</span>');
    expect(html).toContain('data-attn="question" data-target="q1"');
  });

  it("maps every tone and writes the extra markup after the button", () => {
    expect(attentionRowHTML({ ...item, tone: "danger" })).toContain("callout-danger");
    const ok = attentionRowHTML({ ...item, tone: "success", age: "" }, "<b>more</b>");
    expect(ok).toContain("callout-ok");
    expect(ok).not.toContain("attn-age");
    expect(ok).toMatch(/<\/button><b>more<\/b><\/div><\/div>$/);
  });
});

describe("orphanAgentsHTML", () => {
  it("writes nothing without orphans", () => {
    expect(orphanAgentsHTML(undefined)).toBe("");
    expect(orphanAgentsHTML([])).toBe("");
  });

  it("names each orphan, warns, and offers Remove on its container", () => {
    const html = orphanAgentsHTML([
      {
        container_id: "c1",
        name: "developer-task-t1-in_progress-2",
        agent: "developer",
        started_at: new Date(Date.now() - 10 * 60_000).toISOString(),
      },
      { container_id: "c2", name: "tester-task-t1-qa-3" },
    ]);
    expect(html).toContain("badge-warn");
    expect(html).toContain(t("tasks.drawer.props.orphan_badge"));
    expect(html).toContain('data-orphan-remove="c1"');
    expect(html).toContain('data-orphan-remove="c2"');
    expect(html).toContain(
      t("tasks.drawer.props.orphan_remove_named", { name: "developer-task-t1-in_progress-2" }),
    );
    expect(html).toContain(
      t("tasks.drawer.props.orphan_line_untimed", { agent: "tester-task-t1-qa-3" }),
    );
    expect(html).toContain("developer");
  });
});

describe("cancelWaitQuestion", () => {
  it("names the run it cancels and the way back", () => {
    const q = cancelWaitQuestion("coder", "Review");
    expect(q.title).toBe(t("tasks.comments.cancel_wait_title", { agent: "coder" }));
    expect(q.message).toContain("coder");
    expect(q.message).toContain("Review");
    expect(q.message).toContain(t("tasks.drawer.header.relaunch_stage"));
    expect(q.cancelLabel).toBe(t("tasks.comments.keep_waiting"));
  });

  it("still says what it cancels without an agent or a stage", () => {
    const q = cancelWaitQuestion("", "");
    expect(q.title).toBe(t("tasks.comments.cancel_wait_title_unnamed"));
    expect(q.message).toContain(t("tasks.drawer.header.relaunch_stage"));
  });
});

describe("orphanSummary", () => {
  it("counts the orphans for the phone's summary line, and says nothing without", () => {
    expect(orphanSummary(undefined)).toBe("");
    expect(orphanSummary([{ container_id: "c", name: "n" }])).toBe(
      t("tasks.drawer.props.summary_orphans", { count: 1 }),
    );
  });
});
