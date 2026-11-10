import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import path from "node:path";
import { after, before, describe, test } from "node:test";
import { fileURLToPath } from "node:url";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

import { admitHandshake, admitMessage, grantedTools, sameKey } from "../lib/session-auth.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));

describe("admitHandshake", () => {
  test("a shared server refuses a handshake with no key", () => {
    const r = admitHandshake({ key: "", catalog: false });
    assert.equal(r.ok, false);
    assert.equal(r.status, 401);
  });

  test("a shared server opens a session for any run key", () => {
    assert.deepEqual(admitHandshake({ key: "oct_a", catalog: false }), { ok: true, key: "oct_a", listOnly: false });
  });

  test("the keyless catalog probe gets a list-only session", () => {
    assert.deepEqual(admitHandshake({ key: "", catalog: true }), { ok: true, key: "", listOnly: true });
  });

  test("a per-run browser takes only its run's key", () => {
    assert.equal(admitHandshake({ key: "oct_run", catalog: false, requiredKey: "oct_run" }).ok, true);
    assert.equal(admitHandshake({ key: "oct_other", catalog: false, requiredKey: "oct_run" }).ok, false);
  });

  test("a per-run browser refuses the keyless catalog probe too", () => {
    assert.equal(admitHandshake({ key: "", catalog: true, requiredKey: "oct_run" }).ok, false);
  });

  test("a catalog-only server opens nothing but a list-only catalog session", () => {
    const r = admitHandshake({ key: "oct_a", catalog: false, catalogOnly: true });
    assert.equal(r.ok, false);
    assert.equal(r.status, 403);
    assert.deepEqual(admitHandshake({ key: "oct_a", catalog: true, catalogOnly: true }), { ok: true, key: "", listOnly: true });
    assert.deepEqual(admitHandshake({ key: "", catalog: true, catalogOnly: true }), { ok: true, key: "", listOnly: true });
  });
});

describe("admitMessage", () => {
  test("a POST must repeat the session's key", () => {
    assert.equal(admitMessage("oct_a", "oct_a"), true);
    assert.equal(admitMessage("oct_a", "oct_b"), false);
    assert.equal(admitMessage("oct_a", ""), false);
  });

  test("a list-only session has no key to impersonate", () => {
    assert.equal(admitMessage("", ""), true);
  });

  test("sameKey never matches an empty key", () => {
    assert.equal(sameKey("", ""), false);
    assert.equal(sameKey("a", "ab"), false);
  });
});

describe("grantedTools", () => {
  test("without a grant the request decides, as on a shared server", () => {
    assert.equal(grantedTools(null, null), null);
    assert.deepEqual(grantedTools(["screenshot"], null), ["screenshot"]);
  });

  test("a request can narrow the grant and never widen it", () => {
    assert.deepEqual(grantedTools(null, ["screenshot"]), ["screenshot"]);
    assert.deepEqual(grantedTools(["screenshot", "browser_run_script"], ["screenshot"]), ["screenshot"]);
    assert.deepEqual(grantedTools(["browser_run_script"], ["screenshot"]), []);
  });
});

const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

async function freePort() {
  const probe = http.createServer();
  const port = await listen(probe);
  await new Promise((r) => probe.close(r));
  return port;
}

async function startServer(env) {
  const port = await freePort();
  const child = spawn(process.execPath, [path.join(HERE, "..", "server.js")], {
    env: { ...process.env, PORT: String(port), LISTEN_HOST: "127.0.0.1", OCTOPUS_API: "http://127.0.0.1:9", ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("thulu-browser did not start in time")), 15000);
    child.stdout.on("data", (d) => {
      if (String(d).includes("listening on")) {
        clearTimeout(timer);
        resolve();
      }
    });
    child.on("exit", (code) => reject(new Error(`thulu-browser exited early (${code})`)));
  });
  return { child, base: `http://127.0.0.1:${port}` };
}

async function connect(base, query, key) {
  const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  const headers = key ? { "X-API-Key": key } : {};
  await client.connect(new SSEClientTransport(new URL(`${base}/sse?${query}`), {
    requestInit: { headers },
    eventSourceInit: { fetch: (url, init) => fetch(url, { ...init, headers: { ...init?.headers, ...headers } }) },
  }));
  return client;
}

