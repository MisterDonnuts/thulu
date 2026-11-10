// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  allowsOther,
  answerBody,
  answerProblem,
  answerProblemMessage,
  deliveryText,
  emptyDraft,
  isBlocking,
  questionHTML,
  questionStatusBadgeHTML,
  questionsByComment,
  recordedAnswer,
  toggleOption,
  type TaskQuestion,
} from "./questions";

const base: TaskQuestion = {
  id: "q1",
  comment_id: "c1",
  agent: "dev",
  kind: "question",
  blocking: true,
  status: "open",
  payload: {
    kind: "question",
    blocking: true,
    context: "Picking the cache layer",
    questions: [
      {
        header: "Cache",
        question: "Which cache?",
        options: [{ label: "Redis", description: "shared" }, { label: "In-process" }],
      },
      {
        header: "Scope",
        question: "Which endpoints?",
        options: [{ label: "/a" }, { label: "/b" }],
        multi_select: true,
        allow_other: false,
      },
    ],
  },
};

describe("questionsByComment", () => {
  it("indexes by the comment the question is drawn on", () => {
    const m = questionsByComment([base, { ...base, id: "q2", comment_id: "c9" }]);
    expect(m.get("c9")?.id).toBe("q2");
    expect(questionsByComment(undefined).size).toBe(0);
  });
});

describe("defaults", () => {
  it("treats a missing blocking flag as blocking, like the tool", () => {
    expect(isBlocking({ ...base, blocking: undefined, payload: {} })).toBe(true);
    expect(isBlocking({ ...base, blocking: false })).toBe(false);
  });

  it("offers free text unless the agent turned it off, and always without options", () => {
    expect(allowsOther({ question: "x", options: [{ label: "a" }] })).toBe(true);
    expect(allowsOther({ question: "x", options: [{ label: "a" }], allow_other: false })).toBe(
      false,
    );
    expect(allowsOther({ question: "x", options: [], allow_other: false })).toBe(true);
  });
});

describe("toggleOption", () => {
  it("keeps one label on a single-choice question", () => {
    let d = emptyDraft(base);
    d = toggleOption(d, 0, "Redis", false);
    d = toggleOption(d, 0, "In-process", false);
    expect(d.selected[0]).toEqual(["In-process"]);
  });

  it("toggles on a multi-select, without touching the old draft", () => {
    const start = emptyDraft(base);
    const a = toggleOption(start, 1, "/a", true);
    const ab = toggleOption(a, 1, "/b", true);
    expect(toggleOption(ab, 1, "/a", true).selected[1]).toEqual(["/b"]);
    expect(start.selected[1]).toEqual([]);
  });
});

describe("answerProblem", () => {
  it("names the first question left without an answer", () => {
    const d = toggleOption(emptyDraft(base), 0, "Redis", false);
    const p = answerProblem(base, d);
    expect(p).toEqual({ reason: "unanswered", index: 1 });
    expect(answerProblemMessage(base, { reason: "unanswered", index: 1 })).toContain("Scope");
  });

  it("asks to write when the question offers nothing to pick", () => {
    const secret = {
      ...base,
      payload: { ...base.payload, questions: [{ header: "Secret", question: "Token?" }] },
    };
    expect(answerProblemMessage(secret, { reason: "unanswered", index: 0 })).toBe(
      'Write an answer for "Secret"',
    );
  });

  it("accepts free text in place of an option", () => {
    const d = toggleOption(emptyDraft(base), 1, "/a", true);
    d.other[0] = "Memcached";
    expect(answerProblem(base, d)).toBeNull();
  });

  it("ignores free text where the agent did not allow it", () => {
    const d = toggleOption(emptyDraft(base), 0, "Redis", false);
    d.other[1] = "all of them";
    expect(answerProblem(base, d)).toEqual({ reason: "unanswered", index: 1 });
  });

  it("asks a blocker for words, in Other or in the note", () => {
    const blocker: TaskQuestion = {
      ...base,
      kind: "blocker",
      payload: { questions: [{ question: "Which token?", options: [{ label: "prod" }] }] },
    };
    const d = toggleOption(emptyDraft(blocker), 0, "prod", false);
    expect(answerProblem(blocker, d)).toEqual({ reason: "blocker_text" });
    expect(answerProblem(blocker, { ...d, note: "it is in the vault" })).toBeNull();
    expect(answerProblem(blocker, { ...d, other: ["staging"] })).toBeNull();
  });
});

describe("answerBody", () => {
  it("sends the contract's shape, trimmed, without empty fields", () => {
    let d = toggleOption(emptyDraft(base), 0, "Redis", false);
    d = toggleOption(d, 1, "/b", true);
    d.other[0] = "  with a TTL  ";
    d.note = "  ";
    expect(answerBody(base, d)).toEqual({
      answers: [
        { question_index: 0, selected: ["Redis"], other: "with a TTL" },
        { question_index: 1, selected: ["/b"] },
      ],
    });
  });

  it("drops labels the question does not offer and keeps the note", () => {
    const d = emptyDraft(base);
    d.selected[0] = ["Gone"];
    d.note = "ship it";
    expect(answerBody(base, d)).toMatchObject({
      answers: [{ question_index: 0, selected: [] }, { question_index: 1 }],
      note: "ship it",
    });
  });
});

