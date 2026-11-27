import { describe, expect, it } from "vitest";

import { t } from "../i18n";
import {
  AGENT_GREY,
  LEGACY_PAUSE_KEY,
  agentColor,
  agentMark,
  answerReplyID,
  cmAvatarHTML,
  highlightMentions,
  cacheCommentsPref,
  commentKind,
  commentThreads,
  conversation,
  countByFilter,
  decisionLabel,
  filterOf,
  hasVisit,
  isLongBody,
  kindTagHTML,
  listedThreads,
  parseCommentFilter,
  parseCommentsPref,
  pauseHTML,
  pauseText,
  pendingThreadIDs,
  pinnedThreads,
  rankMentions,
  readCachedCommentsPref,
  waitingQuestions,
  type KindedComment,
} from "./comments";

const c = (over: Partial<KindedComment> & { id: string }): KindedComment => ({
  source: "user",
  author: "ana",
  body: "",
  created_at: "2026-11-24T10:00:00Z",
  ...over,
});

const stageLabel = (id: string) => ({ testing: "Testing", in_progress: "Fix local" })[id] ?? id;

// One task as the daemon returns it after W1: a stop with a decision answering it, an approval
// thread, a question, a plain conversation with a pause, and the flow's own notes.
const comments: KindedComment[] = [
  c({
    id: "run-note",
    source: "system",
    key: "api.tasks.comments.run_failed",
    body: "Run 2 failed",
  }),
  c({
    id: "stop",
    source: "agent",
    author: "thulu-evidence",
    kind: "block",
    body: "TC-3 needs a real Google account.",
    meta: { report_id: "r1", run_id: "run-2" },
    created_at: "2026-11-24T10:01:00Z",
  }),
  c({
    id: "dec",
    parent_id: "stop",
    kind: "decision",
    body: "Use the staging account.",
    meta: { action: "access", cases: ["TC-3"], secrets: ["GOOGLE_TEST"] },
    created_at: "2026-11-24T10:05:00Z",
  }),
  c({
    id: "appr",
    source: "system",
    author: "octopus",
    kind: "approval",
    key: "task.approvals.comments.requested",
    body: "Approval requested: push and PR",
    meta: { approval_id: "a1" },
    created_at: "2026-11-24T10:02:00Z",
  }),
  c({
    id: "ask",
    source: "agent",
    author: "dev",
    kind: "question",
    body: "Which env?",
    created_at: "2026-11-24T10:03:00Z",
  }),
  c({ id: "talk", body: "@dev please check the copy", created_at: "2026-11-24T10:04:00Z" }),
  c({
    id: "talk-r1",
    parent_id: "talk",
    source: "agent",
    author: "dev",
    body: "Done",
    created_at: "2026-11-24T10:06:00Z",
  }),
  c({
    id: "talk-mention",
    parent_id: "talk",
    source: "system",
    key: "api.tasks.comments.mention_failed",
    body: "@ghost could not run",
    created_at: "2026-11-24T10:06:30Z",
  }),
  c({
    id: "talk-pause",
    parent_id: "talk",
    source: "system",
    kind: "pause",
    key: LEGACY_PAUSE_KEY,
    meta: { turns: 6 },
    created_at: "2026-11-24T10:07:00Z",
  }),
  c({
    id: "moved",
    source: "system",
    key: "api.tasks.comments.moved",
    body: "Moved to Testing",
    created_at: "2026-11-24T10:08:00Z",
  }),
];

describe("commentKind", () => {
  it("trusts the daemon's kind", () => {
    expect(commentKind(comments[1])).toBe("block");
    expect(commentKind(comments[2])).toBe("decision");
    expect(commentKind(c({ id: "x", kind: "approval" }))).toBe("approval");
  });

  it("reads an older comment as conversation, except a known question and the turn-limit note", () => {
    expect(commentKind(c({ id: "old" }))).toBe("comment");
    expect(commentKind(c({ id: "q-old", source: "agent" }), new Set(["q-old"]))).toBe("question");
    expect(commentKind(c({ id: "p-old", source: "system", key: LEGACY_PAUSE_KEY }))).toBe("pause");
    expect(commentKind(c({ id: "odd", kind: "telepathy" }))).toBe("comment");
  });
});

