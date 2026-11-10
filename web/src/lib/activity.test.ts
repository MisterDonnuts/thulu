import { describe, expect, it } from "vitest";
import {
  answersMentions,
  applyTarget,
  audienceLine,
  authorLabel,
  viewerFor,
  buildStream,
  firstLine,
  foldStream,
  groupSummary,
  groupSystem,
  initials,
  isNoteLine,
  mentionableAgents,
  mentionsIn,
  noteLink,
  openThreadCount,
  parseFilter,
  prOpenedText,
  prRef,
  runsFromEvents,
  stoppedText,
  subtaskTitle,
  threadEventText,
  turnsText,
  type StreamComment,
  type StreamEvent,
  type StreamItem,
} from "./activity";
import {
  applyFormHTML,
  excerpt,
  filterBarHTML,
  metaHTML,
  questionWhoText,
  runCardHTML,
  resolvedLineHTML,
  systemLineHTML,
  threadActionsHTML,
  threadNoteHTML,
  type ActivityCtx,
  sourceTagHTML,
} from "./activity-html";

const at = (min: number) => new Date(Date.UTC(2026, 9, 15, 12, min)).toISOString();

const started = (inst: string, min: number, extra: Record<string, unknown> = {}): StreamEvent => ({
  id: `s-${inst}`,
  kind: "agent_run_started",
  created_at: at(min),
  payload: {
    instance_id: inst,
    agent: "coder",
    stage: "build",
    container_id: `c-${inst}`,
    ...extra,
  },
});
const finished = (inst: string, min: number, exit: number): StreamEvent => ({
  id: `f-${inst}`,
  kind: "agent_run_finished",
  created_at: at(min),
  payload: { instance_id: inst, agent: "coder", stage: "build", exit },
});
const moved = (id: string, min: number): StreamEvent => ({
  id,
  kind: "stage_entered",
  created_at: at(min),
  payload: { from: "plan", to: "build" },
});
const comment = (id: string, min: number, extra: Partial<StreamComment> = {}): StreamComment => ({
  id,
  created_at: at(min),
  body: `comment ${id}`,
  ...extra,
});

describe("parseFilter", () => {
  it("keeps a known filter and falls back to all", () => {
    expect(parseFilter("runs")).toBe("runs");
    expect(parseFilter("nope")).toBe("all");
    expect(parseFilter(null)).toBe("all");
  });
});

describe("runsFromEvents", () => {
  it("numbers runs and reads their end", () => {
    const runs = runsFromEvents([
      started("a", 0),
      finished("a", 5, 0),
      started("b", 6),
      finished("b", 9, 2),
    ]);
    expect(runs.map((r) => [r.id, r.number, r.state, r.exit])).toEqual([
      ["a", 1, "done", 0],
      ["b", 2, "failed", 2],
    ]);
    expect(runs[0].containerID).toBe("c-a");
  });

  it("calls the newest run without an end running only while the task runs", () => {
    const evs = [started("a", 0), finished("a", 5, 0), started("b", 6)];
    expect(runsFromEvents(evs, { alive: true })[1].state).toBe("running");
    expect(runsFromEvents(evs, { status: "idle" })[1].state).toBe("ended");
  });

  it("adds the queued run while the task waits", () => {
    const q: StreamEvent = {
      id: "q1",
      kind: "run_queued",
      created_at: at(10),
      payload: { agent: "coder", stage: "build", reason: "workspace quota" },
    };
    const runs = runsFromEvents([started("a", 0), finished("a", 5, 0), q], { status: "queued" });
    expect(runs.at(-1)).toMatchObject({ state: "queued", number: 0, reason: "workspace quota" });
    expect(runsFromEvents([started("a", 0), q], { status: "running" })).toHaveLength(1);
  });
});

