// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  approvalAuthor,
  approvalStatusBadgeHTML,
  approvalsOf,
  approvalsPaneHTML,
  approvalsSig,
  changesEffect,
  decisionBody,
  kickBackTarget,
  type ChangesStage,
  pendingApprovalCount,
  type Approval,
} from "./approvals";

const design: Approval = {
  id: "a1",
  key: "design",
  label: "Design",
  source: "stage",
  status: "pending",
  reason: "touches checkout",
};

describe("approvalsOf", () => {
  it("prefers approvals over the flags", () => {
    expect(
      approvalsOf({ approvals: [design], flags: [{ id: "f", label: "x", status: "pending" }] }),
    ).toEqual([design]);
  });

  it("maps an older daemon's flags, rejected becoming changes requested", () => {
    expect(
      approvalsOf({ flags: [{ id: "f1", kind: "ux_design", label: "UX", status: "rejected" }] }),
    ).toEqual([
      { id: "f1", key: "ux_design", label: "UX", source: "agent", status: "changes_requested" },
    ]);
  });
});

describe("pendingApprovalCount", () => {
  it("uses the list response's count when there is one", () => {
    expect(pendingApprovalCount({ pending_approvals: 2, approvals: [] })).toBe(2);
  });

  it("counts what still holds the task otherwise", () => {
    const rows: Approval[] = [
      design,
      { ...design, id: "a2", status: "approved" },
      { ...design, id: "a3", status: "waived" },
      { ...design, id: "a4", status: "changes_requested" },
    ];
    expect(pendingApprovalCount({ approvals: rows })).toBe(2);
    expect(pendingApprovalCount({ flags: [{ id: "f", label: "x", status: "pending" }] })).toBe(1);
    expect(pendingApprovalCount({})).toBe(0);
  });
});

describe("decisionBody", () => {
  it("approves without a note", () => {
    expect(decisionBody("approved", " ")).toEqual({ decision: "approved" });
  });

  it("refuses changes or a waiver without one", () => {
    expect(decisionBody("changes_requested", "  ")).toBeNull();
    expect(decisionBody("waived", "")).toBeNull();
    expect(decisionBody("waived", " not this time ")).toEqual({
      decision: "waived",
      note: "not this time",
    });
  });
});

