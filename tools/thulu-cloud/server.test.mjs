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

const daemonCalls = [];
let daemon;
let mcp;
let base;

const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

before(async () => {
  daemon = http.createServer((req, res) => {
    const key = req.headers["x-api-key"];
    daemonCalls.push({ path: req.url, key });
    req.resume();
    req.on("end", () => {
      const reply = (status, body) => {
        res.writeHead(status, { "content-type": "application/json" });
        res.end(JSON.stringify(body));
      };
      if (req.url.startsWith("/internal/environments/env1")) {
        if (key !== RUN_KEY) return reply(403, { error: "forbidden" });
        return reply(200, { environment_id: "env1", project_id: "p1", provider: "azure" });
      }
      if (req.url.startsWith("/internal/credentials/resolve")) {
        if (key !== RUN_KEY) return reply(403, { error: "forbidden" });
        return reply(200, { name: "prod", provider: "azure", kind: "azure_key", payload: { secret: "s3cret" } });
      }
      return reply(200, { environments: [] });
    });
  });
  const daemonPort = await listen(daemon);

  const probe = http.createServer();
  const port = await listen(probe);
  await new Promise((r) => probe.close(r));
  base = `http://127.0.0.1:${port}`;

  mcp = spawn(process.execPath, [path.join(HERE, "server.js")], {
    env: { ...process.env, PORT: String(port), OCTOPUS_API: `http://127.0.0.1:${daemonPort}` },
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
});

async function rawEndpoint(key) {
  const ctrl = new AbortController();
  const res = await fetch(`${base}/sse?task=t1`, { headers: { "X-API-Key": key }, signal: ctrl.signal });
  const reader = res.body.getReader();
  let buf = "";
  while (!buf.includes("\n\n")) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += new TextDecoder().decode(value);
  }
  const data = buf.split("\n").find((l) => l.startsWith("data:"));
  return { endpoint: new URL(data.slice(5).trim(), base).toString(), close: () => ctrl.abort() };
}

test("a handshake without a run key is refused", async () => {
  assert.equal((await fetch(`${base}/sse?task=t1`)).status, 401);
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
  const c = new Client({ name: "probe", version: "0" }, { capabilities: {} });
  await c.connect(new SSEClientTransport(new URL(`${base}/sse?catalog=1`)));
  try {
    const { tools } = await c.listTools();
    assert.ok(tools.some((t) => t.name === "environments_list"));
    daemonCalls.length = 0;
    const r = await c.callTool({ name: "environments_list", arguments: {} });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /without a credential/);
    assert.equal(daemonCalls.length, 0, "a list-only session must never reach the daemon");
  } finally {
    await c.close();
  }
});

test("a scope keyword inside ?tools= grants that scope's tools, not a tool named 'read'", async () => {
  const c = new Client({ name: "agent", version: "0" }, { capabilities: {} });
  await c.connect(
    new SSEClientTransport(new URL(`${base}/sse?tools=read,metrics_query`), {
      requestInit: { headers: { "X-API-Key": "k-scope-list" } },
    }),
  );
  try {
    const names = (await c.listTools()).tools.map((t) => t.name);
    assert.ok(names.includes("environments_list"), names.join(","));
    assert.ok(names.includes("logs_search"), names.join(","));
  } finally {
    await c.close();
  }
});

function keyedClient(key) {
  const c = new Client({ name: "agent", version: "0" }, { capabilities: {} });
  return c
    .connect(new SSEClientTransport(new URL(`${base}/sse`), { requestInit: { headers: { "X-API-Key": key } } }))
    .then(() => c);
}

test("what the daemon granted one key is never served to another from the cache", async () => {
  const owner = await keyedClient(RUN_KEY);
  try {
    const r = await owner.callTool({ name: "discover_resources", arguments: { environment_id: "env1" } });
    assert.match(r.content[0].text, /azure/, "the owner's call reached the provider check");
  } finally {
    await owner.close();
  }

  daemonCalls.length = 0;
  const other = await keyedClient("oct_other_workspace");
  try {
    const r = await other.callTool({ name: "discover_resources", arguments: { environment_id: "env1" } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /403/);
    assert.ok(
      daemonCalls.some((c) => c.path.startsWith("/internal/environments/env1") && c.key === "oct_other_workspace"),
      "the daemon must be asked with the other key, not skipped on a cache hit",
    );
  } finally {
    await other.close();
  }

  daemonCalls.length = 0;
  const res = await fetch(`${base}/collect`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ environment_id: "env1", mappings: [{ id: "m1", source: {} }] }),
  });
  assert.equal(res.status, 400);
  assert.match((await res.json()).error, /credential/);
  assert.equal(daemonCalls.length, 0, "a collect without a target must not call the daemon");
});

test("POST /collect reads with the target the daemon sent", async () => {
  daemonCalls.length = 0;
  const res = await fetch(`${base}/collect`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      environment_id: "env9",
      environment: { environment_id: "env9", project_id: "p1", provider: "azure" },
      credential: { name: "prod", provider: "azure", kind: "azure_key", payload: { secret: "s3cret" } },
      backfill_hours: 24,
      mappings: [{ id: "m1", service: "api", signal: "error_rate", source: {}, backfill: true }],
    }),
  });
  assert.equal(res.status, 200);
  const body = await res.json();
  assert.equal(body.environment_id, "env9");
  assert.match(body.skipped, /azure/, "reached the provider check with the sent environment");
  assert.equal(daemonCalls.length, 0, "the collect must not call the daemon");
});

test("POST /collect refuses a target for another environment id", async () => {
  const res = await fetch(`${base}/collect`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      environment_id: "env9",
      environment: { environment_id: "env1", provider: "aws" },
      credential: { provider: "aws", kind: "aws_keys", payload: { access_key_id: "x", secret_access_key: "y" } },
      mappings: [],
    }),
  });
  assert.equal(res.status, 400);
});