describe("buildStream", () => {
  const events = [
    started("a", 1),
    finished("a", 3, 0),
    moved("m1", 4),
    moved("m2", 5),
    started("b", 6),
    finished("b", 8, 0),
  ];
  const runs = runsFromEvents(events);
  const comments = [comment("c1", 2), comment("r1", 7, { parent_id: "c1" }), comment("c2", 9)];

  it("merges oldest first, folds clean runs into system lines except the newest", () => {
    const items = buildStream({ comments, events, runs, filter: "all" });
    expect(items.map((i) => i.type)).toEqual(["thread", "group", "run", "thread"]);
    const group = items[1] as Extract<StreamItem, { type: "group" }>;
    expect(group.entries.map((e) => e.kind)).toEqual(["run", "stage", "stage"]);
  });

  it("shows every run as a card under Agent runs", () => {
    const items = buildStream({ comments, events, runs, filter: "runs" });
    expect(items.map((i) => i.type)).toEqual(["run", "run"]);
  });

  it("keeps comments only under Comments and system lines ungrouped under System", () => {
    expect(buildStream({ comments, events, runs, filter: "comments" }).map((i) => i.id)).toEqual([
      "c1",
      "c2",
    ]);
    const sys = buildStream({ comments, events, runs, filter: "system" });
    expect(sys.every((i) => i.type === "system")).toBe(true);
    expect(sys.map((i) => i.id)).toEqual(["run-a", "ev-m1", "ev-m2", "run-b"]);
  });

  it("lists subtasks added", () => {
    const items = buildStream({
      comments: [],
      events: [],
      runs: [],
      children: [{ id: "k1", title: "Child", created_at: at(3) }],
      filter: "system",
    });
    expect(items[0]).toMatchObject({ type: "system", entry: { kind: "subtask" } });
  });
});

describe("groupSystem", () => {
  it("groups two or more system lines of one day and leaves single ones", () => {
    const sys = (id: string, iso: string): StreamItem => ({
      type: "system",
      id,
      at: iso,
      entry: { id, at: iso, kind: "stage" },
    });
    const other = "2026-10-16T12:00:00.000Z";
    const out = groupSystem([sys("a", at(1)), sys("b", at(2)), sys("c", other)]);
    expect(out.map((i) => i.type)).toEqual(["group", "system"]);
  });
});

describe("what the last run left the task waiting on", () => {
  const verdict = (id: string, min: number, v: string): StreamEvent => ({
    id,
    kind: "auditor_verdict",
    created_at: at(min),
    payload: { verdict: v, stage: "build" },
  });
  const note = (id: string, min: number, key: string): StreamComment => ({
    id,
    created_at: at(min),
    source: "system",
    key,
    body: `note ${key}`,
  });

  it("keeps the auditor's block note out of any group, and says the verdict once", () => {
    const events = [
      started("a", 1),
      finished("a", 3, 0),
      moved("m1", 4),
      verdict("v1", 5, "block"),
    ];
    const items = buildStream({
      comments: [],
      events,
      runs: runsFromEvents(events),
      notes: [note("n1", 5, "task.comments.auditor_block"), note("n2", 6, "task.comments.other")],
      filter: "all",
    });
    expect(items.map((i) => i.type)).toEqual(["run", "system", "system", "system"]);
    expect(items.map((i) => i.id)).not.toContain("ev-v1");
    expect(items.map((i) => i.id)).toContain("note-n1");
  });

  it("says a kick-back once: the note, without the verdict and the move", () => {
    const back: StreamEvent = {
      id: "kb",
      kind: "stage_entered",
      created_at: at(6),
      payload: { from: "review", to: "build", via: "kick_back" },
    };
    const events = [started("a", 1), finished("a", 3, 0), verdict("v1", 5, "kick_back"), back];
    const ids = buildStream({
      comments: [],
      events,
      runs: runsFromEvents(events),
      notes: [note("n1", 6, "task.comments.auditor_kick_back")],
      filter: "all",
    }).map((i) => i.id);
    expect(ids).toEqual(["run-a", "note-n1"]);
  });

  it("keeps a verdict to continue, which has no note", () => {
    const events = [started("a", 1), finished("a", 3, 0), verdict("v1", 5, "continue")];
    const ids = buildStream({
      comments: [],
      events,
      runs: runsFromEvents(events),
      filter: "all",
    }).map((i) => i.id);
    expect(ids).toContain("ev-v1");
  });
});

