import { describe, expect, it } from "vitest";

import { ApiError } from "./api";
import { destinationOptions, movePickerHTML, pendingTaskCount } from "./move-picker";

describe("pendingTaskCount", () => {
  it("reads the count off the refusal", () => {
    expect(pendingTaskCount(new ApiError(409, "board not empty", { task_count: 12 }))).toBe(12);
  });

  it("is zero for a refusal that was about something else", () => {
    expect(pendingTaskCount(new ApiError(400, "bad id", { error: "bad id" }))).toBe(0);
    expect(pendingTaskCount(new ApiError(500, "boom"))).toBe(0);
    expect(pendingTaskCount(new Error("offline"))).toBe(0);
    expect(pendingTaskCount(undefined)).toBe(0);
  });

  it("is zero when the server says zero, so no picker is offered", () => {
    expect(pendingTaskCount(new ApiError(409, "", { task_count: 0 }))).toBe(0);
  });
});

describe("destinationOptions", () => {
  it("writes one option per destination", () => {
    expect(destinationOptions([{ id: "qa", label: "QA" }])).toBe('<option value="qa">QA</option>');
  });

  it("escapes both halves", () => {
    expect(destinationOptions([{ id: 'a"b', label: "<b>" }])).toBe(
      '<option value="a&quot;b">&lt;b&gt;</option>',
    );
  });

  it("writes nothing for nowhere to move to", () => {
    expect(destinationOptions([])).toBe("");
  });
});

describe("movePickerHTML", () => {
  const html = movePickerHTML({
    message: "3 task(s) <code>bugfix</code> — move them to",
    destinations: [{ id: "qa", label: "QA" }],
    confirmLabel: "Move & delete",
    confirmVariant: "danger",
  });

  it("keeps the caller's markup in the message", () => {
    expect(html).toContain("<code>bugfix</code>");
  });

  it("gives both buttons the act the page wires", () => {
    expect(html).toContain('data-act="confirm"');
    expect(html).toContain('data-act="cancel"');
  });

  it("escapes the confirm label", () => {
    expect(html).toContain("Move &amp; delete");
  });

  it("takes the confirm variant from the caller and draws cancel as a ghost", () => {
    expect(html).toMatch(/class="btn btn-danger"[^>]*data-act="confirm"/);
    expect(html).toMatch(/class="btn btn-ghost"[^>]*data-act="cancel"/);
  });
});