describe("recordedAnswer", () => {
  const answers = [{ question_index: 0, selected: ["Redis"] }];
  it("reads the answers array with a top-level note", () => {
    expect(recordedAnswer({ ...base, answers, note: "n" })).toEqual({ answers, note: "n" });
  });
  it("reads the whole stored body", () => {
    expect(recordedAnswer({ ...base, answers: { answers, note: "n" } })).toEqual({
      answers,
      note: "n",
    });
  });
  it("is empty when nothing was recorded", () => {
    expect(recordedAnswer({ ...base, answers: null })).toEqual({ answers: [], note: "" });
  });
});

describe("questionHTML", () => {
  it("draws an open form with radios, checkboxes and the Other field", () => {
    const html = questionHTML(base, emptyDraft(base));
    const doc = new DOMParser().parseFromString(html, "text/html");
    const cards = [...doc.querySelectorAll(".check-card.q-opt")];
    expect(cards.length).toBeGreaterThan(0);
    expect(cards.every((c) => c.querySelector("input[data-q-opt]:not(:disabled)"))).toBe(true);
    expect(html).toContain('type="radio"');
    expect(html).toContain('type="checkbox"');
    expect(html).toContain('data-q-other="0"');
    expect(html).not.toContain('data-q-other="1"');
    expect(html).toContain("data-q-submit");
    expect(html).toContain("Picking the cache layer");
    expect(html).toContain('<span class="tag tag-sm tag-special tag-inline">');
    expect(html).toContain('<span class="tag tag-sm"><span class="tag-label">Cache</span></span>');
  });

  it("tags a question that does not block", () => {
    const html = questionHTML({ ...base, blocking: false }, emptyDraft(base));
    expect(html).toContain('<span class="tag-label">keeps working meanwhile</span>');
    expect(questionHTML(base, emptyDraft(base))).not.toContain("keeps working meanwhile");
  });

  it("marks a blocker and asks for the note", () => {
    const html = questionHTML({ ...base, kind: "blocker" }, emptyDraft(base));
    expect(html).toContain("q-blocker");
    expect(html).toContain("(required)");
    expect(html).not.toContain("/credentials/");
    expect(questionHTML(base, emptyDraft(base))).not.toContain("/credentials/");
  });

  it("points a blocker that needs a credential to Credentials", () => {
    const q = { ...base, kind: "blocker", payload: { ...base.payload, needs_credential: true } };
    const html = questionHTML(q, emptyDraft(q));
    expect(html).toContain('href="/credentials/"');
    expect(html).toContain("q-secret");
  });

  it("shows an answered question read-only with the choice and who answered", () => {
    const html = questionHTML(
      {
        ...base,
        status: "answered",
        answered_by: "ana@example.com",
        answered_at: "2026-10-01T10:00:00Z",
        answers: { answers: [{ question_index: 0, selected: ["Redis"], other: "TTL 5m" }] },
      },
      emptyDraft(base),
    );
    expect(html).not.toContain("data-q-submit");
    const doc = new DOMParser().parseFromString(html, "text/html");
    const options = [...doc.querySelectorAll<HTMLInputElement>("[data-q-opt='0']")];
    expect(options.length).toBeGreaterThan(1);
    expect(options.every((o) => o.disabled)).toBe(true);
    expect(options.filter((o) => o.checked).map((o) => o.value)).toEqual(["Redis"]);
    expect(doc.querySelector(".check-card")).toBeNull();
    expect(html).toContain("TTL 5m");
    expect(html).toContain("ana@example.com");
    expect(html).toContain('data-delivery=""');
  });

  it("states the question's status as a badge in its tone", () => {
    expect(questionStatusBadgeHTML("open")).toBe(
      '<span class="badge badge-warn badge-sm q-state" data-status="open">waiting for an answer</span>',
    );
    expect(questionStatusBadgeHTML("answered")).toContain("badge-ok");
    expect(questionStatusBadgeHTML("canceled")).toContain("badge-muted");
    expect(questionStatusBadgeHTML("weird")).toContain(">weird</span>");
    const html = questionHTML({ ...base, status: "answered" }, emptyDraft(base));
    expect(html).toContain('<span class="badge badge-ok badge-sm q-state" data-status="answered">');
  });

  it("escapes what the agent wrote", () => {
    const html = questionHTML(
      { ...base, payload: { questions: [{ question: "<img src=x onerror=alert(1)>" }] } },
      emptyDraft(base),
    );
    expect(html).not.toContain("<img");
  });

  it("says what happens when nobody answers a non-blocking question", () => {
    const html = questionHTML(
      { ...base, blocking: false, payload: { ...base.payload, default: "use Redis" } },
      emptyDraft(base),
    );
    expect(html).toContain("use Redis");
    expect(html).toContain("keeps working meanwhile");
  });
});

describe("deliveryText", () => {
  it("says where an answer went, and that it waits when it went nowhere yet", () => {
    expect(deliveryText({ status: "open" })).toBe("");
    expect(deliveryText({ status: "answered", delivery: "steered" })).toContain("while it worked");
    expect(deliveryText({ status: "answered", delivery: "thread" })).toContain("next run");
    expect(deliveryText({ status: "answered", delivery: "" })).toContain("Not delivered yet");
  });
  it("says the answer is saved when the resume failed, instead of promising a delivery", () => {
    const text = deliveryText({ status: "answered", delivery: "" }, true);
    expect(text).toContain("could not be resumed");
    expect(text).not.toContain("Not delivered yet");
    expect(deliveryText({ status: "answered", delivery: "thread" }, true)).toContain("next run");
  });
});