describe("foldStream", () => {
  const items = Array.from({ length: 15 }, (_, i) => ({ type: "thread", open: i === 0 }));

  it("keeps the newest 12 and every open thread", () => {
    const { shown, hidden } = foldStream(items, false);
    expect(hidden).toBe(2);
    expect(shown).toHaveLength(13);
    expect(shown[0].open).toBe(true);
  });

  it("shows everything once expanded or when short", () => {
    expect(foldStream(items, true).hidden).toBe(0);
    expect(foldStream(items.slice(0, 5), false).shown).toHaveLength(5);
  });
});

describe("words", () => {
  it("summarizes a group", () => {
    expect(
      groupSummary([
        { id: "1", at: at(1), kind: "stage" },
        { id: "2", at: at(2), kind: "stage" },
        { id: "3", at: at(3), kind: "stage" },
        { id: "4", at: at(4), kind: "run" },
        { id: "5", at: at(5), kind: "run" },
      ]),
    ).toBe("3 stage moves and 2 runs");
  });

  it("names authors without the bare user", () => {
    expect(authorLabel({ author: "user", source: "user" })).toBe("Someone");
    expect(
      authorLabel({ author: "ana@example.com", source: "user" }, { email: "ana@example.com" }),
    ).toBe("You");
    expect(authorLabel({ author: "", source: "agent" })).toBe("An agent");
    expect(authorLabel({ author: "coder", source: "agent" })).toBe("coder");
    expect(authorLabel({ author: "automation", source: "automation" })).toBe("An automation");
    expect(authorLabel({ author: "", source: "automation" })).toBe("An automation");
  });

  it("reads the dev-mode operator's comments as the viewer's", () => {
    const dev = viewerFor(null, true);
    expect(authorLabel({ author: "user", source: "user" }, dev)).toBe("You");
    expect(viewerFor(null, false)).toBeNull();
    expect(viewerFor({ email: "ana@example.com" }, true)).toEqual({ email: "ana@example.com" });
  });

  it("takes initials and first lines", () => {
    expect(initials("Ana María")).toBe("AM");
    expect(initials("marta@example.com")).toBe("M");
    expect(firstLine("\n## Fix the *login* page\nmore")).toBe("Fix the login page");
  });

  it("titles a subtask without the label that opens a list", () => {
    expect(subtaskTitle("I traced the totals path end to end. Plan:\n1. one")).toBe(
      "I traced the totals path end to end",
    );
    expect(subtaskTitle("Steps:\n- a")).toBe("Steps");
    expect(subtaskTitle("Round the discount down.")).toBe("Round the discount down");
    expect(subtaskTitle("What about 0% VAT?")).toBe("What about 0% VAT?");
  });

  it("finds mentions among known handles", () => {
    expect(mentionsIn("hey @Coder and @nobody, @coder again", ["coder", "qa"])).toEqual(["coder"]);
  });

  it("says who reads a comment", () => {
    const handles = ["coder", "qa"];
    expect(audienceLine({ text: "hi", handles })).toBe("Visible to the workspace.");
    expect(audienceLine({ text: "@qa check", handles })).toContain("@qa replies to it");
    expect(audienceLine({ text: "ok", handles, fallback: "coder" })).toContain("@coder replies");
    expect(audienceLine({ text: "ok", handles, notify: { agent: "coder", on: true } })).toContain(
      "coder",
    );
  });
});

