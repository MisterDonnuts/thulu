import { describe, expect, it } from "vitest";
import { fmtDur, fmtLatency, fmtNum, fmtUSD } from "./metrics-format";
import {
  DEFAULT_UNITS,
  PUSHED_UNITS,
  fmtSignal,
  pushedSignals,
  sigAge,
  signalLabel,
  type SeriesPoint,
} from "./signals";

const f = { latency: fmtLatency, usd: fmtUSD, num: fmtNum };

describe("signalLabel", () => {
  it("names the signals it knows", () => {
    expect(signalLabel("latency_p95")).toBe("Latency p95");
    expect(signalLabel("error_rate")).toBe("Errors");
  });
  it("falls back to the raw id", () => {
    expect(signalLabel("queue_depth")).toBe("queue_depth");
  });
});

describe("fmtSignal", () => {
  it("scales seconds into the latency formatter", () => {
    expect(fmtSignal("latency_p95", 0.039, "s", f)).toBe("39.0ms");
  });
  it("takes the mapping's unit over the default", () => {
    expect(fmtSignal("latency_p95", 2, undefined, f)).toBe("2.0ms");
    expect(fmtSignal("latency_p95", 2, "s", f)).toBe("2.0s");
  });
  it("prints percentages, money and bytes", () => {
    expect(fmtSignal("error_rate", 1.25, "percent", f)).toBe("1.3%");
    expect(fmtSignal("cost_daily", 12.4, undefined, f)).toBe("$12.40");
    expect(fmtSignal("heap", 5_242_880, "bytes", f)).toBe("5 MB");
  });
  it("keeps a quiet service distinguishable from a dead one", () => {
    expect(fmtSignal("throughput", 0.04, "rps", f)).toBe("0.04/s");
    expect(fmtSignal("throughput", 12, "rps", f)).toBe("12/s");
  });
  it("falls back to a plain count with no unit anywhere", () => {
    expect(fmtSignal("restarts", 3, undefined, f)).toBe("3");
  });
  it("keeps the pushed table apart from the pulled one", () => {
    expect(PUSHED_UNITS.throughput).toBe("rps");
    expect(DEFAULT_UNITS.throughput).toBeUndefined();
    expect(PUSHED_UNITS.error_rate).toBe("percent");
    expect(DEFAULT_UNITS.error_rate).toBeUndefined();
  });
});

describe("sigAge", () => {
  const now = Date.parse("2026-07-31T12:00:00Z");
  it("rounds to the coarsest useful unit", () => {
    expect(sigAge("2026-07-31T11:59:30Z", now)).toBe("just now");
    expect(sigAge("2026-07-31T11:45:00Z", now)).toBe("15m ago");
    expect(sigAge("2026-07-31T09:00:00Z", now)).toBe("3h ago");
    expect(sigAge("2026-07-29T12:00:00Z", now)).toBe("2d ago");
  });
  it("never reads as being in the future", () => {
    expect(sigAge("2026-07-31T12:30:00Z", now)).toBe("just now");
  });
  it("says nothing for an unparseable bucket", () => {
    expect(sigAge("whenever", now)).toBe("");
  });
});

describe("pushedSignals", () => {
  const pts: SeriesPoint[] = [
    { service: "api", signal: "latency_p95", bucket: "2026-07-31T10:00:00Z", value: 1 },
    { service: "api", signal: "latency_p95", bucket: "2026-07-31T09:00:00Z", value: 2 },
    { service: "api", signal: "cpu", bucket: "2026-07-31T10:00:00Z", value: 3 },
    { service: "worker", signal: "cpu", bucket: "2026-07-31T10:00:00Z", value: 4 },
  ];

  it("drops every series a mapping already claims", () => {
    const out = pushedSignals({ mappings: [{ service: "api", signal: "cpu" }], points: pts });
    expect(out.map((g) => `${g.service}/${g.signal}`)).toEqual(["api/latency_p95", "worker/cpu"]);
  });
  it("sorts each group oldest first", () => {
    const out = pushedSignals({ mappings: [], points: pts });
    const latency = out.find((g) => g.signal === "latency_p95");
    expect(latency?.pts.map((p) => p.value)).toEqual([2, 1]);
  });
  it("sorts by service, then signal", () => {
    const out = pushedSignals({ mappings: [], points: pts });
    expect(out.map((g) => `${g.service}/${g.signal}`)).toEqual([
      "api/cpu",
      "api/latency_p95",
      "worker/cpu",
    ]);
  });
  it("keeps services apart when one name contains the other plus a space", () => {
    const out = pushedSignals({
      mappings: [],
      points: [
        { service: "a b", signal: "c", bucket: "2026-07-31T10:00:00Z", value: 1 },
        { service: "a", signal: "b c", bucket: "2026-07-31T10:00:00Z", value: 2 },
      ],
    });
    expect(out).toHaveLength(2);
  });
  it("returns nothing when there are no points", () => {
    expect(pushedSignals({ mappings: [], points: [] })).toEqual([]);
  });
});

describe("fmtDur against fmtSignal", () => {
  it("does not leak the wall-clock formatter into latency tiles", () => {
    expect(fmtDur(39)).toBe("0.0s");
    expect(fmtSignal("latency_p50", 39, undefined, f)).toBe("39.0ms");
  });
});
