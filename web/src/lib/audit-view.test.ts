import { describe, expect, it } from "vitest";

import {
  auditActor,
  auditColumns,
  auditResourceHref,
  auditRowHTML,
  auditSummary,
  chainLine,
} from "./audit-view";

describe("auditActor", () => {
  it("names a person by email and a key by its name", () => {
    expect(
      auditActor({
        kind: "task.created",
        actor_type: "user",
        actor_id: "u1",
        actor_email: "ana@example.com",
      }),
    ).toBe("ana@example.com");
    expect(
      auditActor({
        kind: "task.created",
        actor_type: "api_key",
        actor_id: "k1",
        actor_label: "CI",
      }),
    ).toBe("Key: CI");
  });

  it("names an agent run and the daemon", () => {
    expect(
      auditActor({ kind: "task.moved", actor_type: "agent", actor_label: "run:coder@dev task:t1" }),
    ).toBe("Agent: run:coder@dev task:t1");
    expect(auditActor({ kind: "audit.purged", actor_type: "system" })).toBe("Thulu (system)");
    expect(auditActor({ kind: "audit.purged", actor_id: "system" })).toBe("Thulu (system)");
  });

  it("falls back to what a sparse row has, the id last", () => {
    expect(
      auditActor({ kind: "task.created", actor_id: "u9", actor_email: "bo@example.com" }),
    ).toBe("bo@example.com");
    expect(auditActor({ kind: "task.created", actor_id: "u9" })).toBe("u9");
    expect(auditActor({ kind: "task.created" })).toBe("—");
  });
});

describe("auditResourceHref", () => {
  it("links tasks, projects and boards", () => {
    expect(
      auditResourceHref({ kind: "task.created", resource_type: "task", resource_id: "t 1" }),
    ).toBe("/tasks/#task-t%201");
    expect(auditResourceHref({ kind: "project.created", resource_id: "p1" })).toBe(
      "/project/?id=p1",
    );
    expect(auditResourceHref({ kind: "x", resource_type: "board", resource_id: "b1" })).toBe(
      "/tasks/?board=b1",
    );
  });

  it("opens the list a key, a member, a stage or an automation lives in", () => {
    const href = (resource_type: string) =>
      auditResourceHref({ kind: "x", resource_type, resource_id: "id1" });
    expect(href("api_key")).toBe("/settings/#api");
    expect(href("user")).toBe("/settings/#team");
    expect(href("stage")).toBe("/settings/#boards");
    expect(href("automation")).toBe("/automations/");
    expect(href("agent")).toBe("/agents/?agent=id1");
  });

  it("links nothing it has no page for", () => {
    expect(
      auditResourceHref({ kind: "x", resource_type: "license", resource_id: "l1" }),
    ).toBeNull();
    expect(auditResourceHref({ kind: "task.created" })).toBeNull();
  });
});

describe("auditSummary", () => {
  it("prefers the summary in the reader's language", () => {
    expect(
      auditSummary({ kind: "k", summary: "API key created", summary_text: "Se creó la clave" }),
    ).toBe("Se creó la clave");
    expect(auditSummary({ kind: "k", summary: "API key created" })).toBe("API key created");
    expect(auditSummary({ kind: "k" })).toBe("—");
  });
});

describe("chainLine", () => {
  it("reports the entries checked and the head", () => {
    expect(chainLine({ checked: 1204, legacy: 0, ok: true, head: "abcdef0123456789" })).toBe(
      "Chain verified: 1204 entries, head hash abcdef012345…",
    );
  });

  it("mentions rows older than the chain", () => {
    expect(chainLine({ checked: 1, legacy: 3, ok: true, head: "ab" })).toBe(
      "Chain verified: 1 entry, head hash ab… 3 older entries predate the chain and are not covered.",
    );
  });

  it("names where the chain breaks", () => {
    expect(
      chainLine({ checked: 10, legacy: 0, ok: false, broken_at: "e11", reason: "hash mismatch" }),
    ).toBe("Chain broken at entry e11 after 10 good entries: hash mismatch");
  });

  it("names the row once, with when it was written", () => {
    const line = chainLine({
      checked: 10,
      legacy: 0,
      ok: false,
      broken_at: "e11",
      reason: "hash mismatch (row e11)",
      cause: "hash mismatch",
      broken_created_at: "2026-10-15T12:00:00Z",
    });
    expect(line.match(/e11/g)).toHaveLength(1);
    expect(line).toMatch(
      /^Chain broken at entry e11, written .+, after 10 good entries: hash mismatch$/,
    );
  });
});

describe("auditRowHTML", () => {
  const row = {
    kind: "task.created",
    actor_type: "user",
    actor_id: "u1",
    actor_email: "ana@example.com",
    resource_id: "t<1>",
    summary: "made it",
    created_at: "2026-10-01T12:00:00Z",
  };

  it("writes a cell per column, each labelled for the phone layout", () => {
    const html = auditRowHTML(row);
    expect(html.match(/<td/g)).toHaveLength(auditColumns().length);
    for (const col of auditColumns()) expect(html).toContain(`data-label="${col.label}"`);
    expect(html).toContain('audit-summary" data-label=');
  });

  it("keeps the actor's id in the title and links the resource, escaped", () => {
    const html = auditRowHTML(row);
    expect(html).toContain('title="u1"');
    expect(html).toContain('href="/tasks/#task-t%3C1%3E"');
    expect(html).toContain(">t&lt;1&gt;</a>");
  });

  it("leaves the title off an actor without an id, and dashes a missing resource", () => {
    const html = auditRowHTML({ ...row, actor_id: "", resource_id: undefined });
    expect(html).not.toContain("title=");
    expect(html).toContain(">—<");
  });
});