describe("openThreadCount", () => {
  it("counts threads with replies nobody resolved", () => {
    const cs = [
      comment("a", 1),
      comment("a1", 2, { parent_id: "a" }),
      comment("b", 3, { resolved_at: at(5) }),
      comment("b1", 4, { parent_id: "b" }),
      comment("c", 5),
    ];
    expect(openThreadCount(cs)).toBe(1);
  });
});

describe("activity markup", () => {
  const ctx: ActivityCtx = {
    now: Date.parse(at(20)),
    stageLabel: (s) => s.toUpperCase(),
    eventLabel: (k) => k,
    liveAction: () => "Read main.go",
  };

  it("draws a running card with Stop and the live action", () => {
    const [run] = runsFromEvents([started("a", 10)], { alive: true });
    const html = runCardHTML(run, ctx);
    expect(html).toContain("Run 1: coder in BUILD");
    expect(html).toContain('data-run-stop="a"');
    expect(html).toContain("Read main.go");
    expect(html).toContain("data-running-since");
  });

  it("draws a failed card with its exit code and no Stop", () => {
    const [run] = runsFromEvents([started("a", 1), finished("a", 2, 3)]);
    const html = runCardHTML(run, ctx, "boom");
    expect(html).toContain("Exit code 3. boom");
    expect(html).not.toContain("data-run-stop");
  });

  it("offers Send to only on an open thread with replies", () => {
    expect(
      threadActionsHTML({ id: "t", replies: 0, resolved: false, agent: "coder", canSubtask: true }),
    ).not.toContain("data-thread-apply");
    const open = threadActionsHTML({
      id: "t",
      replies: 2,
      resolved: false,
      agent: "coder",
      canSubtask: true,
    });
    expect(open).toContain("Send to coder");
    expect(open).toContain('title="Send this thread to coder to carry out"');
    const never = threadActionsHTML({
      id: "t",
      replies: 2,
      resolved: false,
      agent: "",
      canSubtask: true,
    });
    expect(never).toContain('aria-disabled="true"');
    expect(never).toContain("has not run yet");
    const assigned = threadActionsHTML({
      id: "t",
      replies: 2,
      resolved: false,
      agent: "",
      stageAgent: "desarrollador",
      canSubtask: true,
    });
    expect(assigned).toContain('aria-disabled="true"');
    expect(assigned).toContain(">Send to desarrollador<");
    expect(assigned).toContain("has not run yet");
    const gone = threadActionsHTML({
      id: "t",
      replies: 2,
      resolved: false,
      agent: "",
      goneAgent: "backend",
      canSubtask: true,
    });
    expect(gone).toContain('aria-disabled="true"');
    expect(gone).toContain("backend, the agent that worked on this task, no longer exists");
    expect(gone).not.toContain("has not run yet");
    expect(
      threadActionsHTML({ id: "t", replies: 2, resolved: true, agent: "coder", canSubtask: true }),
    ).toContain("Reopen");
  });

  it("folds a resolved thread to one escaped line", () => {
    const html = resolvedLineHTML(
      {
        root: comment("r", 1, { body: "<b>Fix</b> it", resolved_at: at(4) }),
        replies: [comment("x", 2)],
      },
      "ana",
      ctx,
    );
    expect(html).toContain('data-thread-show="r"');
    expect(html).not.toContain("<b>");
    expect(html).toContain("by ana, 1 reply");
    expect(html).toMatch(/^<div class="row row-sm row-dim act-resolved" data-thread="r">/);
    expect(html).toContain('<span class="row-aside"><time class="act-time"');
  });

  it("links the address preview_url handed out, and only a web one", () => {
    const line = (url: string) =>
      systemLineHTML(
        {
          id: "e",
          at: at(5),
          kind: "other",
          event: { id: "e", kind: "preview_url", created_at: at(5), payload: { port: 3000, url } },
        },
        ctx,
      );
    expect(line("http://host:41234")).toContain('href="http://host:41234"');
    expect(line("javascript:alert(1)")).not.toContain("act-sys-link");
    expect(line("javascript:alert(1)")).not.toContain("javascript:");
  });
});