describe("conversation", () => {
  it("drops the flow's own notes and keeps typed threads, replies, decisions and the pause", () => {
    const ids = conversation(comments).map((x) => x.id);
    expect(ids).not.toContain("run-note");
    expect(ids).not.toContain("moved");
    expect(ids).toEqual([
      "stop",
      "dec",
      "appr",
      "ask",
      "talk",
      "talk-r1",
      "talk-mention",
      "talk-pause",
    ]);
  });

  it("drops the replies of a root it dropped", () => {
    const ids = conversation([
      c({ id: "n", source: "system", key: "api.tasks.comments.run_failed" }),
      c({ id: "n-r", parent_id: "n", body: "why?" }),
    ]).map((x) => x.id);
    expect(ids).toEqual([]);
  });
});

describe("threads, filters and order", () => {
  const threads = commentThreads(comments);

  it("groups replies under their root and types each thread by its root", () => {
    expect(threads.map((th) => [th.id, th.kind, th.replies.length])).toEqual([
      ["stop", "block", 1],
      ["appr", "approval", 0],
      ["ask", "question", 0],
      ["talk", "comment", 3],
    ]);
  });

  it("files each kind under one filter and counts them", () => {
    expect(filterOf("question")).toBe("questions");
    expect(filterOf("block")).toBe("decisions");
    expect(filterOf("approval")).toBe("decisions");
    expect(filterOf("decision")).toBe("decisions");
    expect(filterOf("comment")).toBe("talk");
    expect(countByFilter(threads)).toEqual({ all: 4, questions: 1, decisions: 2, talk: 1 });
    expect(parseCommentFilter("decisions")).toBe("decisions");
    expect(parseCommentFilter("runs")).toBe("all");
  });

  it("lists newest or oldest first, without the pinned threads", () => {
    const pinned = new Set(["stop"]);
    expect(listedThreads(threads, { filter: "all", sort: "new", pinned }).map((x) => x.id)).toEqual(
      ["talk", "ask", "appr"],
    );
    expect(listedThreads(threads, { filter: "all", sort: "old" }).map((x) => x.id)).toEqual([
      "stop",
      "appr",
      "ask",
      "talk",
    ]);
    expect(listedThreads(threads, { filter: "decisions", sort: "old" }).map((x) => x.id)).toEqual([
      "stop",
      "appr",
    ]);
  });
});

describe("pending threads", () => {
  it("pins the attention's thread and the threads of the approvals holding the stage", () => {
    const ids = pendingThreadIDs({
      attention: { thread_id: "stop" },
      approvals: [
        { id: "a1", status: "pending", thread_id: "appr" },
        { id: "a2", status: "approved", thread_id: "old" },
        { id: "a3", status: "pending" },
        { id: "flag", status: "pending", thread_id: "refiner-flag" },
      ],
      waiting: { approvals: ["a1", "a2", "a3"] },
    });
    expect([...ids].sort()).toEqual(["appr", "stop"]);
  });

  it("leaves out a pending approval the flow went on without", () => {
    const approvals = [{ id: "flag", status: "pending", thread_id: "refiner-flag" }];
    expect(pendingThreadIDs({ approvals, waiting: { approvals: [] } }).size).toBe(0);
    expect(pendingThreadIDs({ approvals }).size).toBe(0);
  });

  it("pins the questions the stage the task is in asked", () => {
    const qs = [
      { id: "q-now", status: "open" },
      { id: "q-refiner", status: "open" },
      { id: "q-done", status: "answered" },
    ];
    expect(waitingQuestions(qs, { questions: ["q-now", "q-done"] }).map((q) => q.id)).toEqual([
      "q-now",
    ]);
    expect(waitingQuestions(qs, null)).toEqual([]);
  });

  it("pins nothing while the task runs or on an older daemon", () => {
    expect(pendingThreadIDs({ running: true, attention: { thread_id: "stop" } }).size).toBe(0);
    expect(pendingThreadIDs({ attention: {}, approvals: null }).size).toBe(0);
  });

  it("leaves questions to their own answer block", () => {
    const threads = commentThreads(comments);
    const pinned = pinnedThreads(threads, new Set(["stop", "appr", "ask"]));
    expect(pinned.map((th) => th.id)).toEqual(["stop", "appr"]);
  });
});