describe("approvalsPaneHTML", () => {
  it("ranks a pending row's buttons: Approve primary, Request changes, Waive ghost", () => {
    const html = approvalsPaneHTML([design], null);
    expect(html).toMatch(/class="btn btn-primary btn-sm"[^>]*data-ap="approved"/);
    expect(html).toMatch(/class="btn btn-sm"[^>]*data-ap="changes_requested"/);
    expect(html).toMatch(/class="btn btn-ghost btn-sm"[^>]*data-ap="waived"/);
    expect(html.match(/btn-primary/g)).toHaveLength(1);
  });

  it("puts the note above the buttons and Delete inside the row menu", () => {
    const asked = { ...design, source: "person" };
    const closed = approvalsPaneHTML([asked], null);
    expect(closed.indexOf("data-ap-note-for")).toBeLessThan(closed.indexOf('data-ap="approved"'));
    expect(closed).not.toContain('data-ap="delete"');
    const open = approvalsPaneHTML([asked], null, { menuFor: "a1" });
    expect(open).toMatch(/role="menu"[\s\S]*data-ap="delete"/);
  });

  it("labels the note and ties its help and error lines to it", () => {
    const html = approvalsPaneHTML([design], null);
    const doc = new DOMParser().parseFromString(html, "text/html");
    const box = doc.querySelector<HTMLTextAreaElement>('[data-ap-note-for="a1"]');
    expect(box?.id).toBe("ap-note-a1");
    const field = box?.closest(".field");
    expect(field?.querySelector<HTMLLabelElement>(".field-label")?.htmlFor).toBe("ap-note-a1");
    expect(box?.getAttribute("aria-describedby")).toBe("ap-note-a1-help");
    expect(field?.querySelector("#ap-note-a1-help")?.textContent).not.toBe("");
    const err = field?.querySelector<HTMLElement>("#ap-note-a1-error");
    expect(err?.classList.contains("field-error")).toBe(true);
    expect(err?.hidden).toBe(true);
  });

  it("labels each control of the ask form", () => {
    const doc = new DOMParser().parseFromString(
      approvalsPaneHTML([], null, { askOpen: true }),
      "text/html",
    );
    for (const id of ["ap-label", "ap-reason"]) {
      const label = doc.querySelector<HTMLLabelElement>(`label[for="${id}"]`);
      expect(label?.classList.contains("field-label")).toBe(true);
      expect(doc.getElementById(id)?.tagName).toBe("INPUT");
    }
  });

  it("shows a reader the approvals and their state with nothing to decide", () => {
    const asked = { ...design, id: "a2", source: "person" };
    const decided = { ...design, status: "approved" as const, decided_by: "ana" };
    const html = approvalsPaneHTML([asked, decided], null, { readOnly: true, menuFor: "a2" });
    expect(html).toContain(asked.label);
    expect(html).not.toMatch(/data-ap=|data-ap-menu|data-ap-note|ap-ask-open|<textarea|<input/);
    expect(approvalsSig([asked], null, { readOnly: true })).not.toBe(approvalsSig([asked], null));
  });

  it("never offers to delete a stage's approval, and drops an empty menu", () => {
    expect(approvalsPaneHTML([design], null)).not.toContain("data-ap-menu");
    const decided = approvalsPaneHTML(
      [{ ...design, status: "approved", decided_by: "ana" }],
      null,
      { menuFor: "a1" },
    );
    expect(decided).toContain('data-ap="waived"');
    expect(decided).not.toContain('data-ap="delete"');
    expect(decided).not.toContain("menu-sep");
  });

  it("draws the open row menu as a menu: the other decisions, a rule, Delete", () => {
    const html = approvalsPaneHTML(
      [{ ...design, source: "person", status: "approved", decided_by: "ana" }],
      null,
      { menuFor: "a1" },
    );
    const doc = new DOMParser().parseFromString(html, "text/html");
    const menu = doc.querySelector<HTMLElement>(".menu.ap-pop");
    if (!menu) throw new Error("no open row menu");
    expect(menu.getAttribute("role")).toBe("menu");
    expect(menu.hidden).toBe(false);
    const kids = Array.from(menu.children).map((el) => el.getAttribute("data-ap") ?? el.className);
    expect(kids).toEqual(["changes_requested", "waived", "menu-sep", "delete"]);
    expect(menu.querySelector('[data-ap="delete"]')?.className).toBe("menu-item menu-item-danger");
  });

  it("names the stage that asks and keeps the key in a tooltip", () => {
    const html = approvalsPaneHTML([{ ...design, stage: "review" }], null, {
      stageLabel: (id) => (id === "review" ? "Review" : id),
    });
    expect(html).toContain("asked by Review");
    expect(html).toContain('title="Key: design"');
    expect(html).not.toContain("<code");
  });

  it("names who asked with the shared author line", () => {
    const html = approvalsPaneHTML(
      [{ ...design, source: "agent", created_by: "agent:developer" }],
      null,
    );
    const box = document.createElement("div");
    box.innerHTML = html;
    const line = box.querySelector(".approval-head .author-line");
    expect(line?.querySelector(".author-line-name")?.textContent).toBe("developer");
    expect(line?.querySelector(".author-line-kind")?.textContent).toBe("agent");
    expect(line?.querySelector(".avatar")).toBeNull();
    expect(line?.querySelector("time")).toBeNull();
    expect(html).not.toContain("approval-src");
  });

  it("says under Request changes what it does", () => {
    const html = approvalsPaneHTML([design], null, { effectOf: () => "goes back to Ready" });
    expect(html.indexOf('data-ap="waived"')).toBeLessThan(html.indexOf("goes back to Ready"));
  });

  it("lists pending rows first", () => {
    const html = approvalsPaneHTML(
      [
        { ...design, id: "done", label: "Legal", status: "approved", decided_by: "ana" },
        { ...design, id: "open", label: "Design" },
      ],
      null,
    );
    expect(html.indexOf('data-approval="open"')).toBeLessThan(html.indexOf('data-approval="done"'));
  });

  it("offers a decided row the other decisions in its menu", () => {
    const html = approvalsPaneHTML([{ ...design, status: "approved", decided_by: "ana" }], null, {
      menuFor: "a1",
    });
    expect(html).not.toContain('data-ap="approved"');
    expect(html).toContain('data-ap="changes_requested"');
    expect(html).toContain('data-ap="waived"');
    expect(html).toContain("Approved by ana");
  });

  it("opens a note box on a decided row being changed", () => {
    const html = approvalsPaneHTML([{ ...design, status: "approved" }], {
      id: "a1",
      decision: "waived",
    });
    expect(html).toContain("data-ap-note");
    expect(html).toContain('data-ap="confirm"');
  });

  it("keeps the ask form behind a button until asked", () => {
    expect(approvalsPaneHTML([], null)).toMatch(/id="ap-form" hidden/);
    expect(approvalsPaneHTML([], null, { askOpen: true })).not.toMatch(/id="ap-form" hidden/);
  });

  it("escapes the label and reason", () => {
    const html = approvalsPaneHTML([{ ...design, label: "<b>x</b>", reason: "<i>" }], null);
    expect(html).not.toContain("<b>x</b>");
    expect(html).not.toContain("<i>");
  });

  it("says so when there are none", () => {
    expect(approvalsPaneHTML([], null)).toContain("No approvals");
  });
});

describe("approvalsSig", () => {
  it("moves with a decision, the open note box, the ask form and the menu", () => {
    const a = approvalsSig([design], null);
    expect(approvalsSig([{ ...design, status: "approved" }], null)).not.toBe(a);
    expect(approvalsSig([design], { id: "a1", decision: "waived" })).not.toBe(a);
    expect(approvalsSig([design], null, { askOpen: true })).not.toBe(a);
    expect(approvalsSig([design], null, { menuFor: "a1" })).not.toBe(a);
    expect(approvalsSig([design], null)).toBe(a);
  });
});

