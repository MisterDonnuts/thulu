// On a project whose credential is a stored PAT the daemon never hands a run a token: it answers
// 409 api.github.errors.use_proxy, and every GitHub call goes through /internal/github-proxy with
// the caller's own key. The daemon forwards only what github-proxy.json lists, so these tests also
// keep that file in step with the calls server.js makes.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { Octokit } from "@octokit/rest";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const RUN_KEY = "oct_proxy_run_key";
const PROXY = "/internal/github-proxy";
const TABLE = JSON.parse(readFileSync(path.join(HERE, "github-proxy.json"), "utf8"));
const SOURCE = readFileSync(path.join(HERE, "server.js"), "utf8");
const PR = { number: 7, title: "T", state: "open", user: { login: "a" }, head: { ref: "f" }, base: { ref: "main" } };
const USE_PROXY = { error: "use the proxy", code: "api.github.errors.use_proxy" };

const daemonCalls = [];
const githubCalls = [];
let tokenReply = null;
let proxyReply = null;
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

function reply(res, status, body, headers = {}) {
  res.writeHead(status, { "content-type": "application/json", ...headers });
  res.end(JSON.stringify(body));
}

before(async () => {
  daemon = recorder(daemonCalls, (_req, res, call) => {
    if (call.path.startsWith(`${PROXY}/`)) {
      const [status, body, headers] = proxyReply?.(call, call.path.slice(PROXY.length)) ?? [404, {}];
      return reply(res, status, body, headers);
    }
    return (tokenReply ?? ((r) => reply(r, 409, USE_PROXY)))(res, call);
  });
  github = recorder(githubCalls, (_req, res) => reply(res, 500, { message: "GitHub must not be called directly" }));
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

async function inSession(query, routes, fn) {
  proxyReply = routes;
  const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  await client.connect(
    new SSEClientTransport(new URL(`http://127.0.0.1:${mcpPort}/sse?${query}`), {
      requestInit: { headers: { "X-API-Key": RUN_KEY } },
    }),
  );
  try {
    daemonCalls.length = 0;
    githubCalls.length = 0;
    await fn(client);
  } finally {
    proxyReply = null;
    tokenReply = null;
    await client.close();
  }
}

const proxied = () => daemonCalls.filter((c) => c.path.startsWith(`${PROXY}/`));

function assertProxied(calls) {
  assert.ok(calls.length > 0, "nothing went through the proxy");
  for (const c of calls) {
    assert.equal(c.headers["x-api-key"], RUN_KEY, `${c.method} ${c.path} without the caller's key`);
    assert.equal(c.headers.authorization, undefined, `${c.method} ${c.path} carried an Authorization header`);
  }
  assert.equal(githubCalls.length, 0, "GitHub was called directly");
}

test("a task run on a PAT project reads a PR through the proxy with its own key", async () => {
  const routes = (c, p) =>
    c.method === "GET" && p === "/repos/acme/widgets/pulls/7"
      ? [200, PR]
      : [404, {}];
  await inSession("task=t9&scope=read", routes, async (c) => {
    const r = await c.callTool({ name: "pr_view", arguments: { repo: "acme/widgets", number: 7 } });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(daemonCalls[0].path, "/internal/tasks/t9/github-token");
    assertProxied(proxied());
    assert.equal(proxied()[0].headers["x-thulu-repo"], "acme/widgets");
  });
});

test("pages come back through the proxy on the relative links it writes", async () => {
  const routes = (c, p) => {
    if (p !== "/repos/o/r/issues/5/comments") return [404, {}];
    const page = Number(c.query.get("page") ?? 1);
    const comments = [{ id: page, user: { login: "x" }, author_association: "MEMBER", body: "hi", created_at: "t" }];
    const link = page < 2 ? { link: `</repos/o/r/issues/5/comments?per_page=100&page=${page + 1}>; rel="next"` } : {};
    return [200, comments, link];
  };
  await inSession("task=t9&scope=read", routes, async (c) => {
    const r = await c.callTool({ name: "pr_comments_list", arguments: { repo: "o/r", number: 5 } });
    assert.ok(!r.isError, r.content[0].text);
    assert.deepEqual(
      JSON.parse(r.content[0].text).comments.map((cm) => cm.id),
      [1, 2],
    );
    assert.deepEqual(
      proxied().map((p) => `${p.path}?page=${p.query.get("page") ?? 1}`),
      [`${PROXY}/repos/o/r/issues/5/comments?page=1`, `${PROXY}/repos/o/r/issues/5/comments?page=2`],
    );
    assertProxied(proxied());
  });
});

test("GraphQL goes through the proxy with a document from the shared table and the repository named", async () => {
  const routes = (c, p) => {
    if (p !== "/graphql") return [404, {}];
    const body = JSON.parse(c.body);
    if (body.query === TABLE.graphql.thread.query) {
      return [200, { data: { node: { id: "PRRT_1", isResolved: false, pullRequest: { number: 7, repository: { nameWithOwner: "acme/widgets" } } } } }];
    }
    return [200, { data: { resolveReviewThread: { thread: { id: "PRRT_1", isResolved: true } } } }];
  };
  await inSession("task=t9&scope=write", routes, async (c) => {
    const r = await c.callTool({ name: "pr_review_thread_resolve", arguments: { repo: "acme/widgets", thread_id: "PRRT_1" } });
    assert.ok(!r.isError, r.content[0].text);
    const calls = proxied();
    assert.deepEqual(
      calls.map((x) => JSON.parse(x.body).query),
      [TABLE.graphql.thread.query, TABLE.graphql.resolve_thread.query],
    );
    for (const x of calls) assert.equal(x.headers["x-thulu-repo"], "acme/widgets");
    assertProxied(calls);
  });
});

test("a chat asking by repository switches to the proxy too", async () => {
  const routes = (_c, p) => (p === "/repos/acme/widgets/pulls/7" ? [200, PR] : [404, {}]);
  await inSession("tools=pr_view", routes, async (c) => {
    const r = await c.callTool({ name: "pr_view", arguments: { repo: "acme/widgets", number: 7 } });
    assert.ok(!r.isError, r.content[0].text);
    assert.equal(daemonCalls[0].path, "/internal/github-token");
    assertProxied(proxied());
  });
});

test("the proxy's refusal reaches the agent with its reason", async () => {
  const refusal = { error: 'a person has to approve the "deploy_prod" approval', code: "api.github.errors.proxy_approval_required" };
  const routes = (c, p) => (p.endsWith("/dispatches") ? [403, refusal] : [200, { workflow_runs: [] }]);
  await inSession("task=t9&scope=deploy&tools=run_dispatch", routes, async (c) => {
    const r = await c.callTool({ name: "run_dispatch", arguments: { repo: "acme/widgets", workflow_id: "deploy.yml", ref: "main" } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /deploy_prod/);
    assertProxied(proxied());
  });
});

test("a 409 that is not the proxy signal is an error, not a switch", async () => {
  tokenReply = (res) => reply(res, 409, { error: "something else", code: "api.github.errors.other" });
  await inSession("tools=pr_view", null, async (c) => {
    const r = await c.callTool({ name: "pr_view", arguments: { repo: "acme/widgets", number: 7 } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /something else/);
    assert.equal(proxied().length, 0);
  });
});

// Every octokit method server.js calls must be in github-proxy.json with the same method and path,
// or it works with an App token and fails on every PAT project.
test("github-proxy.json lists every REST call server.js makes, with octokit's own route", () => {
  const used = new Set();
  for (const m of SOURCE.matchAll(/octokit\.([a-z]+)\.([a-zA-Z]+)/g)) {
    if (m[1] !== "hook" && m[1] !== "paginate") used.add(`${m[1]}.${m[2]}`);
  }
  const listed = new Map(TABLE.rest.map((r) => [r.octokit, r]));
  const octokit = new Octokit();
  for (const name of used) {
    const entry = listed.get(name);
    assert.ok(entry, `server.js calls octokit.${name}, which github-proxy.json does not list`);
    const [ns, fn] = name.split(".");
    const { method, url } = octokit[ns][fn].endpoint.DEFAULTS;
    assert.equal(entry.method, method, `${name}: method`);
    assert.equal(entry.path, url, `${name}: path`);
  }
  for (const name of listed.keys()) assert.ok(used.has(name), `github-proxy.json lists ${name}, which server.js never calls`);
});

test("github-proxy.json's GraphQL documents are the ones server.js sends, already normalized", () => {
  const used = new Set([...SOURCE.matchAll(/GQL\.([a-z_]+)/g)].map((m) => m[1]));
  assert.deepEqual([...used].sort(), Object.keys(TABLE.graphql).sort());
  for (const [name, doc] of Object.entries(TABLE.graphql)) {
    assert.equal(doc.query, doc.query.split(/\s+/).filter(Boolean).join(" "), `${name} is not whitespace-normalized`);
    for (const v of doc.query.matchAll(/\$([a-zA-Z_]+)\s*:/g)) {
      assert.ok(doc.variables.includes(v[1]), `${name} declares $${v[1]} but the table does not allow it`);
    }
  }
});

test("every tool named in github-proxy.json exists in tool.yaml", () => {
  const yaml = readFileSync(path.join(HERE, "tool.yaml"), "utf8");
  const tools = new Set([...yaml.matchAll(/^\s*-\s*name:\s*([a-z_]+)\s*$/gm)].map((m) => m[1]));
  const named = [...TABLE.rest.flatMap((r) => r.tools), ...Object.values(TABLE.graphql).flatMap((d) => d.tools)];
  for (const tool of named) assert.ok(tools.has(tool), `${tool} is not a thulu-github tool`);
});
