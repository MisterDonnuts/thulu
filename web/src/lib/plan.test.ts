import { describe, expect, it } from "vitest";

import {
  extraCapEditable,
  extraCapNow,
  hourQuota,
  parseExtraCap,
  planFacts,
  planName,
  trialEnds,
  unlimited,
  type Entitlements,
  type HourUsage,
} from "./plan";

const hours: HourUsage = {
  period: "2026-10",
  used_hours: 120,
  included_hours: 200,
  extra_hours: 0,
  extra_usd: 0,
  cap_usd: 10,
  ratio: 0.6,
  resets_at: "2026-11-01T00:00:00Z",
};

const starter: Entitlements = {
  plan: "starter",
  seat_limit: 4,
  concurrency_limit: 2,
  hour_quota_monthly: 200,
  storage_quota_bytes: 20 * 1024 ** 3,
  extra_hours_cap_usd: 10,
  hours,
  storage: { used_bytes: 3 * 1024 ** 3, quota_bytes: 20 * 1024 ** 3, remaining_bytes: 0 },
};

const selfHosted: Entitlements = {
  plan: "self-hosted",
  seat_limit: 0,
  concurrency_limit: 0,
  hour_quota_monthly: 0,
  storage_quota_bytes: 0,
};

function quota(h: Partial<HourUsage>) {
  const q = hourQuota({ ...starter, hours: { ...hours, ...h } });
  if (!q) throw new Error("no quota");
  return q;
}

describe("hourQuota", () => {
  it("reads used and included hours and the day they renew", () => {
    const q = quota({});
    expect(q.value).toBe("120 h of 200 h");
    expect(q.ratio).toBeCloseTo(0.6);
    expect(q.tone).toBe("ok");
    expect(q.sub).toBe("Resets Nov 1");
  });

  it("warns from 80% and shows the extra spend past the included hours", () => {
    expect(quota({ used_hours: 160, ratio: 0.8 }).tone).toBe("warn");
    const past = quota({ used_hours: 260, ratio: 1.3, extra_hours: 60, extra_usd: 3 });
    expect(past.tone).toBe("warn");
    expect(past.ratio).toBe(1);
    expect(past.sub).toBe("60 extra h · $3 of $10");
  });

  it("is full at the cap or past the hours with no extra allowed", () => {
    expect(quota({ used_hours: 1200, ratio: 6, extra_hours: 1000, extra_usd: 50 }).tone).toBe(
      "full",
    );
    expect(quota({ used_hours: 200, ratio: 1, cap_usd: 0 }).tone).toBe("full");
  });

  it("is null when the plan has no hour quota", () => {
    expect(hourQuota(selfHosted)).toBeNull();
    expect(hourQuota(null)).toBeNull();
  });
});

describe("plan names and trial", () => {
  it("reads legacy plans as their new names", () => {
    expect(planName("free")).toBe("Trial");
    expect(planName("business")).toBe("Team");
    expect(planName("inactive")).toBe("No active plan");
  });

  it("says when the trial ends, or that it has not started", () => {
    const trial = { ...starter, plan: "trial" };
    expect(
      trialEnds({ ...trial, hours: { ...hours, trial_ends_at: "2026-12-31T10:00:00Z" } }),
    ).toBe("Trial ends Dec 31");
    expect(trialEnds({ ...trial, hours: { ...hours, trial_ends_at: undefined } })).toMatch(
      /starts with the first run/,
    );
    expect(trialEnds(starter)).toBe("");
  });
});

describe("extra hours cap", () => {
  it("is editable only on plans that sell extra hours", () => {
    expect(extraCapEditable(starter)).toBe(true);
    expect(extraCapEditable({ ...starter, plan: "trial" })).toBe(false);
    expect(extraCapEditable(selfHosted)).toBe(false);
  });

  it("never promises a reset to a trial", () => {
    const trial = {
      ...starter,
      plan: "trial",
      hours: { ...hours, trial: true, included_hours: 50, used_hours: 30, ratio: 0.6, cap_usd: 0 },
    };
    expect(hourQuota(trial)?.sub).toBe("Hours for the whole trial");
  });

  it("explains the cap in hours", () => {
    expect(extraCapNow(starter)).toBe("Up to $10 of extra hours a month (200 h at $0.05).");
    expect(extraCapNow({ ...starter, hours: { ...hours, cap_usd: 0 } })).toMatch(/^No extra hours/);
  });

  it("parses dollars and refuses what the API would", () => {
    expect(parseExtraCap("120")).toEqual({ usd: 120 });
    expect(parseExtraCap("12,5")).toEqual({ usd: 12.5 });
    expect("error" in parseExtraCap("-1")).toBe(true);
    expect("error" in parseExtraCap("20000")).toBe(true);
    expect("error" in parseExtraCap("")).toBe(true);
  });
});

describe("planFacts", () => {
  it("lists each limit with what is used of it", () => {
    const facts = planFacts(starter);
    expect(facts.map((f) => f.id)).toEqual(["hours", "extra", "concurrency", "seats", "storage"]);
    expect(facts.map((f) => f.value)).toEqual([
      "120 h of 200 h",
      "$0.05/h, up to $10 a month",
      "2 at a time",
      "4 people",
      "3 GiB of 20 GiB",
    ]);
  });

  it("says no limit for a zero", () => {
    expect(planFacts(selfHosted).every((f) => f.value === "No limit")).toBe(true);
    expect(unlimited(selfHosted)).toBe(true);
    expect(unlimited(starter)).toBe(false);
  });
});