async function rawEndpoint(base, key) {
  const ctrl = new AbortController();
  const res = await fetch(`${base}/sse`, { headers: { "X-API-Key": key }, signal: ctrl.signal });
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

describe("shared server", () => {
  let srv;
  before(async () => { srv = await startServer({}); });
  after(() => srv?.child.kill("SIGKILL"));

  test("GET /sse without a key is refused", async () => {
    const r = await fetch(`${srv.base}/sse`);
    assert.equal(r.status, 401);
  });

  test("a keyed session lists the tools", async () => {
    const c = await connect(srv.base, "", "oct_a");
    const { tools } = await c.listTools();
    assert.ok(tools.some((t) => t.name === "browser_run_script"));
    await c.close();
  });

  test("the catalog probe lists the tools but cannot run one", async () => {
    const c = await connect(srv.base, "catalog=1", "");
    const { tools } = await c.listTools();
    assert.ok(tools.length > 0);
    const r = await c.callTool({ name: "browser_new_session", arguments: {} });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /without a credential/);
    await c.close();
  });

  test("a POST with another caller's key is refused, even with the right session id", async () => {
    const { endpoint, close } = await rawEndpoint(srv.base, "oct_victim");
    try {
      const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" });
      const stolen = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-API-Key": "oct_attacker" },
        body,
      });
      assert.equal(stolen.status, 401);
      const keyless = await fetch(endpoint, { method: "POST", headers: { "Content-Type": "application/json" }, body });
      assert.equal(keyless.status, 401);
      const own = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-API-Key": "oct_victim" },
        body,
      });
      assert.equal(own.status, 202);
    } finally {
      close();
    }
  });
});

describe("catalog-only server", () => {
  let srv;
  before(async () => { srv = await startServer({ MCP_CATALOG_ONLY: "1" }); });
  after(() => srv?.child.kill("SIGKILL"));

  test("a run key does not open a session", async () => {
    assert.equal((await fetch(`${srv.base}/sse`, { headers: { "X-API-Key": "oct_a" } })).status, 403);
  });

  test("the catalog lists the tools, and a keyed catalog session still cannot run one", async () => {
    const c = await connect(srv.base, "catalog=1", "oct_a");
    const { tools } = await c.listTools();
    assert.ok(tools.some((t) => t.name === "browser_run_script"));
    const r = await c.callTool({ name: "browser_run_script", arguments: { session_id: "x", code: "return 1" } });
    assert.equal(r.isError, true);
    assert.match(r.content[0].text, /without a credential/);
    await c.close();
  });
});

describe("per-run server", () => {
  let srv;
  before(async () => { srv = await startServer({ MCP_REQUIRED_KEY: "oct_run" }); });
  after(() => srv?.child.kill("SIGKILL"));

  test("only the run's key opens a session", async () => {
    assert.equal((await fetch(`${srv.base}/sse`, { headers: { "X-API-Key": "oct_other" } })).status, 401);
    assert.equal((await fetch(`${srv.base}/sse?catalog=1`)).status, 401);
    const c = await connect(srv.base, "", "oct_run");
    const { tools } = await c.listTools();
    assert.ok(tools.length > 0);
    await c.close();
  });
});

describe("per-run server with a tool grant", () => {
  let srv;
  before(async () => {
    srv = await startServer({ MCP_REQUIRED_KEY: "oct_run", MCP_ALLOWED_TOOLS: "screenshot,get_text" });
  });
  after(() => srv?.child.kill("SIGKILL"));

  test("the run's own ?tools= cannot reach a tool its stage did not grant", async () => {
    const c = await connect(srv.base, "tools=browser_run_script,screenshot", "oct_run");
    const { tools } = await c.listTools();
    assert.deepEqual(tools.map((t) => t.name), ["screenshot"]);
    const r = await c.callTool({ name: "browser_run_script", arguments: { session_id: "x", code: "return 1" } });
    assert.equal(r.isError, true);
    await c.close();
  });

  test("with no ?tools= the session gets exactly the grant", async () => {
    const c = await connect(srv.base, "", "oct_run");
    const { tools } = await c.listTools();
    assert.deepEqual(tools.map((t) => t.name).sort(), ["get_text", "screenshot"]);
    await c.close();
  });
});
