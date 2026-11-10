import { describe, expect, it } from "vitest";

import {
  areaColor,
  areaLegend,
  areaSegments,
  areaTone,
  fmtDuration,
  fmtGrowth,
  fmtHitRate,
  isEmptyReport,
  namesOf,
  partialSince,
  projectTone,
  signalText,
  signalTone,
  sortProjects,
  stackedBars,
  stackLegend,
  type CacheDay,
  type CacheProject,
  type CacheReport,
  type CacheThresholds,
  type CacheWorkspace,
  cacheProjectColumns,
  cacheToolColumns,
  cacheWorkspaceColumns,
} from "./infra-cache";

const TH: CacheThresholds = {
  growth_pct: 50,
  capacity_pct: 85,
  low_hit_rate: 0.5,
  low_hit_min_runs: 5,
  prewarm_failures: 3,
  key_churn_keys: 5,
  key_churn_days: 7,
  tool_unused_days: 30,
};

function day(date: string, over: Partial<CacheDay> = {}): CacheDay {
  return {
    date,
    skipped: 0,
    restored: 0,
    ran: 0,
    failed: 0,
    none: 0,
    prewarms: 0,
    prewarms_failed: 0,
    tool_downloads: 0,
    ...over,
  };
}

function project(over: Partial<CacheProject>): CacheProject {
  return {
    project_id: "p",
    workspace_id: "ws",
    runs: 0,
    with_setup: 0,
    hits: 0,
    hit_rate: null,
    statuses: {},
    tool_downloads: 0,
    prewarms: 0,
    prewarms_failed: 0,
    last_run_at: null,
    avg_setup_ms: 0,
    snapshot_bytes: 0,
    snapshot_count: 0,
    distinct_keys: 0,
    recent_keys: 0,
    last_prewarm: null,
    prewarm_fail_streak: 0,
    ...over,
  };
}

function report(over: Partial<CacheReport> = {}): CacheReport {
  return {
    range: "7d",
    since: "2026-10-01T00:00:00Z",
    generated_at: "2026-10-07T12:00:00Z",
    log_since: null,
    totals: {
      runs: 0,
      with_setup: 0,
      hits: 0,
      hit_rate: null,
      statuses: {},
      tool_downloads: 0,
      prewarms: 0,
      prewarms_failed: 0,
      no_cache: 0,
      runs_downloading: 0,
      avg_ms: {},
      avg_ran_setup_ms: 0,
      time_saved_ms: 0,
      cache_bytes: 0,
      fullest_pct: null,
    },
    daily: [],
    workspaces: [],
    projects: [],
    tools: [],
    signals: [],
    thresholds: TH,
    ...over,
  };
}

describe("figures", () => {
  it("prints a hit rate, a dash when nothing had a setup phase", () => {
    expect(fmtHitRate(0.5)).toBe("50%");
    expect(fmtHitRate(0.075)).toBe("7.5%");
    expect(fmtHitRate(null)).toBe("—");
  });

  it("prints durations in seconds, minutes and hours", () => {
    expect(fmtDuration(0)).toMatch(/^0\s?s/);
    expect(fmtDuration(45_400)).toMatch(/^45\s?s/);
    expect(fmtDuration(200_000)).toMatch(/^3\s?m/);
    expect(fmtDuration(7_500_000)).toMatch(/^2\s?h/);
  });

  it("signs the growth and leaves a dash without a baseline", () => {
    expect(fmtGrowth(0, null)).toBe("—");
    expect(fmtGrowth(1024 ** 3, 40)).toBe("+1 GiB (+40%)");
    expect(fmtGrowth(-512 * 1024 ** 2, -20)).toBe("−512 MiB (−20%)");
    expect(fmtGrowth(2048, null)).toBe("+2 KiB");
  });

  it("calls a report empty only when nothing at all is there", () => {
    expect(isEmptyReport(report())).toBe(true);
    expect(isEmptyReport(report({ projects: [project({})] }))).toBe(false);
    const r = report();
    r.totals.prewarms = 1;
    expect(isEmptyReport(r)).toBe(false);
  });

  it("notes a log that starts inside the range", () => {
    expect(partialSince(report({ log_since: "2026-10-04T10:00:00Z" }))).toBe(
      "2026-10-04T10:00:00Z",
    );
    expect(partialSince(report({ log_since: "2026-09-01T10:00:00Z" }))).toBeNull();
    expect(partialSince(report())).toBeNull();
  });
});