describe("changesEffect", () => {
  const board: ChangesStage[] = [
    { id: "backlog", label: "Backlog", position: 0, initial: true },
    { id: "ready", label: "Ready", position: 1 },
    { id: "review", label: "Review", position: 2 },
    { id: "done", label: "Done", position: 3, terminal: true },
  ];
  const at = (mode: string, to = "") =>
    board.map((s) =>
      s.id === "review" ? { ...s, on_changes_requested: mode, changes_kick_back_to: to } : s,
    );

  it("resumes the stage's agent by default", () => {
    expect(changesEffect({ stage: "review" }, "review", board, "backend")).toBe(
      "Requesting changes resumes backend with your note.",
    );
  });

  it("sends the task back where kick_back says, or to the board's default", () => {
    expect(changesEffect({ stage: "review" }, "review", at("kick_back", "backlog"), "")).toContain(
      "back to Backlog",
    );
    expect(changesEffect({ stage: "review" }, "review", at("kick_back"), "")).toContain(
      "back to Ready",
    );
    expect(kickBackTarget(at("kick_back", "done"), at("kick_back", "done")[2])?.id).toBe("ready");
  });

  it("takes the board's first middle column as the default, agent or not", () => {
    const triage: ChangesStage = { id: "triage", label: "Triage", position: 1 };
    const rows = [
      board[0],
      triage,
      ...board.slice(1).map((s) => ({ ...s, position: s.position + 1 })),
    ];
    const review = { ...rows[3], on_changes_requested: "kick_back" };
    expect(kickBackTarget(rows, review)?.id).toBe("triage");
    const short: ChangesStage[] = [
      board[0],
      { id: "review", label: "Review", position: 1, on_changes_requested: "kick_back" },
      { id: "done", label: "Done", position: 2, terminal: true },
    ];
    expect(kickBackTarget(short, short[1])).toBeNull();
    expect(changesEffect({ stage: "review" }, "review", short, "")).toContain(
      "keeps the task in Review",
    );
  });

  it("says the decision waits while a run works on the task", () => {
    expect(changesEffect({ stage: "review" }, "review", board, "backend", true)).toBe(
      "A run is working on the task: requesting changes resumes backend with your note when it ends.",
    );
    expect(changesEffect({ stage: "review" }, "review", at("kick_back"), "", true)).toContain(
      "sends it back to Ready when the run ends",
    );
    expect(changesEffect({ stage: "review" }, "review", at("stay"), "x", true)).toContain(
      "keeps the task in Review",
    );
  });

  it("stays for a person, and only records an approval of another stage", () => {
    expect(changesEffect({ stage: "review" }, "review", at("stay"), "x")).toContain(
      "keeps the task in Review",
    );
    expect(changesEffect({ stage: "ready" }, "review", board, "x")).toContain("only recorded");
    expect(changesEffect({}, "review", board, "x")).toContain("only recorded");
  });
});

describe("approvalStatusBadgeHTML", () => {
  it("draws each state as a small badge in its tone", () => {
    expect(approvalStatusBadgeHTML("pending")).toBe(
      '<span class="badge badge-warn badge-sm" data-status="pending">pending</span>',
    );
    expect(approvalStatusBadgeHTML("approved")).toContain("badge-ok");
    expect(approvalStatusBadgeHTML("changes_requested")).toContain("badge-danger");
    expect(approvalStatusBadgeHTML("waived")).toContain("badge-muted");
    expect(approvalStatusBadgeHTML("odd")).toBe(
      '<span class="badge badge-sm" data-status="odd">odd</span>',
    );
  });

  it("says so when there is nothing to approve", () => {
    expect(approvalsPaneHTML([], null, { readOnly: true })).toContain(
      '<div class="empty-state empty-state-sm"',
    );
  });
});

describe("approvalAuthor", () => {
  const stageLabel = (id: string) => (id === "review" ? "Review" : id);

  it("names a person, an agent and the daemon as the History tab does", () => {
    expect(approvalAuthor({ ...design, source: "person", created_by: "ana" })).toEqual({
      name: "ana",
      kind: "person",
      stage: undefined,
    });
    expect(approvalAuthor({ ...design, source: "agent", created_by: "agent:qa" })).toEqual({
      name: "qa",
      kind: "agent",
      stage: undefined,
    });
    expect(
      approvalAuthor({ ...design, stage: "review", created_by: "octopus" }, stageLabel),
    ).toEqual({ name: "Thulu", kind: "system", stage: "Review" });
  });

  it("names the source when nobody signed the row", () => {
    expect(approvalAuthor({ ...design, stage: "review" }, stageLabel)).toEqual({
      name: "asked by Review",
      kind: "system",
    });
    expect(approvalAuthor({ ...design, source: "agent" })).toEqual({
      name: "agent",
      kind: "system",
    });
  });
});
