import { describe, expect, it } from "vitest";

import { tableRowHTML } from "./ui/table";
import {
  attributedCost,
  delegationAgentCells,
  delegationRowCells,
  delegationStatusTone,
  delegationTiles,
  mcpServerCells,
  optimizerCells,
  optimizerLevelName,
  sortOptimizerRows,
  type DelegationRow,
  type DelegationsReport,
  type OptimizerLevelRow,
} from "./usage-optimizer";
import {
  delegationAgentColumns,
  delegationRecentColumns,
  mcpServerColumns,
  optimizerColumns,
} from "./usage-tables";

describe("attributedCost", () => {
  it("prefers the attributed cost and falls back to the even share", () => {
    expect(attributedCost({ cost_usd: 0.4, cost_share_usd: 0.1 })).toBe(0.4);
    expect(attributedCost({ cost_share_usd: 0.1 })).toBe(0.1);
    expect(attributedCost({ cost_usd: 0, cost_share_usd: 0.1 })).toBe(0);
    expect(attributedCost({})).toBe(0);
  });
});

describe("MCP servers", () => {
  it("write a cell per column", () => {
    const cells = mcpServerCells({
      mcp_server: "thulu-github",
      invocations: 1200,
      sessions: 14,
      result_tokens: 250_000,
      weighted_tokens: 900_000,
      cost_usd: 3.5,
    });
    expect(cells).toHaveLength(mcpServerColumns().length);
    expect(cells.slice(1)).toEqual(["1,200", "14", "250.0k", "$3.50"]);
    expect(tableRowHTML(mcpServerColumns(), { cells })).toContain(
      '<span class="key">thulu-github</span>',
    );
  });
});

const recent: DelegationRow = {
  id: "dlg_0123456789abcdef",
  agent: "dev",
  stage: "build",
  kind: "read",
  model: "claude-haiku",
  provider: "anthropic",
  files: 3,
  input_tokens: 90_000,
  output_tokens: 800,
  cost_usd: 0.02,
  duration_ms: 4200,
  status: "ok",
  created_at: "2026-10-21T10:00:00Z",
  task_id: "t-1",
};
const byAgent = { agent: "dev", stage: "build", count: 9, cost_usd: 0.3 };

const report: DelegationsReport = {
  summary: {
    count: 12,
    ok: 11,
    failed: 1,
    cost_usd: 0.42,
    input_tokens: 1_200_000,
    output_tokens: 40_000,
    avg_duration_ms: 6500,
  },
  by_agent: [byAgent],
  recent: [recent],
};

describe("delegations", () => {
  it("sum up the count, the cost and the duration", () => {
    expect(delegationTiles(report.summary)).toEqual([
      { id: "dlg-count", value: "12", sub: "11 ok · 1 failed" },
      { id: "dlg-cost", value: "$0.4200", sub: "1.20M in · 40.0k out" },
      { id: "dlg-duration", value: "6.5s", sub: "" },
    ]);
    expect(delegationTiles(null)[0].value).toBe("0");
  });

  it("write the per-agent rows", () => {
    const cells = delegationAgentCells(byAgent);
    expect(cells).toHaveLength(delegationAgentColumns().length);
    expect(cells.slice(2)).toEqual(["9", "$0.3000"]);
    expect(delegationAgentCells({ agent: "dev", stage: "", count: 1, cost_usd: 0 })[1]).toBe("—");
  });

  it("write a recent delegation with its run, task, kind and state", () => {
    const row = recent;
    const cells = delegationRowCells(row);
    expect(cells).toHaveLength(delegationRecentColumns().length);
    const html = tableRowHTML(delegationRecentColumns(), { cells });
    expect(html).toContain("dev · build");
    expect(html).toContain('href="/tasks/#task-t-1"');
    expect(html).toContain(">Read<");
    expect(html).toContain("anthropic");
    expect(html).toContain("90.0k / 800");
    expect(html).toContain("badge-ok");
    const failed: DelegationRow = { ...row, status: "timeout", task_id: undefined };
    expect(
      tableRowHTML(delegationRecentColumns(), { cells: delegationRowCells(failed) }),
    ).toContain("badge-danger");
  });

  it("read started as neutral and anything else but ok as a failure", () => {
    expect(delegationStatusTone("ok")).toBe("ok");
    expect(delegationStatusTone("provider_error")).toBe("danger");
    expect(delegationStatusTone("started")).toBe("neutral");
  });
});

describe("runs by optimizer level", () => {
  const rows: OptimizerLevelRow[] = [
    {
      level: "enforce",
      sessions: 5,
      cost_usd: 2,
      avg_cost_usd: 0.4,
      tools_cost_usd: 0.5,
      delegations_cost_usd: 0.1,
    },
    {
      level: "",
      sessions: 40,
      cost_usd: 30,
      avg_cost_usd: 0.75,
      tools_cost_usd: 12,
      delegations_cost_usd: 0,
    },
    {
      level: "off",
      sessions: 10,
      cost_usd: 9,
      avg_cost_usd: 0.9,
      tools_cost_usd: 4,
      delegations_cost_usd: 0,
    },
  ];

  it("put off first, then suggest and enforce, then runs with none recorded", () => {
    expect(sortOptimizerRows(rows).map((r) => r.level)).toEqual(["off", "enforce", ""]);
    expect(sortOptimizerRows(null)).toEqual([]);
  });

  it("name each level and write its costs", () => {
    expect(optimizerLevelName("suggest")).toBe("Suggest");
    expect(optimizerLevelName("")).toBe("Not recorded");
    const cells = optimizerCells(rows[0]);
    expect(cells).toHaveLength(optimizerColumns().length);
    expect(cells).toEqual(["Enforce", "5", "$2.00", "$0.4000", "$0.5000", "$0.1000"]);
  });
});
