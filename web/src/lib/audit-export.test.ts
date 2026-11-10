import { describe, expect, it } from "vitest";

import {
  AUDIT_PAGE,
  auditExportParams,
  auditWindowLimit,
  filenameFromDisposition,
  mergeAuditPage,
} from "./audit-export";

describe("auditWindowLimit", () => {
  it("asks for a full page before anything is loaded", () => {
    expect(auditWindowLimit(0)).toBe(AUDIT_PAGE);
  });

  it("asks for the whole window once the operator has paged in", () => {
    expect(auditWindowLimit(340)).toBe(340);
  });

  it("stops at the server's cap", () => {
    expect(auditWindowLimit(5000)).toBe(1000);
  });
});

describe("mergeAuditPage", () => {
  it("appends the page", () => {
    expect(mergeAuditPage([{ id: "a" }], [{ id: "b" }])).toEqual([{ id: "a" }, { id: "b" }]);
  });

  it("drops entries the previous page already showed", () => {
    const merged = mergeAuditPage([{ id: "a" }, { id: "b" }], [{ id: "b" }, { id: "c" }]);
    expect(merged.map((e) => e.id)).toEqual(["a", "b", "c"]);
  });

  it("keeps the row already on screen rather than the copy", () => {
    const first = { id: "a", summary: "on screen" };
    expect(mergeAuditPage([first], [{ id: "a", summary: "refetched" }])[0]).toBe(first);
  });
});

describe("auditExportParams", () => {
  it("sends the format alone when no filter is set", () => {
    expect(auditExportParams("csv", "").toString()).toBe("format=csv");
  });

  it("carries the kind filter the table is showing", () => {
    expect(auditExportParams("json", "task").toString()).toBe("format=json&kind=task");
  });
});

describe("filenameFromDisposition", () => {
  it("falls back when there is no header", () => {
    expect(filenameFromDisposition(null, "audit.csv")).toBe("audit.csv");
    expect(filenameFromDisposition("inline", "audit.csv")).toBe("audit.csv");
  });

  it("reads a plain filename, quoted or not", () => {
    expect(filenameFromDisposition('attachment; filename="a b.csv"', "x")).toBe("a b.csv");
    expect(filenameFromDisposition("attachment; filename=a.csv", "x")).toBe("a.csv");
  });

  it("prefers the RFC 5987 form and decodes it", () => {
    expect(
      filenameFromDisposition("attachment; filename=a.csv; filename*=UTF-8''cr%C3%B3nica.csv", "x"),
    ).toBe("crónica.csv");
  });

  it("keeps a percent sequence that does not decode", () => {
    expect(filenameFromDisposition("attachment; filename*=UTF-8''100%.csv", "x")).toBe("100%.csv");
  });
});
