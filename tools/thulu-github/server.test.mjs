import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const RUN_KEY = "oct_test_run_key";
const OPERATOR_TOKEN = "ghp_operator_token_that_must_never_be_used";

const daemonCalls = [];
const githubCalls = [];
let daemonReply = null;
let daemon;
let github;
let mcp;
let mcpPort;

const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

function recorder(calls, handler) {
  return http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    const call = { method: req.method, path: url.pathname, query: url.searchParams, headers: req.headers, body: "" };
    calls.push(call);
    req.setEncoding("utf8");
    req.on("data", (chunk) => {
      call.body += chunk;
    });
    req.on("end", () => handler(req, res, call));
  });
}

function reply(res, status, body) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

before(async () => {
  daemon = recorder(daemonCalls, (req, res, call) => {
    if (daemonReply) return daemonReply(res, call);
    reply(res, 404, { error: "no github credential for this project" });
  });
  github = recorder(githubCalls, (_req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    res.end("{}");
  });
  const daemonPort = await listen(daemon);
  const githubPort = await listen(github);

  const probe = http.createServer();
  mcpPort = await listen(probe);
  await new Promise((r) => probe.close(r));

  mcp = spawn(process.execPath, [path.join(HERE, "server.js")], {
    env: {
      ...process.env,
      PORT: String(mcpPort),
      OCTOPUS_API: `http://127.0.0.1:${daemonPort}`,
      GITHUB_API_URL: `http://127.0.0.1:${githubPort}`,
      GH_TOKEN: OPERATOR_TOKEN,
      GITHUB_TOKEN: OPERATOR_TOKEN,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("MCP server did not start in time")), 10000);
    mcp.stdout.on("data", (d) => {
      if (String(d).includes("listening on")) {
        clearTimeout(timer);
        resolve();
      }
    });
    mcp.on("exit", (code) => reject(new Error(`MCP server exited early (${code})`)));
  });
});

after(async () => {
  mcp?.kill("SIGKILL");
  await new Promise((r) => daemon.close(r));
  await new Promise((r) => github.close(r));
});

async function connect(query, key = RUN_KEY) {
  const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  const init = key ? { requestInit: { headers: { "X-API-Key": key } } } : {};
  await client.connect(new SSEClientTransport(new URL(`http://127.0.0.1:${mcpPort}/sse?${query}`), init));
  return client;
}

async function rawEndpoint(key) {
  const ctrl = new AbortController();
  const res = await fetch(`http://127.0.0.1:${mcpPort}/sse?task=t1`, {
    headers: { "X-API-Key": key },
    signal: ctrl.signal,
  });
  const reader = res.body.getReader();
  let buf = "";
  while (!buf.includes("\n\n")) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += new TextDecoder().decode(value);
  }
  const data = buf.split("\n").find((l) => l.startsWith("data:"));
  return {
    endpoint: new URL(data.slice(5).trim(), `http://127.0.0.1:${mcpPort}`).toString(),
    close: () => ctrl.abort(),
  };
}

test("a handshake without a run key is refused", async () => {
  const r = await fetch(`http://127.0.0.1:${mcpPort}/sse?task=t1`);
  assert.equal(r.status, 401);
});

test("a POST on a leaked session id without that session's key is refused", async () => {
  const { endpoint, close } = await rawEndpoint(RUN_KEY);
  try {
    const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" });
    const post = (headers) =>
      fetch(endpoint, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body });
    assert.equal((await post({ "X-API-Key": "oct_attacker" })).status, 401);
    assert.equal((await post({})).status, 401);
    assert.equal((await post({ "X-API-Key": RUN_KEY })).status, 202);
  } finally {
    close();
  }
});

test("the keyless catalog probe lists tools and cannot call one", async () => {
  const c = await connect("scope=write&catalog=1", "");
  const names = (await c.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("run_dispatch"), "catalog must still widen the list");
  daemonCalls.length = 0;
  githubCalls.length = 0;
  const r = await c.callTool({ name: "pr_view", arguments: { repo: "o/r", number: 1 } });
  assert.equal(r.isError, true);
  assert.match(r.content[0].text, /without a credential/);
  assert.equal(daemonCalls.length + githubCalls.length, 0);
  await c.close();
});

test("a project without a GitHub credential fails instead of using the server's GH_TOKEN", async () => {
  const c = await connect("task=t1&scope=read");
  try {
    daemonCalls.length = 0;
    githubCalls.length = 0;
    const r = await c.callTool({ name: "pr_view", arguments: { repo: "o/r", number: 1 } });
    assert.equal(r.isError, true, r.content[0].text);
    assert.match(r.content[0].text, /no GitHub token available/);
    assert.equal(githubCalls.length, 0, "nothing may reach GitHub without the caller's own token");
    assert.ok(daemonCalls.length > 0);
    for (const call of daemonCalls) assert.equal(call.headers["x-api-key"], RUN_KEY);
  } finally {
    await c.close();
  }
});

