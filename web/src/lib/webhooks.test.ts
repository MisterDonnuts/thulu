import { describe, expect, it } from "vitest";

import {
  canRetryDelivery,
  deliveriesTableHTML,
  deliveryColumns,
  deliveriesPending,
  deliveryResult,
  deliveryState,
  eventDescription,
  legacySigningNote,
  secretRevealLabel,
  testResultToast,
  validateWebhookForm,
} from "./webhooks";

describe("legacySigningNote", () => {
  it("says nothing when every endpoint signs the documented way", () => {
    expect(legacySigningNote(0)).toBeNull();
  });

  it("counts the endpoints that still need a rotation", () => {
    expect(legacySigningNote(1)).toMatch(/^One endpoint signs with a key derived/);
    expect(legacySigningNote(3)).toMatch(/^3 endpoints sign with a key derived/);
  });
});

describe("secretRevealLabel", () => {
  it("names the endpoint and whether the secret is a rotation", () => {
    expect(secretRevealLabel("https://a.example/h", false)).toBe(
      "Signing secret for https://a.example/h. Copy it now: it will not be shown again.",
    );
    expect(secretRevealLabel("https://a.example/h", true)).toMatch(/^New signing secret for/);
  });
});

describe("eventDescription", () => {
  it("explains every event the daemon emits", () => {
    for (const ev of [
      "task.created",
      "task.stage_changed",
      "task.deleted",
      "chat.message",
      "stages.changed",
      "boards.changed",
      "task_types.changed",
      "question.asked",
      "question.answered",
      "question.canceled",
      "approval.requested",
      "approval.decided",
    ]) {
      expect(eventDescription(ev), ev).not.toBe("");
    }
  });

  it("says nothing about an event it does not know", () => {
    expect(eventDescription("billing.charged")).toBe("");
  });
});

describe("testResultToast", () => {
  it("reports the status and time of a delivered test", () => {
    expect(testResultToast({ success: true, status_code: 204, duration_ms: 87 })).toEqual({
      ok: true,
      text: "Test delivery OK: HTTP 204 in 87 ms",
    });
  });

  it("names the status a receiver answered with", () => {
    expect(testResultToast({ success: false, status_code: 502 }).text).toBe(
      "Test delivery failed: HTTP 502",
    );
  });

  it("says why a delivery got no response instead of an empty status", () => {
    const r = testResultToast({
      success: false,
      status_code: 0,
      error: "Could not connect: ci.example.com did not resolve",
    });
    expect(r.text).toBe("Test delivery failed. Could not connect: ci.example.com did not resolve");
    expect(r.text).not.toContain("undefined");
    expect(testResultToast({ success: false }).text).toBe("Test delivery failed");
  });
});

describe("delivery series", () => {
  const base = {
    id: "d1",
    event: "task.created",
    status_code: 0,
    attempt: 2,
    duration_ms: 10,
    delivered: false,
    created_at: "",
  };
  it("offers Retry only where the daemon says it is safe", () => {
    expect(canRetryDelivery({ ...base, state: "retrying", retryable: false })).toBe(false);
    expect(canRetryDelivery({ ...base, state: "retried", retryable: false })).toBe(false);
    expect(canRetryDelivery({ ...base, state: "failed", retryable: true })).toBe(true);
    expect(canRetryDelivery(base)).toBe(true);
    expect(canRetryDelivery({ ...base, delivered: true })).toBe(false);
  });

  it("says whether more attempts are coming", () => {
    expect(deliveryState({ ...base, state: "retrying" })).toBe("Will retry");
    expect(deliveryState({ ...base, state: "retried" })).toBe("Another attempt followed");
    expect(deliveryState({ ...base, state: "failed" })).toBe("Failed; no more attempts");
    expect(deliveryState({ ...base, delivered: true })).toBe("Delivered");
  });

  it("keeps watching while an attempt is pending", () => {
    expect(deliveriesPending([{ ...base, state: "retrying" }])).toBe(true);
    expect(deliveriesPending([{ ...base, state: "failed" }])).toBe(false);
  });
});

describe("deliveryResult", () => {
  const base = {
    id: "d1",
    event: "task.created",
    attempt: 1,
    duration_ms: 10,
    delivered: false,
    created_at: "",
  };
  it("shows the status, or the recorded reason when there is none", () => {
    expect(deliveryResult({ ...base, status_code: 500 })).toBe("HTTP 500");
    expect(
      deliveryResult({
        ...base,
        status_code: 0,
        error: "Could not connect: x refused the connection",
      }),
    ).toBe("Could not connect: x refused the connection");
    expect(deliveryResult({ ...base, status_code: 0 })).toBe("No response");
  });
});

describe("validateWebhookForm", () => {
  it("passes a real URL with at least one event", () => {
    expect(validateWebhookForm("https://example.com/hooks", ["*"])).toBeNull();
  });

  it("asks for the URL first", () => {
    expect(validateWebhookForm("", [])).toBe("URL is required.");
  });

  it("rejects something that is not a URL", () => {
    expect(validateWebhookForm("example.com/hooks", ["*"])).toBe("Enter a valid URL.");
  });

  it("rejects an empty event selection", () => {
    expect(validateWebhookForm("https://example.com", [])).toBe("Select at least one event.");
  });
});

describe("deliveriesTableHTML", () => {
  const base = {
    event: "task.created",
    status_code: 0,
    attempt: 3,
    duration_ms: 120,
    created_at: "2026-10-01T12:00:00Z",
  };

  it("marks a failed attempt and offers Retry only where it is safe", () => {
    const html = deliveriesTableHTML([
      { ...base, id: "d1", delivered: false, retryable: true, state: "failed" },
      { ...base, id: "d2", delivered: true, status_code: 200, state: "delivered" },
    ]);
    const [failed, ok] = html.split("</tr>").filter((r) => r.includes("<td"));
    expect(failed).toContain('data-tone="danger"');
    expect(failed).toContain('class="btn btn-sm wh-retry-btn"');
    expect(failed).toContain('data-id="d1"');
    expect(ok).not.toContain("data-tone");
    expect(ok).not.toContain("wh-retry-btn");
  });

  it("keeps one header cell per column, the actions one hidden from sight", () => {
    const html = deliveriesTableHTML([]);
    expect(html.match(/<th[ >]/g)).toHaveLength(deliveryColumns().length);
    expect(html).toContain("visually-hidden");
  });
});
