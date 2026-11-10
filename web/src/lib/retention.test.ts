import { describe, expect, it } from "vitest";

import {
  checkRetention,
  daysText,
  diskLine,
  diskLow,
  parseDays,
  refusedRetentionKey,
  retentionProblemText,
  retentionRowsHTML,
  sweepLine,
  windowLabel,
  type RetentionView,
} from "./retention";

const view: RetentionView = {
  windows: [
    { key: "retention.run_logs_days", value: 14, default: 30 },
    { key: "audit.retention_days", value: 0, default: 0 },
  ],
  disk_floor: { key: "janitor.disk_free_min_percent", value: 10, default: 10 },
  last_sweep: "2026-10-16T09:00:00Z",
  sweep_every_sec: 3600,
  disk_free_percent: 42.7,
};

describe("retention rows", () => {
  it("shows each window with its value and its default", () => {
    const html = retentionRowsHTML(view);
    expect(html).toContain("Run logs and transcripts");
    expect(html).toContain('value="14"');
    expect(html).toContain("default: 30 days");
    expect(html).toContain("default: forever");
    expect(html).toContain('data-key="janitor.disk_free_min_percent"');
    expect(html).toContain("default: 10%");
  });

  it("labels an unknown key as itself", () => {
    expect(windowLabel("retention.new_days")).toBe("retention.new_days");
  });

  it("reads 0 as forever", () => {
    expect(daysText(0)).toBe("forever");
    expect(daysText(1)).toBe("1 day");
  });
});

describe("parseDays", () => {
  it("takes whole numbers, empty as the default, and refuses the rest", () => {
    expect(parseDays("7", 30)).toBe(7);
    expect(parseDays(" ", 30)).toBe(30);
    expect(parseDays("0", 30)).toBe(0);
    expect(parseDays("-1", 30)).toBeNull();
    expect(parseDays("1.5", 30)).toBeNull();
  });
});

describe("checkRetention", () => {
  const rows = [
    { key: "retention.run_logs_days", raw: "14", default: 30, value: 14 },
    { key: "retention.sessions_days", raw: "", default: 30, value: 7 },
    { key: "janitor.disk_free_min_percent", raw: "15", default: 10, value: 10 },
  ];
  it("returns only what changed, empty as the default", () => {
    expect(checkRetention(rows)).toEqual({
      ok: true,
      changes: { "retention.sessions_days": 30, "janitor.disk_free_min_percent": 15 },
    });
  });

  it("marks every wrong row, each in its unit", () => {
    const bad = [
      { ...rows[0], raw: "-1" },
      { ...rows[1], raw: "1.5" },
      { ...rows[2], raw: "365" },
    ];
    expect(checkRetention(bad)).toEqual({
      ok: false,
      problems: {
        "retention.run_logs_days": "days",
        "retention.sessions_days": "days",
        "janitor.disk_free_min_percent": "percent",
      },
    });
    expect(retentionProblemText("percent")).toContain("100");
    expect(retentionProblemText("days")).toContain("days");
  });

  it("names the row a refusal is about, when the form has it", () => {
    const keys = rows.map((r) => r.key);
    expect(refusedRetentionKey({ setting: "retention.sessions_days" }, keys)).toBe(
      "retention.sessions_days",
    );
    expect(refusedRetentionKey({ setting: "sandbox.max" }, keys)).toBeNull();
    expect(refusedRetentionKey("nope", keys)).toBeNull();
  });

  it("caps the disk floor input at 100", () => {
    expect(retentionRowsHTML(view)).toMatch(/data-key="janitor.disk_free_min_percent"/);
    expect(retentionRowsHTML(view)).toContain('max="100"');
  });
});

describe("janitor lines", () => {
  it("says when it last ran and how often", () => {
    expect(sweepLine(view)).toMatch(/^Last pass: .+\. It runs every hour\.$/);
    expect(sweepLine({ ...view, last_sweep_pressure: true })).toContain("disk was low");
    expect(sweepLine({ ...view, last_sweep: undefined })).toContain("has not finished a pass");
  });

  it("compares free space to the floor", () => {
    expect(diskLine(view)).toContain("Free now: 42%");
    expect(diskLow(view)).toBe(false);
    expect(diskLow({ ...view, disk_free_percent: 4 })).toBe(true);
    expect(diskLine({ ...view, disk_floor: { ...view.disk_floor, value: 0 } })).toContain(
      "No disk floor",
    );
    expect(diskLine({ ...view, disk_free_percent: undefined })).toContain("could not be measured");
  });
});