test("/health reports only ok", async () => {
  const body = await (await fetch(`http://127.0.0.1:${mcpPort}/health`)).json();
  assert.deepEqual(body, { ok: true });
});

async function inSession(query, answer, fn) {
  daemonReply = answer;
  const c = await connect(query);
  try {
    daemonCalls.length = 0;
    githubCalls.length = 0;
    await fn(c);
  } finally {
    daemonReply = null;
    await c.close();
  }
}

test("a chat asks the daemon for its token by repository", async () => {
  const answer = (res, call) =>
    call.path === "/internal/github-token" ? reply(res, 200, { token: "ghs_chat" }) : reply(res, 404, {});
  await inSession("tools=run_cancel", answer, async (c) => {
    const r = await c.callTool({ name: "run_cancel", arguments: { repo: "Acme/Widgets", run_id: 9 } });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(daemonCalls.length, 1);
    assert.equal(daemonCalls[0].path, "/internal/github-token");
    assert.equal(daemonCalls[0].query.get("repo"), "Acme/Widgets");
    assert.equal(daemonCalls[0].query.get("permissions"), "actions:write");
    assert.equal(daemonCalls[0].headers["x-api-key"], RUN_KEY);
    assert.equal(githubCalls.length, 1);
    assert.equal(githubCalls[0].method, "POST");
    assert.equal(githubCalls[0].path, "/repos/Acme/Widgets/actions/runs/9/cancel");
    assert.equal(githubCalls[0].headers.authorization, "token ghs_chat");
  });
});

