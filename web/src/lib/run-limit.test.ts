import { describe, expect, it } from "vitest";
import { minutesLabel, parseRunLimit, runLimitHelp, runLimitNow, type RunLimit } from "./run-limit";

const base: RunLimit = { value: null, default: 60, ceiling: 240, effective: 60 };

describe("run limit", () => {
  it("names no limit for 0", () => {
    expect(minutesLabel(0)).toBe("no limit");
    expect(minutesLabel(1)).toBe("1 minute");
    expect(minutesLabel(90)).toBe("90 minutes");
  });

  it("says the default and the ceiling under the field", () => {
    expect(runLimitHelp(base)).toContain("60 minutes");
    expect(runLimitHelp(base)).toContain("240 minutes");
    expect(runLimitHelp({ ...base, default: 0, effective: 0 })).toContain("no limit");
  });

  it("says where the limit comes from", () => {
    expect(runLimitNow(base)).toContain("default");
    expect(runLimitNow({ ...base, value: 180, effective: 180 })).toContain("180 minutes");
    const capped = runLimitNow({ ...base, value: 300, ceiling: 120, effective: 120 });
    expect(capped).toContain("300 minutes");
    expect(capped).toContain("120 minutes");
  });

  it("takes whole minutes from 1 to the ceiling", () => {
    expect(parseRunLimit(" 45 ", 240)).toEqual({ minutes: 45 });
    expect(parseRunLimit("240", 240)).toEqual({ minutes: 240 });
    for (const bad of ["", "0", "-3", "241", "1.5", "abc"]) {
      expect(parseRunLimit(bad, 240), bad).toHaveProperty("error");
    }
  });
});
