// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import {
  apiCallColumns,
  categoryBarHTML,
  categoryLabel,
  hasBreakdown,
  orderedCategories,
  runOptimizerLevel,
  sessionBreakdownHTML,
  taskBreakdownHTML,
  taskUsageView,
  topCallColumns,
  topCallRows,
  type SessionBreakdown,
  type TaskBreakdown,
  type TopCall,
} from "./usage-breakdown";

const taskUsage: TaskBreakdown = {
  cost_usd: 4.2,
  unattributed_usd: 0.05,
  categories: [
    { category: "tools", weighted_tokens: 900_000, cost_usd: 2.5 },
    { category: "startup", weighted_tokens: 300_000, cost_usd: 0.9 },
    { category: "reasoning", weighted_tokens: 100_000, cost_usd: 0.6 },
    { category: "delegation", weighted_tokens: 0, cost_usd: 0.15 },
    { category: "unattributed", weighted_tokens: 0, cost_usd: 0.05 },
    { category: "compaction", weighted_tokens: 0, cost_usd: 0 },
  ],
  top_calls: [
    call({ tool_name: "Bash", cost_usd: 0.12, input_summary: "go test ./..." }),
    call({
      tool_name: "Read",
      cost_usd: 0.41,
      result_tokens: 31_000,
      carried_calls: 52,
      input_summary: "web/src/pages/tasks.astro",
      agent: "dev",
      stage: "build",
      session_id: "s-1",
    }),
    call({
      tool_name: "mcp__thulu-github__pr_get",
      mcp_server: "thulu-github",
      cost_usd: 0.2,
      delegation_id: "dlg_0123456789abcdef",
      delegated_cost_usd: 0.01,
    }),
  ],
  delegations: { count: 3, cost_usd: 0.15 },
};

function call(o: Partial<TopCall>): TopCall {
  return {
    tool_use_id: "tu",
    tool_name: "Read",
    result_tokens: 1000,
    carried_calls: 1,
    cost_usd: 0.01,
    session_id: "s-2",
    ...o,
  };
}

describe("categories", () => {
  it("stack in a fixed order and leave out the ones that cost nothing", () => {
    expect(orderedCategories(taskUsage.categories).map((c) => c.category)).toEqual([
      "startup",
      "reasoning",
      "tools",
      "delegation",
      "unattributed",
    ]);
  });

  it("show a category the dashboard does not know by its id", () => {
    expect(categoryLabel("tools")).toBe("Tools");
    expect(categoryLabel("cache_miss")).toBe("cache_miss");
  });

  it("draw one bar with a segment and a legend item per category", () => {
    const html = categoryBarHTML(taskUsage.categories);
    expect(html.match(/seg-progress-seg/g)).toHaveLength(5);
    expect(html).toContain('data-tone="viz-4"');
    expect(html).toContain('title="Tools: $2.50"');
    expect(html).toContain('<span class="chart-legend-value">$0.9000</span>');
    expect(categoryBarHTML([])).toBe("");
  });
});

describe("top calls", () => {
  it("list the costliest first, at most ten", () => {
    const rows = topCallRows(taskUsage.top_calls);
    expect(rows).toHaveLength(3);
    expect(rows.map((r) => (r.cells[0] as { html: string }).html)).toEqual([
      '<span class="mono">Read</span>',
      '<span class="mono">mcp__thulu-github__pr_get</span>',
      '<span class="mono">Bash</span>',
    ]);
    const many = Array.from({ length: 14 }, (_, i) => call({ cost_usd: i }));
    expect(topCallRows(many)).toHaveLength(10);
  });

  it("carry the summary, the run, the tokens, the re-reads and the cost", () => {
    const [read] = topCallRows(taskUsage.top_calls);
    expect(read.cells).toHaveLength(topCallColumns().length);
    expect((read.cells[0] as { subHTML: string }).subHTML).toBe("web/src/pages/tasks.astro");
    expect(read.cells[1]).toBe("dev · build");
    expect(read.cells[2]).toBe("31.0k");
    expect(read.cells[3]).toBe("52");
    expect((read.cells[4] as { html: string }).html).toBe("$0.4100");
  });

  it("badge a delegated call and name its MCP server", () => {
    const mcp = topCallRows(taskUsage.top_calls)[1];
    expect((mcp.cells[0] as { subHTML: string }).subHTML).toBe("MCP thulu-github");
    const cost = (mcp.cells[4] as { html: string }).html;
    expect(cost).toContain("Delegated");
    expect(cost).toContain("dlg_0123456789abcdef");
  });
});

