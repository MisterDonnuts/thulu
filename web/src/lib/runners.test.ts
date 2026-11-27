import { afterEach, describe, expect, it, vi } from "vitest";

import { api, ApiError } from "./api";
import {
  activeRunners,
  admissionLines,
  loadRunnersOrOff,
  localToolsLines,
  mixWarning,
  revokeMessage,
  runnerBadgeHTML,
  runnerStatus,
  setupCommands,
  withWs,
  type Runner,
} from "./runners";

const NOW = Date.parse("2026-11-14T12:00:00Z");

function rn(over: Partial<Runner> = {}): Runner {
  return { id: "r1", name: "mac", created_at: "2026-11-01T00:00:00Z", online: false, ...over };
}

describe("runnerStatus", () => {
  it("puts revoked ahead of online", () => {
    expect(runnerStatus(rn({ online: true, revoked_at: "2026-11-02T00:00:00Z" }), NOW).tone).toBe(
      "danger",
    );
  });
  it("says online for a runner holding a claim", () => {
    expect(runnerStatus(rn({ online: true }), NOW).tone).toBe("ok");
  });
  it("tells a runner that never connected from one seen before", () => {
    const never = runnerStatus(rn(), NOW).label;
    const seen = runnerStatus(rn({ last_seen_at: "2026-11-14T10:00:00Z" }), NOW).label;
    expect(never).not.toBe(seen);
    expect(seen).not.toBe("");
  });
});

describe("activeRunners", () => {
  it("drops revoked runners", () => {
    const list = [rn({ id: "a" }), rn({ id: "b", revoked_at: "2026-11-02T00:00:00Z" })];
    expect(activeRunners(list).map((r) => r.id)).toEqual(["a"]);
  });
});

describe("setupCommands", () => {
  it("registers against the given daemon with the token", () => {
    const c = setupCommands("tok123", "https://thulu.example");
    expect(c.register).toBe("thulu runner register -url https://thulu.example -token tok123");
    expect(c.allow).toContain("thulu runner allow");
    expect(c.start).toContain("thulu runner");
  });
});

describe("withWs", () => {
  it("adds the workspace only when there is one", () => {
    expect(withWs("/runners")).toBe("/runners");
    expect(withWs("/runners", "w 1")).toBe("/runners?workspace=w%201");
    expect(withWs("/runners?x=1", "w")).toBe("/runners?x=1&workspace=w");
  });
});

describe("mixWarning", () => {
  const list = [rn({ id: "a", name: "mac" }), rn({ id: "b", name: "linux" })];
  it("stays quiet for projects in Thulu only or on one machine", () => {
    expect(mixWarning([{ name: "x" }, { name: "y" }], list)).toBe("");
    expect(
      mixWarning(
        [
          { name: "x", runner_id: "a" },
          { name: "y", runner_id: "a" },
        ],
        list,
      ),
    ).toBe("");
  });
  it("stays quiet when a machine project meets a Thulu one", () => {
    expect(mixWarning([{ name: "local", runner_id: "a" }, { name: "cloud" }], list)).toBe("");
  });
  it("names only the machines when two machines meet a Thulu project", () => {
    const w = mixWarning(
      [{ name: "x", runner_id: "a" }, { name: "y", runner_id: "b" }, { name: "cloud" }],
      list,
    );
    expect(w).toContain("mac");
    expect(w).toContain("linux");
    expect(w).not.toContain("cloud");
  });
  it("names the machines when projects sit on two", () => {
    const w = mixWarning(
      [
        { name: "x", runner_id: "a" },
        { name: "y", runner_id: "b" },
      ],
      list,
    );
    expect(w).toContain("mac");
    expect(w).toContain("linux");
  });
});

describe("runnerBadgeHTML", () => {
  it("is empty for a project in Thulu", () => {
    expect(runnerBadgeHTML(undefined, [])).toBe("");
  });
  it("names the machine and escapes it", () => {
    const html = runnerBadgeHTML("a", [rn({ id: "a", name: "<b>mac</b>" })]);
    expect(html).toContain("&lt;b&gt;mac&lt;/b&gt;");
    expect(html).not.toContain("<b>");
  });
  it("still shows for a runner it doesn't know", () => {
    expect(runnerBadgeHTML("gone", [])).not.toBe("");
  });
});

describe("loadRunnersOrOff", () => {
  afterEach(() => vi.restoreAllMocks());

  it("reads runners off only from a 503", async () => {
    vi.spyOn(api, "get").mockRejectedValue(new ApiError(503, "runners are off"));
    expect(await loadRunnersOrOff()).toBeNull();
  });

  // A failed request must not hide "Login on a machine" or blame the runners on every row.
  it("reads any other failure as no machine known", async () => {
    vi.spyOn(api, "get").mockRejectedValue(new ApiError(500, "boom"));
    expect(await loadRunnersOrOff()).toEqual([]);
    vi.spyOn(api, "get").mockRejectedValue(new TypeError("network"));
    expect(await loadRunnersOrOff()).toEqual([]);
  });

  it("returns the list when it loads", async () => {
    vi.spyOn(api, "get").mockResolvedValue([rn()]);
    expect(await loadRunnersOrOff()).toEqual([rn()]);
  });
});

describe("localToolsLines", () => {
  it("says nothing for a runner that has not reported it", () => {
    expect(localToolsLines(undefined)).toEqual([]);
  });

  it("names the selected MCPs and where the setting lives", () => {
    const [line] = localToolsLines({ mode: "selected", mcp: ["pg", "claude.ai Slack"] });
    expect(line).toContain("Thulu's MCPs plus pg, claude.ai Slack");
    expect(line).toContain("thulu runner config local-tools");
  });

  it("tells thulu and all apart", () => {
    expect(localToolsLines({ mode: "thulu" })[0]).toContain("only Thulu's MCPs");
    expect(localToolsLines({ mode: "all" })[0]).toContain("every MCP the machine has");
  });

  it("warns when a policy turns bypass off with no allowed tools", () => {
    const lines = localToolsLines({ mode: "thulu", bypass_disabled: true, allowed_tools: 0 });
    expect(lines).toHaveLength(2);
    expect(lines[1]).toContain("allowed_tools is empty");
    expect(
      localToolsLines({ mode: "thulu", bypass_disabled: true, allowed_tools: 2 })[1],
    ).toContain("only what allowed_tools lists");
  });
});

describe("admissionLines", () => {
  it("says why a runner takes no runs", () => {
    expect(
      admissionLines({ state: "paused_low_memory", available_mb: 1229, min_free_mb: 2048 }),
    ).toEqual(["Not taking runs: 1.2 GB free, needs 2 GB."]);
  });

  it("says nothing while it takes runs, or before it reported", () => {
    expect(admissionLines({ state: "accepting", available_mb: 9000, min_free_mb: 2048 })).toEqual(
      [],
    );
    expect(admissionLines(undefined)).toEqual([]);
  });
});

describe("revokeMessage", () => {
  const profiles = [
    { name: "on mac", runner_id: "r1", is_default: true },
    { name: "mac fast", runner_id: "r1" },
    { name: "linux", runner_id: "r2" },
    { name: "keyed" },
  ];

  it("warns that profiles on the machine stop running", () => {
    expect(revokeMessage("r9", profiles)).toContain("AI-engine profiles on it are refused");
  });

  it("names the profiles on the machine and flags an engine default", () => {
    const msg = revokeMessage("r1", profiles);
    expect(msg).toContain("Profiles on it: on mac (default), mac fast.");
    expect(msg).not.toContain("linux");
    expect(msg).not.toContain("keyed");
  });
});