describe("decision labels and tags", () => {
  it("names each action the person took", () => {
    expect(decisionLabel({ action: "move", to_stage: "testing" }, stageLabel).text).toBe(
      t("lib.comments.decision.moved_to", { stage: "Testing" }),
    );
    expect(decisionLabel({ action: "send_back", to_stage: "in_progress" }, stageLabel).text).toBe(
      t("lib.comments.decision.sent_back_to", { stage: "Fix local" }),
    );
    expect(
      decisionLabel({ action: "relaunch", from_stage: "testing", to_stage: "testing" }, stageLabel)
        .text,
    ).toBe(t("lib.comments.decision.reran", { stage: "Testing" }));
    expect(
      decisionLabel({ action: "waive", cases: ["TC-3", "TC-4", "TC-5"] }, stageLabel).text,
    ).toBe(t("lib.comments.decision.skipped", { cases: "TC-3–5" }));
    expect(decisionLabel({ action: "access", cases: ["TC-3"] }, stageLabel).text).toBe(
      t("lib.comments.decision.access", { cases: "TC-3" }),
    );
    expect(decisionLabel({ action: "approve" }, stageLabel).text).toBe(
      t("lib.comments.decision.approved"),
    );
    expect(decisionLabel({ action: "request_changes" }, stageLabel).text).toBe(
      t("lib.comments.decision.changes"),
    );
    expect(decisionLabel({ action: "waive_approval" }, stageLabel).text).toBe(
      t("lib.comments.decision.approval_skipped"),
    );
    expect(decisionLabel(null, stageLabel).text).toBe(t("lib.comments.decision.other"));
    expect(decisionLabel({ action: "move" }, stageLabel).text).toBe(
      t("lib.comments.decision.moved"),
    );
  });

  it("tags a waiting stop, an approval, a question and a decision; plain talk carries none", () => {
    expect(kindTagHTML({ kind: "block", pending: true }, stageLabel)).toContain(
      t("lib.comments.kind.waiting"),
    );
    expect(kindTagHTML({ kind: "block" }, stageLabel)).toContain(t("lib.comments.kind.stopped"));
    expect(kindTagHTML({ kind: "approval", pending: true }, stageLabel)).toContain("cm-tag-blk");
    expect(kindTagHTML({ kind: "approval", resolved: true }, stageLabel)).toContain("cm-tag-ok");
    expect(kindTagHTML({ kind: "approval" }, stageLabel)).toContain(
      t("lib.comments.kind.approval"),
    );
    expect(kindTagHTML({ kind: "question" }, stageLabel)).toContain(
      t("lib.comments.kind.question"),
    );
    expect(
      kindTagHTML({ kind: "decision", meta: { action: "move", to_stage: "<b>" } }, stageLabel),
    ).not.toContain("<b>");
    expect(kindTagHTML({ kind: "comment" }, stageLabel)).toBe("");
    expect(kindTagHTML({ kind: "pause" }, stageLabel)).toBe("");
  });
});

describe("pause, long bodies and visits", () => {
  it("says how many replies the agents spent before the pause", () => {
    expect(pauseText(6)).toBe(t("lib.comments.paused", { count: 6 }));
    expect(pauseText(undefined)).toBe(t("lib.comments.paused_plain"));
    expect(pauseHTML(comments[8])).toContain('id="comment-talk-pause"');
  });

  it("folds a body over the length or line limit", () => {
    expect(isLongBody("short")).toBe(false);
    expect(isLongBody("x".repeat(421))).toBe(true);
    expect(isLongBody(Array(9).fill("line").join("\n"))).toBe(true);
    expect(isLongBody(undefined)).toBe(false);
  });

  it("links the flow's kinds to Activity, not plain talk", () => {
    expect(hasVisit("block")).toBe(true);
    expect(hasVisit("question")).toBe(true);
    expect(hasVisit("comment")).toBe(false);
    expect(hasVisit("pause")).toBe(false);
  });
});

describe("rankMentions", () => {
  const agents = [
    { handle: "reviewer", words: ["Code review"] },
    { handle: "thulu-evidence", words: ["Evidence"] },
    { handle: "dev", words: ["Developer", "writes the evidence code"] },
    { handle: "devops", words: [] },
    { handle: "e2e-runner", words: [] },
  ];

  it("puts handles that start with the query first, then word starts, then anywhere", () => {
    expect(rankMentions(agents, "dev").map((a) => a.handle)).toEqual(["dev", "devops"]);
    expect(rankMentions(agents, "ev").map((a) => a.handle)).toEqual([
      "thulu-evidence",
      "reviewer",
      "dev",
      "devops",
    ]);
    expect(rankMentions(agents, "evi").map((a) => a.handle)).toEqual([
      "thulu-evidence",
      "reviewer",
      "dev",
    ]);
  });

  it("lists everyone for an empty query, in order, up to the limit", () => {
    expect(rankMentions(agents, "").map((a) => a.handle)).toEqual(agents.map((a) => a.handle));
    expect(rankMentions(agents, "", 2)).toHaveLength(2);
    expect(rankMentions(agents, "zzz")).toEqual([]);
  });
});