describe("runs that ended without finishing", () => {
  const ctx: ActivityCtx = {
    now: Date.parse(at(20)),
    stageLabel: (s) => s.toUpperCase(),
    eventLabel: (k) => k,
    viewer: { name: "ana" },
  };
  const notStarted: StreamEvent = {
    id: "ns1",
    kind: "run_not_started",
    created_at: at(5),
    payload: {
      stage: "build",
      agent: "coder",
      cause: "llm_credential",
      reason: "falta una credencial",
      reason_i18n: { en: "No model credential for claude-code", es: "falta una credencial" },
    },
  };

  it("draws a run that never started with its reason and where to fix it", () => {
    const runs = runsFromEvents([started("a", 1), finished("a", 2, 0), notStarted]);
    expect(runs.map((r) => [r.number, r.state])).toEqual([
      [1, "done"],
      [0, "not_started"],
    ]);
    const html = runCardHTML(runs[1], ctx);
    expect(html).toContain("Did not start");
    expect(html).toContain("No model credential for claude-code");
    expect(html).toMatch(/<div class="callout callout-danger">.*No model credential/s);
    expect(html).toContain('href="/settings/#llm"');
    expect(html).not.toContain("data-run-log");
    expect(html).not.toContain("data-run-chat");
  });

  it("names a run without an agent by its stage, and opens the Agent field to fix it", () => {
    const noAgent = { ...notStarted, payload: { stage: "build", agent: "", cause: "no_agent" } };
    const run = runsFromEvents([noAgent])[0];
    const html = runCardHTML(run, ctx);
    expect(html).toContain("No agent in BUILD");
    expect(html).not.toContain("the agent in");
    expect(html).toMatch(/badge badge-danger badge-pill" data-run-state="not_started">.*No agent</);
    expect(html).toContain("data-pick-agent");
    expect(html).not.toContain('href="/agents/"');
    expect(runCardHTML(run, { ...ctx, fixedAbove: "no_agent" })).not.toContain("data-pick-agent");
  });

  it("calls a credential refusal what the header calls it, and leaves the link to the header", () => {
    const cred = {
      ...notStarted,
      payload: { stage: "build", agent: "coder", cause: "credential" },
    };
    const run = runsFromEvents([cred])[0];
    expect(runCardHTML(run, ctx)).toContain("Credential expired");
    expect(runCardHTML(run, ctx)).toContain('href="/credentials/"');
    expect(runCardHTML(run, { ...ctx, fixedAbove: "credential" })).not.toContain("/credentials/");
  });

  it("reads turns, a stop and who stopped it", () => {
    const out = runsFromEvents([
      started("a", 1, { max_turns: "30" }),
      {
        ...finished("a", 2, 1),
        payload: { instance_id: "a", stage: "build", exit: 1, outcome: "max_turns", turns: 30 },
      },
    ])[0];
    expect(out.state).toBe("out_of_turns");
    expect(turnsText(out)).toBe("30 of 30 turns");
    const card = runCardHTML(out, ctx);
    expect(card).toContain("Out of turns");
    expect(card).toContain("30 of 30 turns");
    expect(card).not.toContain("Exit code");

    const stopped = runsFromEvents([
      started("b", 1),
      { ...finished("b", 2, 143), payload: { instance_id: "b", exit: 143, stopped_by: "ana" } },
    ])[0];
    expect(stopped.state).toBe("stopped");
    expect(stoppedText(stopped, { name: "ana" })).toBe("Stopped by you");
    expect(stoppedText(stopped, { name: "luis" })).toBe("Stopped by ana");
    expect(runCardHTML(stopped, ctx)).toContain("Stopped by you");
  });

  it("gives a queued run Cancel wait, its reason, a quota link and no Open chat", () => {
    const [q] = runsFromEvents(
      [
        {
          id: "q1",
          kind: "run_queued",
          created_at: at(3),
          payload: {
            stage: "build",
            agent: "coder",
            wait: "room",
            reason: "x",
            reason_i18n: { en: "The workspace has 5.5 of 6 CPU reserved" },
          },
        },
      ],
      { status: "queued" },
    );
    const html = runCardHTML(q, ctx);
    expect(html).toContain("The workspace has 5.5 of 6 CPU reserved");
    expect(html).toContain('data-run-cancel="q1"');
    expect(html).toContain('href="/infra/#workspaces"');
    expect(html).not.toContain("data-run-chat");
  });
});

describe("notes in the stream", () => {
  const ctx: ActivityCtx = {
    now: Date.parse(at(20)),
    stageLabel: (s) => s,
    eventLabel: (k) => k,
  };

  it("keeps the notes a run card says and shows the rest", () => {
    expect(isNoteLine({ source: "system", key: "task.comments.run_finished" })).toBe(false);
    expect(isNoteLine({ source: "system", key: "task.comments.auditor_kick_back" })).toBe(true);
    expect(isNoteLine({ source: "system" })).toBe(false);
    expect(isNoteLine({ source: "agent", key: "x" })).toBe(false);
  });

  it("draws a note as a system line in the stream", () => {
    const note = comment("n1", 4, {
      source: "system",
      key: "task.comments.auditor_block",
      body: "The **auditor** stopped the task.",
    });
    const items = buildStream({
      comments: [],
      events: [],
      runs: [],
      notes: [note],
      filter: "system",
    });
    expect(items).toHaveLength(1);
    const it0 = items[0] as Extract<StreamItem, { type: "system" }>;
    expect(it0.entry.kind).toBe("note");
    expect(systemLineHTML(it0.entry, ctx)).toContain("The auditor stopped the task.");
    expect(
      buildStream({ comments: [], events: [], runs: [], notes: [note], filter: "comments" }),
    ).toHaveLength(0);
  });

  it("draws a failed mention inside its thread with where to fix it", () => {
    const html = threadNoteHTML(
      comment("m1", 5, {
        source: "system",
        parent_id: "r",
        key: "task.comments.mention_launch_no_credential",
        body: "@reviewer could not reply: no credential",
      }),
      ctx,
    );
    expect(html).toContain("@reviewer could not reply");
    expect(html).toContain('href="/settings/#llm"');
    expect(noteLink("task.comments.run_engine_auth_failed")?.href).toBe("/settings/#llm");
    expect(noteLink("task.comments.launch_blocked")?.href).toBe("/credentials/");
    expect(noteLink("api.tasks.comments.agent_turn_limit")?.href).toBe("/agents/");
    expect(noteLink("task.comments.auditor_block")).toBeNull();
  });

  it("says who opened a pull request and names it", () => {
    expect(prOpenedText({ source: "agent" })).toBe("The agent opened a pull request");
    expect(prOpenedText({ trigger: "force_pr" })).toBe("Force PR opened a pull request");
    expect(prRef("https://github.com/acme/conduit-api/pull/42")).toBe("conduit-api#42");
    const line = systemLineHTML(
      {
        id: "e",
        at: at(3),
        kind: "other",
        event: {
          id: "e",
          kind: "pr_opened",
          created_at: at(3),
          payload: { source: "agent", url: "https://github.com/acme/api/pull/7" },
        },
      },
      ctx,
    );
    expect(line).toContain("The agent opened a pull request");
    expect(line).toContain(">api#7<");
  });
});

describe("metaHTML", () => {
  const ctx: ActivityCtx = {
    now: Date.parse(at(30)),
    stageLabel: (s) => (s === "dev" ? "Development" : s),
    eventLabel: (k) => k,
  };

  it("writes the avatar family's author line beside the avatar", () => {
    const html = metaHTML({ name: "dev", kind: "agent", at: at(10), stage: "dev" }, ctx);
    expect(html).toMatch(/^<div class="act-meta"><div class="author-line">/);
    expect(html).toContain('<span class="author-line-name">dev</span>');
    expect(html).toContain('<span class="author-line-kind">agent</span>');
    expect(html).toContain(
      `<time class="author-line-time" datetime="${at(10)}" data-ts="${at(10)}"`,
    );
    expect(html).toContain('<span class="author-line-in">in Development</span>');
    expect(html).not.toContain("avatar");
  });

  it("leaves out the agent word and the stage when they do not apply", () => {
    const html = metaHTML({ name: "Ana", kind: "person", at: at(10) }, ctx);
    expect(html).not.toContain("author-line-kind");
    expect(html).not.toContain("author-line-in");
  });
});

describe("systemLineHTML", () => {
  const ctx: ActivityCtx = { now: Date.parse(at(30)), stageLabel: (s) => s, eventLabel: (k) => k };
  const merged = (payload: Record<string, unknown>): StreamEvent => ({
    id: "m1",
    kind: "branch_merged",
    actor: "ana",
    summary: "ana merged task/x into main of api (abc1234)",
    created_at: at(10),
    payload,
  });

  it("words a merge from its payload, not the stored summary", () => {
    const html = systemLineHTML(
      {
        id: "m1",
        at: at(10),
        kind: "other",
        event: merged({ branch: "task/x", target: "main", commit: "abc1234def" }),
      },
      ctx,
    );
    expect(html).toContain("task/x");
    expect(html).toContain("abc1234");
    expect(html).not.toContain("abc1234def");
    expect(html).not.toContain("of api");
    expect(html).toContain("ana");
  });

  it("falls back to the summary when the payload says nothing", () => {
    const html = systemLineHTML({ id: "m1", at: at(10), kind: "other", event: merged({}) }, ctx);
    expect(html).toContain("of api");
  });
});

describe("mentions", () => {
  it("offers the agents the daemon lets answer a mention", () => {
    expect(answersMentions({ name: "a" })).toBe(true);
    expect(answersMentions({ name: "a", valid_stages: ["mention"] })).toBe(true);
    expect(answersMentions({ name: "a", valid_stages: ["chat"] })).toBe(true);
    expect(answersMentions({ name: "a", valid_stages: ["build"] })).toBe(false);
    const list = mentionableAgents([
      { name: "coder", valid_stages: ["build"] },
      { name: "architect", valid_stages: ["mention", "plan"] },
    ]);
    expect(list.map((a) => a.name)).toEqual(["architect"]);
  });
});

describe("applyTarget", () => {
  it("names the live run's agent, else the session's, else nobody", () => {
    const base = { stage: "build", stage_assignments: { build: "coder" } };
    expect(applyTarget({ ...base, run_alive: true }).agent).toBe("coder");
    expect(applyTarget({ ...base, session_agent: "architect" }).agent).toBe("architect");
    expect(applyTarget(base)).toEqual({ agent: "", gone: "" });
  });

  it("names a deleted session agent instead of offering it", () => {
    const task = { stage: "build", session_agent: "backend" };
    const exists = (name: string) => name === "architect";
    expect(applyTarget(task, exists)).toEqual({ agent: "", gone: "backend" });
    expect(applyTarget({ ...task, session_agent: "architect" }, exists)).toEqual({
      agent: "architect",
      gone: "",
    });
    expect(applyTarget(task).agent).toBe("backend");
    expect(
      applyTarget({ ...task, run_alive: true, stage_assignments: { build: "coder" } }, exists)
        .agent,
    ).toBe("coder");
  });
});

describe("threadEventText", () => {
  const title = (id: string) => (id === "r1" ? "backoff?" : "");
  const ev = (kind: string, payload: Record<string, unknown>, actor = "ana"): StreamEvent => ({
    id: "e",
    kind,
    created_at: at(1),
    actor,
    payload,
  });

  it("says who resolved or reopened which thread", () => {
    expect(threadEventText(ev("thread_resolved", { thread_id: "r1", resolved: true }), title)).toBe(
      'ana resolved the thread "backoff?"',
    );
    expect(
      threadEventText(ev("thread_resolved", { thread_id: "gone", resolved: false }), title),
    ).toBe("ana reopened a thread");
  });

  it("says where a sent thread went", () => {
    expect(
      threadEventText(
        ev("thread_applied", { thread_id: "r1", agent: "architect", delivery: "unreachable" }),
        title,
      ),
    ).toBe('Sent the thread "backoff?" to architect: there was no session to resume');
    expect(threadEventText(ev("thread_applied", { thread_id: "r1", agent: "" }), title)).toContain(
      "there was no session",
    );
  });

  it("reads mentions and leaves other kinds alone", () => {
    expect(
      threadEventText(
        ev("mention_finished", { thread_id: "r1", posted: false }, "agent:qa"),
        title,
      ),
    ).toBe('qa finished without answering in the thread "backoff?"');
    expect(threadEventText(ev("stage_entered", {}), title)).toBe("");
  });
});

describe("excerpt", () => {
  it("drops emphasis but keeps underscores inside words", () => {
    expect(excerpt("rename _payment_events_ to **audit_log**")).toBe(
      "rename payment_events to audit_log",
    );
  });
});

describe("apply form and pinned question", () => {
  it("draws the note box with Send to", () => {
    const html = applyFormHTML("r1", "coder", "<x>");
    expect(html).toContain('data-thread-apply-send="r1"');
    expect(html).toContain("&lt;x&gt;");
    expect(html).toContain("Send to coder");
  });

  it("is a thread composer that keeps the page's hooks", () => {
    const html = applyFormHTML("r1", "coder", "fix it");
    expect(html).toMatch(
      /^<div class="composer composer-thread thread-apply" data-thread-apply-form="r1">/,
    );
    expect(html).toMatch(
      /<textarea class="composer-input thread-apply-note" rows="2" placeholder="[^"]+" aria-label="[^"]+">fix it<\/textarea>/,
    );
    expect(html).toContain('data-thread-apply-cancel="r1"');
    expect(html).toMatch(/class="btn btn-primary btn-sm composer-submit"/);
    expect(html).toMatch(/<span class="composer-audience">[^<]*coder[^<]*<\/span>/);
  });

  it("says a non-blocking question keeps the agent working", () => {
    expect(questionWhoText("backend", false)).toBe("backend asked and keeps working");
    expect(questionWhoText("backend", true)).not.toContain("keeps working");
  });
});

describe("sourceTagHTML", () => {
  it("tints the tag by who wrote it", () => {
    expect(sourceTagHTML("agent", "Agent")).toBe(
      '<span class="tag tag-sm tag-special"><span class="tag-label">Agent</span></span>',
    );
    expect(sourceTagHTML("auditor", "Auditor")).toContain("tag-warn");
    expect(sourceTagHTML("system", "System")).toContain("tag-muted");
    expect(sourceTagHTML("user", "<you>")).toBe(
      '<span class="tag tag-sm"><span class="tag-label">&lt;you&gt;</span></span>',
    );
  });
});

describe("filterBarHTML", () => {
  it("draws the filter as a small segmented group with the current one chosen", () => {
    const html = filterBarHTML("runs", "Show");
    expect(html).toMatch(
      /^<div class="segmented segmented-sm" id="act-filter" role="radiogroup" aria-label="Show">/,
    );
    const values = Array.from(html.matchAll(/name="act-filter" value="(\w+)"/g), (m) => m[1]);
    expect(values).toEqual(["all", "comments", "runs", "system"]);
    expect(html.match(/ checked/g)).toHaveLength(1);
    expect(html).toMatch(/value="runs" checked/);
  });
});