describe("taskBreakdownHTML", () => {
  it("is empty for a run from before attribution, so the block stays as it was", () => {
    expect(hasBreakdown(undefined)).toBe(false);
    expect(taskBreakdownHTML(undefined)).toBe("");
    expect(taskBreakdownHTML({ ...taskUsage, categories: [] })).toBe("");
  });

  it("puts the bar, the delegations, the unattributed part and the top calls", () => {
    document.body.innerHTML = taskBreakdownHTML(taskUsage);
    expect(document.querySelector(".seg-progress")).not.toBeNull();
    expect(document.body.textContent).toContain("3 delegations to the worker, $0.1500.");
    expect(document.body.textContent).toContain("$0.0500 of $4.20 could not be traced to a call.");
    const open = document.querySelectorAll<HTMLElement>("[data-breakdown-session]");
    expect(Array.from(open).map((b) => b.dataset.breakdownSession)).toEqual(["s-1", "s-2", "s-2"]);
    expect(open[0].getAttribute("aria-label")).toBe("Open the breakdown of dev · build");
  });
});

describe("sessionBreakdownHTML", () => {
  const session: SessionBreakdown = {
    session_id: "s-1",
    attribution_version: 1,
    cost_usd: 1.25,
    unattributed_usd: 0.01,
    api_calls: 2,
    base_tokens: 18_000,
    compactions: 1,
    token_optimizer: "enforce",
    categories: [{ category: "tools", weighted_tokens: 10, cost_usd: 1 }],
    tools: [
      {
        tool_use_id: "a",
        tool_name: "Read",
        turn_index: 3,
        result_tokens: 8000,
        carried_calls: 35,
        cost_usd: 0.3,
        is_error: true,
      },
    ],
    calls: [
      {
        idx: 1,
        model: "claude-sonnet",
        input_tokens: 10,
        cache_read: 0,
        cache_creation: 18_000,
        output_tokens: 200,
        prompt_size: 18_010,
      },
      {
        idx: 2,
        model: "",
        input_tokens: 5,
        cache_read: 18_000,
        cache_creation: 8000,
        output_tokens: 300,
        prompt_size: 26_005,
        compaction: true,
      },
    ],
  };

  it("lists the run's facts, its tool calls and its API calls", () => {
    document.body.innerHTML = sessionBreakdownHTML(session);
    const text = document.body.textContent;
    expect(text).toContain("Token optimizer");
    expect(text).toContain("Enforce");
    const tables = document.querySelectorAll("table");
    expect(tables).toHaveLength(2);
    expect(tables[0].querySelector("tbody tr")?.getAttribute("data-tone")).toBe("danger");
    expect(tables[1].querySelectorAll("tbody tr")).toHaveLength(2);
    expect(tables[1].querySelectorAll("thead th")).toHaveLength(apiCallColumns().length);
    expect(tables[1].textContent).toContain("Compaction");
  });

  it("says when the run predates attribution", () => {
    expect(sessionBreakdownHTML({ ...session, attribution_version: 0 })).toContain(
      "predates per-call attribution",
    );
  });

  it("reads the level from either shape", () => {
    expect(runOptimizerLevel("suggest")).toBe("suggest");
    expect(runOptimizerLevel({ level: "off" })).toBe("off");
    expect(runOptimizerLevel(null)).toBe("");
  });
});

describe("taskUsageView", () => {
  it("says no runs only when the task never started one", () => {
    expect(taskUsageView({ sessions: 0, run_ids: [] })).toBe("no_runs");
    expect(taskUsageView({})).toBe("no_runs");
    expect(taskUsageView({ sessions: 0, run_ids: null })).toBe("no_runs");
  });

  it("keeps quiet about a run that has not reported tokens", () => {
    expect(taskUsageView({ sessions: 0, run_ids: ["c-1"] })).toBe("no_usage");
  });

  it("shows usage once a run reported it", () => {
    expect(taskUsageView({ sessions: 1, run_ids: ["c-1", "c-2"] })).toBe("usage");
  });
});
