import { describe, expect, it } from "vitest";

import { planFacts, planName, runQuota, unlimited, type Entitlements } from "./plan";

const team: Entitlements = {
  plan: "team",
  seat_limit: 20,
  concurrency_limit: 5,
  run_quota_monthly: 1000,
  storage_quota_bytes: 20 * 1024 ** 3,
  runs: {
    period: "2026-10",
    used: 312,
    limit: 1000,
    remaining: 688,
    resets_at: "2026-11-01T00:00:00Z",
  },
  storage: { used_bytes: 3 * 1024 ** 3, quota_bytes: 20 * 1024 ** 3, remaining_bytes: 0 },
};

const selfHosted: Entitlements = {
  plan: "self-hosted",
  seat_limit: 0,
  concurrency_limit: 0,
  run_quota_monthly: 0,
  storage_quota_bytes: 0,
  runs: { period: "", used: 0, limit: 0, remaining: -1, resets_at: "0001-01-01T00:00:00Z" },
};

function quota(e: Entitlements) {
  const q = runQuota(e);
  if (!q) throw new Error("no quota");
  return q;
}

const teamRuns = team.runs ?? { period: "", used: 0, limit: 0, remaining: 0, resets_at: "" };

describe("runQuota", () => {
  it("reads used, limit and the day it resets", () => {
    const q = quota(team);
    expect(q.used).toBe(312);
    expect(q.limit).toBe(1000);
    expect(q.ratio).toBeCloseTo(0.312);
    expect(q.tone).toBe("ok");
    expect(q.resets).toBe("Resets Nov 1");
    expect(q.value).toBe("312 of 1,000");
  });

  it("warns from 80% and is full at the limit", () => {
    const at = (used: number) => quota({ ...team, runs: { ...teamRuns, used } });
    expect(at(800).tone).toBe("warn");
    expect(at(1000).tone).toBe("full");
    expect(at(1200).ratio).toBe(1);
  });

  it("is null when the plan has no quota", () => {
    expect(runQuota(selfHosted)).toBeNull();
    expect(runQuota(null)).toBeNull();
  });
});

describe("planFacts", () => {
  it("lists each limit with what is used of it", () => {
    const facts = planFacts(team);
    expect(facts.map((f) => f.id)).toEqual(["runs", "concurrency", "seats", "storage"]);
    expect(facts.map((f) => f.value)).toEqual([
      "312 of 1,000",
      "5 at a time",
      "20 people",
      "3 GiB of 20 GiB",
    ]);
  });

  it("says no limit for a zero", () => {
    expect(planFacts(selfHosted).every((f) => f.value === "No limit")).toBe(true);
    expect(unlimited(selfHosted)).toBe(true);
    expect(unlimited(team)).toBe(false);
  });
});

describe("planName", () => {
  it("names the known plans and passes others through", () => {
    expect(planName("team")).toBe("Team");
    expect(planName("self-hosted")).toBe("Self-hosted");
    expect(planName("custom-deal")).toBe("custom-deal");
  });
});