test("a chat sees why the daemon refused its token", async () => {
  const answer = (res) => reply(res, 404, { error: "repository acme/secret is not a project of this workspace" });
  await inSession("tools=run_cancel", answer, async (c) => {
    const r = await c.callTool({ name: "run_cancel", arguments: { repo: "acme/secret", run_id: 9 } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /HTTP 404/);
    assert.match(r.content[0].text, /acme\/secret is not a project of this workspace/);
    assert.equal(githubCalls.length, 0);
  });
});

test("a chat cannot commit or push: there is no worktree", async () => {
  await inSession("tools=commit+branch_push+branch_status", null, async (c) => {
    for (const [name, args] of [["commit", { message: "x" }], ["branch_push", {}], ["branch_status", {}]]) {
      const r = await c.callTool({ name, arguments: args });
      assert.equal(r.isError, true, name);
      assert.match(r.content[0].text, /works on a task's worktree/, name);
    }
    assert.equal(daemonCalls.length + githubCalls.length, 0);
  });
});

test("a chat's merge is held by the daemon, not done with the chat's token", async () => {
  const held = { status: "awaiting_confirmation", action_id: "act-1", message: "waiting for the person" };
  await inSession("tools=pr_merge", (res) => reply(res, 202, held), async (c) => {
    const r = await c.callTool({
      name: "pr_merge",
      arguments: { repo: "Acme/Widgets", number: 7, merge_method: "squash" },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.deepEqual(JSON.parse(r.content[0].text), held);
    assert.equal(daemonCalls.length, 1, "no token lookup: the merge goes to the daemon");
    assert.equal(daemonCalls[0].method, "POST");
    assert.equal(daemonCalls[0].path, "/internal/github/Acme/Widgets/pulls/7/merge");
    assert.equal(daemonCalls[0].headers["x-api-key"], RUN_KEY);
    assert.deepEqual(JSON.parse(daemonCalls[0].body), { merge_method: "squash" });
    assert.equal(githubCalls.length, 0);
  });
});

test("a chat's deployment approval is held by the daemon", async () => {
  const held = { status: "awaiting_confirmation", action_id: "act-2", message: "waiting for the person" };
  await inSession("tools=deployment_approve", (res) => reply(res, 202, held), async (c) => {
    const r = await c.callTool({
      name: "deployment_approve",
      arguments: { repo: "Acme/Widgets", run_id: 42, environment_ids: [5] },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(JSON.parse(r.content[0].text).status, "awaiting_confirmation");
    assert.equal(daemonCalls.length, 1);
    assert.equal(daemonCalls[0].path, "/internal/github/Acme/Widgets/runs/42/deployment-review");
    assert.deepEqual(JSON.parse(daemonCalls[0].body), { state: "approved", environment_ids: [5] });
    assert.equal(githubCalls.length, 0);
  });
});

test("a task run merges with its own token", async () => {
  const answer = (res, call) =>
    call.path === "/internal/tasks/t9/github-token" ? reply(res, 200, { token: "ghs_task" }) : reply(res, 404, {});
  await inSession("task=t9&tools=pr_merge", answer, async (c) => {
    const r = await c.callTool({
      name: "pr_merge",
      arguments: { repo: "acme/widgets", number: 7, merge_method: "rebase" },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(daemonCalls.length, 1);
    assert.equal(daemonCalls[0].path, "/internal/tasks/t9/github-token");
    assert.equal(githubCalls.length, 1);
    assert.equal(githubCalls[0].method, "PUT");
    assert.equal(githubCalls[0].path, "/repos/acme/widgets/pulls/7/merge");
    assert.equal(githubCalls[0].headers.authorization, "token ghs_task");
    assert.equal(JSON.parse(githubCalls[0].body).merge_method, "rebase");
  });
});

import { describe } from "node:test";

describe("PR feedback tools", () => {
  const ghCalls = [];
  let ghRoute = () => [404, { message: "Not Found" }];
  let gh;
  let server;
  let port;

  before(async () => {
    gh = recorder(ghCalls, (req, res, call) => {
      const [status, body, headers = {}] = ghRoute(call, `http://${req.headers.host}`);
      res.writeHead(status, { "content-type": "application/json", ...headers });
      res.end(JSON.stringify(body));
    });
    const ghPort = await listen(gh);
    const probe = http.createServer();
    port = await listen(probe);
    await new Promise((r) => probe.close(r));
    server = spawn(process.execPath, [path.join(HERE, "server.js")], {
      env: {
        ...process.env,
        PORT: String(port),
        OCTOPUS_API: `http://127.0.0.1:${daemon.address().port}`,
        GITHUB_API_URL: `http://127.0.0.1:${ghPort}`,
        LIST_CAP: "5",
        HUNK_CAP_BYTES: "20",
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("MCP server did not start in time")), 10000);
      server.stdout.on("data", (d) => {
        if (String(d).includes("listening on")) {
          clearTimeout(timer);
          resolve();
        }
      });
      server.on("exit", (code) => reject(new Error(`MCP server exited early (${code})`)));
    });
  });

  after(async () => {
    server?.kill("SIGKILL");
    await new Promise((r) => gh.close(r));
  });

  const taskToken =
    ({ refuseChecks = false } = {}) =>
    (res, call) => {
      if (call.path !== "/internal/tasks/t1/github-token") return reply(res, 404, {});
      if (refuseChecks && call.query.get("permissions") === "checks:read") {
        return reply(res, 400, { error: "unknown permission checks:read" });
      }
      return reply(res, 200, { token: "ghs_task" });
    };

  async function session(query, answer, routes, fn) {
    daemonReply = answer;
    ghRoute = routes ?? (() => [404, { message: "Not Found" }]);
    const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
    await client.connect(
      new SSEClientTransport(new URL(`http://127.0.0.1:${port}/sse?${query}`), {
        requestInit: { headers: { "X-API-Key": RUN_KEY } },
      }),
    );
    try {
      daemonCalls.length = 0;
      ghCalls.length = 0;
      await fn(client);
    } finally {
      daemonReply = null;
      await client.close();
    }
  }

  const call = async (c, name, args) => {
    const r = await c.callTool({ name, arguments: args });
    assert.ok(!r.isError, r.content[0].text);
    return JSON.parse(r.content[0].text);
  };

  const PR = { number: 5, head: { sha: "abc123", ref: "feat" } };

  test("every PR feedback tool is listed under scope read", async () => {
    await session("task=t1&scope=read", taskToken(), undefined, async (c) => {
      const names = (await c.listTools()).tools.map((t) => t.name);
      for (const name of [
        "pr_comments_list",
        "pr_review_threads",
        "pr_reviews_list",
        "pr_reviewers",
        "pr_checks",
        "labels_list",
      ]) {
        assert.ok(names.includes(name), name);
      }
      assert.ok(!names.includes("pr_comment"), "scope read must not list write tools");
    });
  });

  test("pr_review_threads pages by cursor, groups comments and puts unresolved first", async () => {
    const thread = (id, resolved, comments) => ({
      id,
      isResolved: resolved,
      isOutdated: false,
      path: "a.go",
      line: 10,
      originalLine: 9,
      diffSide: "RIGHT",
      comments: { totalCount: comments.length, nodes: comments },
    });
    const comment = (databaseId, replyTo) => ({
      databaseId,
      author: { login: "rev" },
      body: `comment ${databaseId}`,
      createdAt: "2026-10-01T00:00:00Z",
      url: `https://github.com/o/r/pull/5#discussion_r${databaseId}`,
      diffHunk: "@@ -1,3 +1,4 @@ a long hunk that has to be clipped",
      replyTo: replyTo ? { databaseId: replyTo } : null,
    });
    const routes = (c) => {
      if (c.method !== "POST" || c.path !== "/graphql") return [404, {}];
      const { variables } = JSON.parse(c.body);
      const page =
        variables.after == null
          ? {
              pageInfo: { hasNextPage: true, endCursor: "c1" },
              nodes: [thread("PRRT_1", true, [comment(11)]), thread("PRRT_2", false, [comment(21), comment(22, 21)])],
            }
          : { pageInfo: { hasNextPage: false, endCursor: null }, nodes: [thread("PRRT_3", false, [comment(31)])] };
      return [200, { data: { repository: { pullRequest: { reviewThreads: page } } } }];
    };
    await session("task=t1&scope=read", taskToken(), routes, async (c) => {
      const all = await call(c, "pr_review_threads", { repo: "o/r", number: 5 });
      assert.equal(ghCalls.length, 2);
      const first = JSON.parse(ghCalls[0].body);
      assert.match(first.query, /reviewThreads\(first: 100, after: \$after\)/);
      assert.match(first.query, /isResolved isOutdated/);
      assert.match(first.query, /replyTo \{ databaseId \}/);
      assert.deepEqual(first.variables, { owner: "o", repo: "r", number: 5, after: null });
      assert.equal(JSON.parse(ghCalls[1].body).variables.after, "c1");
      assert.equal(ghCalls[0].headers.authorization, "token ghs_task");

      assert.deepEqual([all.total, all.unresolved, all.resolved, all.capped], [3, 2, 1, false]);
      assert.deepEqual(
        all.threads.map((th) => th.id),
        ["PRRT_2", "PRRT_3", "PRRT_1"],
      );
      const t2 = all.threads[0];
      assert.deepEqual(
        t2.comments.map((cm) => [cm.id, cm.in_reply_to]),
        [
          [21, null],
          [22, 21],
        ],
      );
      assert.equal(t2.original_line, 9);
      assert.match(t2.diff_hunk, /^@@ -1,3 \+1,4 @@ a lo/);
      assert.match(t2.diff_hunk, /diff_hunk truncated/);

      const open = await call(c, "pr_review_threads", { repo: "o/r", number: 5, state: "unresolved" });
      assert.deepEqual(
        open.threads.map((th) => th.id),
        ["PRRT_2", "PRRT_3"],
      );
      assert.equal(open.total, 3, "counts cover every thread whatever the filter");
      const done = await call(c, "pr_review_threads", { repo: "o/r", number: 5, state: "resolved" });
      assert.deepEqual(
        done.threads.map((th) => th.id),
        ["PRRT_1"],
      );

      const bad = await c.callTool({ name: "pr_review_threads", arguments: { repo: "o/r", number: 5, state: "open" } });
      assert.equal(bad.isError, true);
      assert.match(bad.content[0].text, /state must be 'all', 'unresolved' or 'resolved'/);
    });
  });

  test("pr_checks asks for checks:read and reports statuses it may not read as unavailable", async () => {
    const routes = (c) => {
      if (c.path === "/repos/o/r/pulls/5") return [200, PR];
      if (c.path === "/repos/o/r/commits/abc123/check-runs") {
        return [
          200,
          {
            total_count: 2,
            check_runs: [
              { name: "lint", status: "completed", conclusion: "success", app: { slug: "github-actions" } },
              {
                name: "test",
                status: "completed",
                conclusion: "failure",
                app: { slug: "github-actions" },
                output: { title: "2 failed", summary: "TestA, TestB" },
              },
            ],
          },
        ];
      }
      if (c.path === "/repos/o/r/commits/abc123/status") {
        return [403, { message: "Resource not accessible by integration" }];
      }
      return [404, { message: "Not Found" }];
    };
    await session("task=t1&scope=read", taskToken(), routes, async (c) => {
      const out = await call(c, "pr_checks", { repo: "o/r", number: 5 });
      assert.equal(daemonCalls.length, 1);
      assert.equal(daemonCalls[0].query.get("permissions"), "checks:read");
      const checks = ghCalls.find((g) => g.path.endsWith("/check-runs"));
      assert.equal(checks.query.get("filter"), "latest");
      assert.equal(out.source, "checks");
      assert.equal(out.head_sha, "abc123");
      assert.equal(out.verdict, "failing");
      assert.deepEqual(out.counts, { total: 2, failing: 1, pending: 0, passing: 1 });
      assert.equal(out.check_runs.length, 2);
      assert.equal(out.check_runs[1].title, "2 failed");
      assert.equal(out.statuses_unavailable.status, 403);
      assert.equal(out.statuses, undefined);
      assert.ok(!ghCalls.some((g) => g.path.includes("/actions/runs")));

      const failed = await call(c, "pr_checks", { repo: "o/r", number: 5, only_failed: true });
      assert.deepEqual(
        failed.check_runs.map((r) => r.name),
        ["test"],
      );
      assert.equal(failed.counts.total, 2, "only_failed narrows the list, not the verdict");
    });
  });

  test("pr_checks falls back to Actions runs when the mint with checks:read is refused", async () => {
    const routes = (c) => {
      if (c.path === "/repos/o/r/pulls/5") return [200, PR];
      if (c.path === "/repos/o/r/actions/runs") {
        return [
          200,
          {
            total_count: 2,
            workflow_runs: [
              { id: 7, name: "CI", status: "completed", conclusion: "success" },
              { id: 8, name: "E2E", status: "in_progress", conclusion: null },
            ],
          },
        ];
      }
      if (c.path === "/repos/o/r/commits/abc123/status") {
        return [
          200,
          {
            state: "success",
            statuses: [{ context: "deploy/preview", state: "success", target_url: "https://x" }],
          },
        ];
      }
      return [404, { message: "Not Found" }];
    };
    await session("task=t1&scope=read", taskToken({ refuseChecks: true }), routes, async (c) => {
      const out = await call(c, "pr_checks", { repo: "o/r", number: 5 });
      assert.deepEqual(
        daemonCalls.map((d) => d.query.get("permissions")),
        ["checks:read", null],
        "retried without the extra",
      );
      assert.ok(!ghCalls.some((g) => g.path.endsWith("/check-runs")), "no check runs without the permission");
      const runs = ghCalls.find((g) => g.path === "/repos/o/r/actions/runs");
      assert.equal(runs.query.get("head_sha"), "abc123");
      assert.equal(out.source, "actions");
      assert.match(out.source_note, /checks:read/);
      assert.match(out.source_note, /GitHub Actions/);
      assert.deepEqual(
        out.workflow_runs.map((r) => [r.id, r.outcome]),
        [
          [7, "passing"],
          [8, "pending"],
        ],
      );
      assert.equal(out.statuses.state, "success");
      assert.equal(out.verdict, "pending");
    });
  });

  test("pr_checks falls back to Actions runs when GitHub refuses the check runs", async () => {
    const routes = (c) => {
      if (c.path === "/repos/o/r/pulls/5") return [200, PR];
      if (c.path === "/repos/o/r/commits/abc123/check-runs") return [403, { message: "Resource not accessible" }];
      if (c.path === "/repos/o/r/actions/runs") {
        return [200, { workflow_runs: [{ id: 9, name: "CI", status: "completed", conclusion: "failure" }] }];
      }
      if (c.path === "/repos/o/r/commits/abc123/status") return [200, { state: "pending", statuses: [] }];
      return [404, { message: "Not Found" }];
    };
    await session("task=t1&scope=read", taskToken(), routes, async (c) => {
      const out = await call(c, "pr_checks", { repo: "o/r", number: 5 });
      assert.equal(out.source, "actions");
      assert.match(out.source_note, /HTTP 403/);
      assert.equal(out.verdict, "failing");
      assert.equal(out.statuses.state, null, "a commit without statuses has no combined state");
    });
  });

  test("pr_comments_list pages until the cap and says it stopped there", async () => {
    const routes = (c, base) => {
      if (c.path !== "/repos/o/r/issues/5/comments") return [404, {}];
      const page = Number(c.query.get("page") ?? 1);
      const comments = [1, 2, 3].map((i) => ({
        id: page * 10 + i,
        user: { login: "someone" },
        author_association: "MEMBER",
        body: "hi",
        created_at: "2026-10-01T00:00:00Z",
      }));
      const next = `${base}/repos/o/r/issues/5/comments?per_page=100&page=${page + 1}`;
      return [200, comments, { link: `<${next}>; rel="next"` }];
    };
    await session("task=t1&scope=read", taskToken(), routes, async (c) => {
      const out = await call(c, "pr_comments_list", { repo: "o/r", number: 5, since: "2026-10-01T00:00:00Z" });
      assert.equal(ghCalls.length, 2, "stops fetching once the cap is reached");
      assert.equal(ghCalls[0].query.get("since"), "2026-10-01T00:00:00Z");
      assert.equal(out.returned, 5);
      assert.equal(out.capped, true);
      assert.match(out.note, /stopped at 5 comments/);
      assert.deepEqual(
        out.comments.map((cm) => cm.id),
        [11, 12, 13, 21, 22],
      );
      assert.equal(out.comments[0].author, "someone");
    });
  });

  test("pr_reviewers keeps an approval standing past a later comment", async () => {
    const routes = (c) => {
      if (c.path === "/repos/o/r/pulls/5/requested_reviewers") {
        return [200, { users: [{ login: "carol" }, { login: "alice" }], teams: [{ slug: "core", name: "Core" }] }];
      }
      if (c.path === "/repos/o/r/pulls/5/reviews") {
        return [
          200,
          [
            { id: 1, user: { login: "alice" }, state: "APPROVED", submitted_at: "2026-10-01T00:00:00Z" },
            { id: 2, user: { login: "alice" }, state: "COMMENTED", submitted_at: "2026-10-02T00:00:00Z" },
            { id: 3, user: { login: "bob" }, state: "CHANGES_REQUESTED", submitted_at: "2026-10-02T00:00:00Z" },
          ],
        ];
      }
      return [404, {}];
    };
    await session("task=t1&scope=read", taskToken(), routes, async (c) => {
      const out = await call(c, "pr_reviewers", { repo: "o/r", number: 5 });
      assert.deepEqual(out.requested_users, [
        { login: "carol", latest_state: null },
        { login: "alice", latest_state: "APPROVED" },
      ]);
      assert.deepEqual(out.requested_teams, [{ slug: "core", name: "Core" }]);
      assert.deepEqual(out.approved_by, ["alice"]);
      assert.deepEqual(out.changes_requested_by, ["bob"]);
    });
  });
});

const PR_WRITE_TOOLS = [
  "pr_review_comment_reply",
  "pr_review_thread_resolve",
  "pr_review_thread_unresolve",
  "pr_reviewers_request",
  "pr_reviewers_remove",
  "pr_labels_add",
  "pr_labels_remove",
  "pr_labels_set",
  "pr_assignees_add",
  "pr_assignees_remove",
  "pr_update",
  "pr_ready_for_review",
];

const ghCalls = [];
let ghReply = null;
let ghStub;
let mcpWrite;
let mcpWritePort;

before(async () => {
  ghStub = recorder(ghCalls, (_req, res, call) => {
    let body = null;
    try {
      body = call.body ? JSON.parse(call.body) : null;
    } catch {
      body = null;
    }
    const [status, json] = ghReply?.(call, body) ?? [200, {}];
    reply(res, status, json);
  });
  const ghPort = await listen(ghStub);
  const probe = http.createServer();
  mcpWritePort = await listen(probe);
  await new Promise((r) => probe.close(r));
  mcpWrite = spawn(process.execPath, [path.join(HERE, "server.js")], {
    env: {
      ...process.env,
      PORT: String(mcpWritePort),
      OCTOPUS_API: `http://127.0.0.1:${daemon.address().port}`,
      GITHUB_API_URL: `http://127.0.0.1:${ghPort}`,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("MCP server did not start in time")), 10000);
    mcpWrite.stdout.on("data", (d) => {
      if (String(d).includes("listening on")) {
        clearTimeout(timer);
        resolve();
      }
    });
    mcpWrite.on("exit", (code) => reject(new Error(`MCP server exited early (${code})`)));
  });
});

after(async () => {
  mcpWrite?.kill("SIGKILL");
  await new Promise((r) => ghStub.close(r));
});

async function inTaskRun(github, fn) {
  daemonReply = (res, call) =>
    call.path === "/internal/tasks/t9/github-token" ? reply(res, 200, { token: "ghs_task" }) : reply(res, 404, {});
  ghReply = github;
  const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  await client.connect(
    new SSEClientTransport(new URL(`http://127.0.0.1:${mcpWritePort}/sse?task=t9&scope=write`), {
      requestInit: { headers: { "X-API-Key": RUN_KEY } },
    }),
  );
  try {
    ghCalls.length = 0;
    await fn(client);
  } finally {
    daemonReply = null;
    ghReply = null;
    await client.close();
  }
}

const writes = () => ghCalls.filter((c) => c.method !== "GET" && c.path !== "/graphql");
const graphqlOps = () =>
  ghCalls.filter((c) => c.path === "/graphql").map((c) => JSON.parse(c.body).query.split("(")[0].trim());

test("the PR write tools are absent under scope read and present under write", async () => {
  const read = await connect("task=t1&scope=read");
  const write = await connect("task=t1&scope=write");
  try {
    const readNames = (await read.listTools()).tools.map((t) => t.name);
    const writeNames = (await write.listTools()).tools.map((t) => t.name);
    for (const name of PR_WRITE_TOOLS) {
      assert.ok(!readNames.includes(name), `${name} listed under read`);
      assert.ok(writeNames.includes(name), `${name} missing under write`);
    }
    const r = await read.callTool({ name: "pr_labels_add", arguments: { repo: "o/r", number: 1, labels: ["bug"] } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /requires scope 'write'/);
  } finally {
    await read.close();
    await write.close();
  }
});

test("pr_review_comment_reply replies under the thread's first comment", async () => {
  const github = (call) => {
    if (call.method === "GET" && call.path === "/repos/acme/widgets/pulls/comments/202") {
      return [200, { id: 202, in_reply_to_id: 101, pull_request_url: "https://api.github.com/repos/acme/widgets/pulls/7" }];
    }
    if (call.method === "POST") return [201, { id: 303, in_reply_to_id: 101, html_url: "https://gh/c/303" }];
    return [404, { message: "Not Found" }];
  };
  await inTaskRun(github, async (c) => {
    const r = await c.callTool({
      name: "pr_review_comment_reply",
      arguments: { repo: "acme/widgets", number: 7, comment_id: 202, body: "Fixed in abc123." },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.deepEqual(JSON.parse(r.content[0].text), { id: 303, in_reply_to_id: 101, html_url: "https://gh/c/303" });
    const post = writes();
    assert.equal(post.length, 1);
    assert.equal(post[0].path, "/repos/acme/widgets/pulls/7/comments/101/replies");
    assert.deepEqual(JSON.parse(post[0].body), { body: "Fixed in abc123." });
    assert.equal(post[0].headers.authorization, "token ghs_task");
  });
});

test("pr_review_comment_reply posts nothing for a comment of another PR", async () => {
  const github = () => [200, { id: 202, pull_request_url: "https://api.github.com/repos/acme/widgets/pulls/8" }];
  await inTaskRun(github, async (c) => {
    const r = await c.callTool({
      name: "pr_review_comment_reply",
      arguments: { repo: "acme/widgets", number: 7, comment_id: 202, body: "x" },
    });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /belongs to PR #8, not #7/);
    assert.equal(writes().length, 0);
  });
});

function threadGithub(nameWithOwner, isResolved = false) {
  return (call, body) => {
    if (call.path !== "/graphql") return [404, { message: "Not Found" }];
    if (body.query.startsWith("query")) {
      return [
        200,
        { data: { node: { id: body.variables.id, isResolved, pullRequest: { number: 7, repository: { nameWithOwner } } } } },
      ];
    }
    const op = body.query.includes("unresolveReviewThread") ? "unresolveReviewThread" : "resolveReviewThread";
    return [200, { data: { [op]: { thread: { id: body.variables.id, isResolved: op === "resolveReviewThread" } } } }];
  };
}

test("pr_review_thread_resolve refuses a thread from another repo", async () => {
  await inTaskRun(threadGithub("acme/secret"), async (c) => {
    const r = await c.callTool({
      name: "pr_review_thread_resolve",
      arguments: { repo: "acme/widgets", thread_id: "PRRT_1" },
    });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /belongs to acme\/secret, not acme\/widgets/);
    assert.deepEqual(graphqlOps(), ["query"], "the mutation must not be sent");
  });
});

test("pr_review_thread_resolve and _unresolve mutate a thread of the repo", async () => {
  await inTaskRun(threadGithub("Acme/Widgets"), async (c) => {
    const r = await c.callTool({
      name: "pr_review_thread_resolve",
      arguments: { repo: "acme/widgets", thread_id: "PRRT_1" },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.deepEqual(JSON.parse(r.content[0].text), { thread_id: "PRRT_1", number: 7, resolved: true });
    assert.deepEqual(graphqlOps(), ["query", "mutation"]);
    assert.match(JSON.parse(ghCalls.at(-1).body).query, /resolveReviewThread\(input: \{ threadId: \$id \}\)/);
  });
  await inTaskRun(threadGithub("acme/widgets", true), async (c) => {
    const r = await c.callTool({
      name: "pr_review_thread_unresolve",
      arguments: { repo: "acme/widgets", thread_id: "PRRT_1" },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(JSON.parse(r.content[0].text).resolved, false);
    assert.match(JSON.parse(ghCalls.at(-1).body).query, /unresolveReviewThread/);
  });
});

const repoLabels = (call) =>
  call.method === "GET" && call.path === "/repos/acme/widgets/labels"
    ? [200, [{ name: "bug" }, { name: "Needs Review" }, { name: "type: docs" }]]
    : null;

test("pr_labels_add refuses unknown labels and never calls addLabels", async () => {
  await inTaskRun((call) => repoLabels(call) ?? [500, {}], async (c) => {
    const r = await c.callTool({
      name: "pr_labels_add",
      arguments: { repo: "acme/widgets", number: 7, labels: ["bug", "bgu", "zzzzzz"] },
    });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /'bgu' \(closest: bug\)/);
    assert.match(r.content[0].text, /'zzzzzz' \(nothing close\)/);
    assert.equal(writes().length, 0, "nothing may be sent: GitHub would create the label");
  });
});

test("pr_labels_add sends known labels in the repo's spelling", async () => {
  const github = (call, body) =>
    repoLabels(call) ??
    (call.method === "POST" ? [200, [{ name: "bug" }, ...body.labels.map((name) => ({ name }))]] : [500, {}]);
  await inTaskRun(github, async (c) => {
    const r = await c.callTool({
      name: "pr_labels_add",
      arguments: { repo: "acme/widgets", number: 7, labels: ["needs review"] },
    });
    assert.ok(!r.isError, r.content[0].text);
    const post = writes();
    assert.equal(post.length, 1);
    assert.equal(post[0].path, "/repos/acme/widgets/issues/7/labels");
    assert.deepEqual(JSON.parse(post[0].body), { labels: ["Needs Review"] });
    assert.deepEqual(JSON.parse(r.content[0].text).labels, ["bug", "Needs Review"]);
  });
});

test("pr_labels_remove reports a label the PR does not carry", async () => {
  const github = (call) => {
    if (call.method !== "DELETE") return [500, {}];
    return call.path.endsWith("/labels/bug") ? [200, [{ name: "type: docs" }]] : [404, { message: "Label does not exist" }];
  };
  await inTaskRun(github, async (c) => {
    const r = await c.callTool({
      name: "pr_labels_remove",
      arguments: { repo: "acme/widgets", number: 7, labels: ["bug", "wontfix"] },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.deepEqual(JSON.parse(r.content[0].text), { number: 7, labels: ["type: docs"], not_on_pr: ["wontfix"] });
  });
});

test("pr_reviewers_request needs a non-empty list", async () => {
  await inTaskRun(null, async (c) => {
    for (const args of [{}, { reviewers: [] }, { reviewers: [" "], team_reviewers: [] }]) {
      const r = await c.callTool({
        name: "pr_reviewers_request",
        arguments: { repo: "acme/widgets", number: 7, ...args },
      });
      assert.equal(r.isError, true);
      assert.match(r.content[0].text, /at least one reviewer/);
    }
    assert.equal(ghCalls.length, 0);
  });
});

test("pr_reviewers_request sends logins and team slugs", async () => {
  const github = () => [
    201,
    { number: 7, requested_reviewers: [{ login: "alice" }], requested_teams: [{ slug: "core" }], html_url: "u" },
  ];
  await inTaskRun(github, async (c) => {
    const r = await c.callTool({
      name: "pr_reviewers_request",
      arguments: { repo: "acme/widgets", number: 7, reviewers: ["alice"], team_reviewers: ["core"] },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(ghCalls[0].path, "/repos/acme/widgets/pulls/7/requested_reviewers");
    assert.deepEqual(JSON.parse(ghCalls[0].body), { reviewers: ["alice"], team_reviewers: ["core"] });
    assert.deepEqual(JSON.parse(r.content[0].text).requested_teams, ["core"]);
  });
});

test("pr_assignees_add reports logins GitHub skipped", async () => {
  await inTaskRun(() => [201, { assignees: [{ login: "alice" }] }], async (c) => {
    const r = await c.callTool({
      name: "pr_assignees_add",
      arguments: { repo: "acme/widgets", number: 7, assignees: ["alice", "ghost"] },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(ghCalls[0].path, "/repos/acme/widgets/issues/7/assignees");
    assert.deepEqual(JSON.parse(r.content[0].text), { number: 7, assignees: ["alice"], not_assigned: ["ghost"] });
  });
});

test("pr_update forwards title, body and base, never state", async () => {
  await inTaskRun(() => [200, { number: 7, title: "New", base: { ref: "dev" } }], async (c) => {
    const empty = await c.callTool({ name: "pr_update", arguments: { repo: "acme/widgets", number: 7 } });
    assert.equal(empty.isError, true);
    assert.equal(ghCalls.length, 0);
    const r = await c.callTool({
      name: "pr_update",
      arguments: { repo: "acme/widgets", number: 7, title: "New", base: "dev", state: "closed" },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(ghCalls[0].method, "PATCH");
    assert.deepEqual(JSON.parse(ghCalls[0].body), { title: "New", base: "dev" });
  });
});

test("pr_ready_for_review picks the mutation from draft", async () => {
  const github = (draftNow) => (call, body) => {
    if (call.method === "GET") return [200, { number: 7, draft: draftNow, node_id: "PR_node", html_url: "u" }];
    const op = body.query.includes("convertPullRequestToDraft") ? "convertPullRequestToDraft" : "markPullRequestReadyForReview";
    return [200, { data: { [op]: { pullRequest: { number: 7, isDraft: op === "convertPullRequestToDraft" } } } }];
  };
  await inTaskRun(github(true), async (c) => {
    const r = await c.callTool({ name: "pr_ready_for_review", arguments: { repo: "acme/widgets", number: 7 } });
    assert.ok(!r.isError, r.content[0].text);
    const sent = JSON.parse(ghCalls.at(-1).body);
    assert.match(sent.query, /markPullRequestReadyForReview/);
    assert.deepEqual(sent.variables, { id: "PR_node" });
    assert.equal(JSON.parse(r.content[0].text).draft, false);
  });
  await inTaskRun(github(false), async (c) => {
    const r = await c.callTool({
      name: "pr_ready_for_review",
      arguments: { repo: "acme/widgets", number: 7, draft: true },
    });
    assert.ok(!r.isError, r.content[0].text);
    assert.match(JSON.parse(ghCalls.at(-1).body).query, /convertPullRequestToDraft/);
    assert.equal(JSON.parse(r.content[0].text).draft, true);
  });
  await inTaskRun(github(false), async (c) => {
    const r = await c.callTool({ name: "pr_ready_for_review", arguments: { repo: "acme/widgets", number: 7 } });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(JSON.parse(r.content[0].text).unchanged, true);
    assert.equal(graphqlOps().length, 0, "already ready: no mutation");
  });
});