describe("comments preference", () => {
  it("keeps only a sort it knows", () => {
    expect(parseCommentsPref({ sort: "old" })).toEqual({ sort: "old" });
    expect(parseCommentsPref({ sort: "cost" })).toEqual({ sort: "new" });
    expect(parseCommentsPref(null)).toEqual({ sort: "new" });
  });

  it("round-trips through this browser and survives a broken store", () => {
    const store = new Map<string, string>();
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
    };
    cacheCommentsPref(storage, { sort: "old" });
    expect(readCachedCommentsPref(storage)).toEqual({ sort: "old" });
    expect(readCachedCommentsPref(null)).toEqual({ sort: "new" });
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
    };
    expect(readCachedCommentsPref(broken)).toEqual({ sort: "new" });
  });
});

describe("avatars", () => {
  it("marks an agent with its role, without the workspace prefix", () => {
    expect(agentMark("thulu-evidence")).toBe("EV");
    expect(agentMark("thulu-test-designer")).toBe("TE");
    expect(agentMark("reviewer")).toBe("RE");
    expect(agentMark("agent:qa")).toBe("QA");
    expect(agentMark("")).toBe("?");
  });

  it("paints an agent in the colour of the column it works, else where it wrote, else grey", () => {
    const stages = [
      { id: "dev", color: "#f2994a" },
      { id: "qa", color: "#27ae60" },
      { id: "bad", color: "red;}" },
    ];
    expect(agentColor("coder", stages, { dev: "coder" }, "qa")).toBe("#f2994a");
    expect(agentColor("coder", stages, { dev: "other" }, "qa")).toBe("#27ae60");
    expect(agentColor("coder", stages, { bad: "coder" }, "bad")).toBe(AGENT_GREY);
    expect(agentColor("coder", stages, null)).toBe(AGENT_GREY);
  });

  it("draws a person round with initials and an agent square in its colour", () => {
    const person = cmAvatarHTML("person", "You");
    expect(person).toContain("cm-av");
    expect(person).toContain(">YO<");
    expect(person).not.toContain("avatar-square");
    const agent = cmAvatarHTML("agent", "thulu-evidence", "#27ae60");
    expect(agent).toContain("avatar-square");
    expect(agent).toContain(">EV<");
    expect(agent).toContain("--av-bg:#27ae60");
    expect(cmAvatarHTML("agent", "x-y", "url(evil)")).toContain(`--av-bg:${AGENT_GREY}`);
  });
});

describe("highlightMentions", () => {
  it("marks known agents in text and leaves code, links and strangers alone", () => {
    const html =
      '<p>@dev look, cc @ghost (@qa)</p><p><code>@dev</code> <a href="x">@qa</a> mail@dev.io</p>';
    const out = highlightMentions(html, ["dev", "qa"]);
    expect(out).toContain('<span class="mention">@dev</span> look');
    expect(out).toContain('(<span class="mention">@qa</span>)');
    expect(out).toContain("cc @ghost");
    expect(out).toContain("<code>@dev</code>");
    expect(out).toContain('<a href="x">@qa</a>');
    expect(out).toContain("mail@dev.io");
  });

  it("does nothing without agents", () => {
    expect(highlightMentions("<p>@dev</p>", [])).toBe("<p>@dev</p>");
  });
});

describe("answerReplyID", () => {
  const replies = [
    { id: "agent", source: "agent", created_at: "2026-11-20T09:54:50Z" },
    { id: "late", source: "user", created_at: "2026-11-20T10:30:00Z" },
    { id: "ans", source: "user", created_at: "2026-11-20T09:54:51Z" },
  ];

  it("finds the person's reply posted with the answer", () => {
    expect(
      answerReplyID({ status: "answered", answered_at: "2026-11-20T09:54:50.77Z" }, replies),
    ).toBe("ans");
  });

  it("finds none for an open question or one answered without a reply", () => {
    expect(answerReplyID({ status: "open" }, replies)).toBe("");
    expect(
      answerReplyID({ status: "answered", answered_at: "2026-11-21T00:00:00Z" }, replies),
    ).toBe("");
  });
});