describe("stackedBars", () => {
  it("stacks each day's statuses bottom up and titles every bar", () => {
    const svg = stackedBars(
      [day("2026-10-06", { skipped: 2, restored: 1, ran: 1, prewarms: 1 }), day("2026-10-07")],
      { width: 400, height: 200 },
    );
    expect(svg.match(/<g class="chart-bar">/g)).toHaveLength(2);
    expect(svg.match(/fill="var\(--/g)).toHaveLength(3);
    expect(svg).toContain("2 already there, 1 from snapshot, 1 installed, 0 failed, 1 prewarms");
    const ys = [...svg.matchAll(/<rect x="[\d.]+" y="([\d.]+)"[^>]*fill="var/g)].map((m) =>
      Number(m[1]),
    );
    expect(ys[0]).toBeGreaterThan(ys[1]);
    expect(ys[1]).toBeGreaterThan(ys[2]);
    const ticks = [...svg.matchAll(/<text class="chart-tick" x="30.0"[^>]*>([^<]*)</g)].map(
      (m) => m[1],
    );
    expect(ticks).toEqual(["0", "2", "4"]);
  });

  it("draws an empty range without dividing by zero", () => {
    const svg = stackedBars([], { width: 300, height: 200 });
    expect(svg).toContain("<svg");
    expect(svg).not.toContain("NaN");
  });
});

describe("areaSegments", () => {
  it("orders areas, folds unknown ones into other and drops empty ones", () => {
    const segs = areaSegments({ other: 10, npm: 50, mise: 40, pip: 0, cargo: 0, bun: 0 }, 100);
    expect(segs.map((s) => s.area)).toEqual(["npm", "mise", "other"]);
    expect(segs.map((s) => s.pct)).toEqual([50, 40, 10]);
    expect(areaSegments({ npm: 1, deno: 3 }, 0).map((s) => [s.area, s.pct])).toEqual([
      ["npm", 25],
      ["other", 75],
    ]);
    expect(areaSegments(null, 0)).toEqual([]);
  });
});

describe("areaTone", () => {
  it("gives each area its series in order, and an unknown one other's", () => {
    expect(areaTone("npm")).toBe("viz-1");
    expect(areaTone("snapshots")).toBe("viz-6");
    expect(areaTone("other")).toBe("viz-7");
    expect(areaTone("deno")).toBe("viz-7");
    expect(areaColor("go_mod")).toBe("var(--viz-2)");
    expect(areaColor("deno")).toBe("var(--viz-7)");
  });
});

describe("projects", () => {
  it("rates a project by its prewarm streak first, then its hit rate", () => {
    expect(projectTone(project({ prewarm_fail_streak: 3, hit_rate: 1, with_setup: 9 }), TH)).toBe(
      "danger",
    );
    expect(projectTone(project({ hit_rate: 0.2, with_setup: 6 }), TH)).toBe("warn");
    expect(projectTone(project({ hit_rate: 0.2, with_setup: 2 }), TH)).toBe("ok");
    expect(projectTone(project({}), TH)).toBe("");
  });

  it("puts the busiest first and snapshot-only projects by size", () => {
    const list = sortProjects([
      project({ project_id: "a", snapshot_bytes: 5 }),
      project({ project_id: "b", runs: 3 }),
      project({ project_id: "c", snapshot_bytes: 50 }),
    ]);
    expect(list.map((p) => p.project_id)).toEqual(["b", "c", "a"]);
  });
});

describe("signals", () => {
  const r = report({
    workspaces: [{ workspace_id: "ws1", workspace_name: "Acme" } as CacheWorkspace],
    projects: [project({ project_id: "p1", project_name: "web", workspace_id: "ws1" })],
  });
  const names = namesOf(r);

  it("names workspaces and projects, falling back to the id", () => {
    expect(names.workspace("ws1")).toBe("Acme");
    expect(names.workspace("ws9")).toBe("ws9");
    expect(names.workspace("default")).toBe("Default workspace");
    expect(names.project("p1")).toBe("web");
  });

  it("writes one sentence per kind, with the threshold", () => {
    expect(
      signalText(
        { kind: "low_hit_rate", workspace_id: "ws1", project_id: "p1", value: 20, threshold: 50 },
        names,
        TH,
      ),
    ).toBe("web (Acme): 20% of installs came from the cache (threshold 50%)");
    expect(
      signalText(
        { kind: "cache_full", workspace_id: "ws1", value: 91.5, threshold: 85 },
        names,
        TH,
      ),
    ).toBe("Acme: the cache disk is 92% full (threshold 85%)");
    expect(
      signalText(
        { kind: "prewarm_failing", workspace_id: "ws1", project_id: "p1", value: 4, threshold: 3 },
        names,
        TH,
      ),
    ).toBe("web (Acme): the last 4 prewarms failed");
    expect(
      signalText(
        { kind: "key_churn", workspace_id: "ws1", project_id: "p1", value: 8, threshold: 5 },
        names,
        TH,
      ),
    ).toContain("8 different setup keys in 7 days");
    expect(
      signalText({ kind: "tool_unused", tool: "node@20.1.0", value: 41, threshold: 30 }, names, TH),
    ).toBe("node@20.1.0: cached, and no run asked for it in 41 days");
    expect(
      signalText({ kind: "tool_unused", tool: "go@1.22.0", value: -1, threshold: 30 }, names, TH),
    ).toBe("go@1.22.0: cached, and no run in the log asked for it");
  });

  it("marks what breaks installs or fills the disk as danger", () => {
    expect(signalTone({ kind: "cache_full", value: 90, threshold: 85 })).toBe("danger");
    expect(signalTone({ kind: "prewarm_failing", value: 3, threshold: 3 })).toBe("danger");
    expect(signalTone({ kind: "key_churn", value: 6, threshold: 5 })).toBe("warn");
  });
});

describe("cache table columns", () => {
  it("keep the order the renderers write their cells in", () => {
    expect(cacheProjectColumns().map((c) => c.label)).toEqual([
      "Project",
      "Installs",
      "Hit rate",
      "Last",
      "Full install",
      "Snapshots",
      "Prewarm",
    ]);
    expect(cacheWorkspaceColumns()).toHaveLength(6);
    expect(cacheWorkspaceColumns()[2].num).toBeUndefined();
    expect(cacheToolColumns()[0].mono).toBe(true);
    expect(cacheToolColumns()[3].num).toBe(true);
  });
});

describe("legends", () => {
  it("keys the bars by status, square, in the order they stack", () => {
    const got = stackLegend();
    expect(got.map((i) => i.color)).toEqual([
      "var(--ok)",
      "var(--accent)",
      "var(--warn)",
      "var(--danger)",
    ]);
    expect(got.every((i) => i.swatch === "square" && i.label !== "")).toBe(true);
  });
  it("keys only the areas a bar shows, in AREA_ORDER", () => {
    const got = areaLegend(new Set(["other", "npm", "deno"]));
    expect(got.map((i) => i.color)).toEqual(["var(--viz-1)", "var(--viz-7)"]);
    expect(got.every((i) => i.swatch === "square")).toBe(true);
    expect(areaLegend(new Set())).toEqual([]);
  });
});
