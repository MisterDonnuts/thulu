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
const REFUSED_KEY = "oct_refused_key";
const FIGMA_TOKEN = "figd_test_token";
const FILE_KEY = "filekeyfilekey00";
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=",
  "base64",
);

const daemonCalls = [];
const figmaCalls = [];
let daemon;
let figma;
let figmaBase;
let mcp;
let base;

const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

const reply = (res, status, body, type = "application/json") => {
  res.writeHead(status, { "content-type": type });
  res.end(type === "application/json" ? JSON.stringify(body) : body);
};

const fileDoc = {
  name: "App",
  lastModified: "2026-10-20T12:00:00Z",
  version: "42",
  editorType: "figma",
  document: {
    id: "0:0",
    type: "DOCUMENT",
    children: [
      {
        id: "0:1",
        name: "Screens",
        type: "CANVAS",
        children: [{ id: "1:2", name: "Login", type: "FRAME", children: [{ id: "1:3", name: "Title", type: "TEXT" }] }],
      },
    ],
  },
  components: { "5:1": { key: "k", name: "Button/Primary", description: "" } },
  styles: { "S:1": { key: "s", name: "Brand/500", styleType: "FILL" } },
};

before(async () => {
  daemon = http.createServer((req, res) => {
    const key = req.headers["x-api-key"];
    daemonCalls.push({ method: req.method, path: req.url, key });
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      if (req.url.startsWith("/internal/credentials/resolve")) {
        const q = new URL(req.url, "http://x").searchParams;
        if (key === REFUSED_KEY) {
          return reply(res, 403, {
            decision: { allowed: false, reason: "tool_disabled_by_policy", detail: `${q.get("tool")} is switched off` },
          });
        }
        if (key !== RUN_KEY) return reply(res, 401, { error: "unauthorized" });
        return reply(res, 200, { name: "design", provider: "figma", kind: "figma_pat", payload: { token: FIGMA_TOKEN } });
      }
      if (req.method === "POST" && req.url === "/tasks/t1/attachments") {
        if (key !== RUN_KEY) return reply(res, 401, { error: "unauthorized" });
        const body = Buffer.concat(chunks).toString("latin1");
        daemonCalls.at(-1).body = body;
        return reply(res, 201, { id: "att1" });
      }
      return reply(res, 404, { error: "not found" });
    });
  });
  const daemonPort = await listen(daemon);

  figma = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    figmaCalls.push({ path: url.pathname, query: Object.fromEntries(url.searchParams), token: req.headers["x-figma-token"] });
    if (url.pathname === "/img/1-2.png") return reply(res, 200, PNG, "image/png");
    if (req.headers["x-figma-token"] !== FIGMA_TOKEN) return reply(res, 403, { status: 403, err: "Invalid token" });
    switch (url.pathname) {
      case `/v1/files/${FILE_KEY}`:
        return reply(res, 200, fileDoc);
      case `/v1/files/${FILE_KEY}/nodes`:
        return reply(res, 200, {
          name: "App",
          nodes: {
            "1:2": {
              document: {
                id: "1:2",
                name: "Login",
                type: "FRAME",
                absoluteBoundingBox: { x: 0, y: 0, width: 390, height: 844 },
                fills: [{ type: "SOLID", color: { r: 1, g: 1, b: 1, a: 1 } }],
                styles: { fill: "S:1" },
                children: [],
              },
              styles: { "S:1": { name: "Brand/500" } },
            },
          },
        });
      case `/v1/images/${FILE_KEY}`:
        return reply(res, 200, { err: null, images: { "1:2": `${figmaBase}/img/1-2.png`, "9:9": null } });
      case `/v1/files/${FILE_KEY}/comments`:
        return reply(res, 200, {
          comments: [
            { id: "c1", message: "Too tight", user: { handle: "ana" }, created_at: "2026-10-01T00:00:00Z", client_meta: { node_id: "1:2" } },
            { id: "c2", message: "Done", user: { handle: "ana" }, created_at: "2026-09-01T00:00:00Z", resolved_at: "2026-09-02T00:00:00Z" },
          ],
        });
      case "/v1/teams/123/projects":
        return reply(res, 200, { name: "Design", projects: [{ id: 9, name: "Mobile" }] });
      case "/v1/projects/9/files":
        return reply(res, 200, {
          name: "Mobile",
          files: [{ key: FILE_KEY, name: "App", thumbnail_url: "https://x", last_modified: "2026-10-20T12:00:00Z" }],
        });
      default:
        return reply(res, 404, { status: 404, err: "Not found" });
    }
  });
  const figmaPort = await listen(figma);
  figmaBase = `http://127.0.0.1:${figmaPort}`;

  const probe = http.createServer();
  const port = await listen(probe);
  await new Promise((r) => probe.close(r));
  base = `http://127.0.0.1:${port}`;

  mcp = spawn(process.execPath, [path.join(HERE, "server.js")], {
    env: {
      ...process.env,
      PORT: String(port),
      OCTOPUS_API: `http://127.0.0.1:${daemonPort}`,
      FIGMA_API_URL: figmaBase,
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
  await new Promise((r) => figma.close(r));
});

async function connect(key, query = "task=t1") {
  const headers = key ? { "X-API-Key": key } : {};
  const transport = new SSEClientTransport(new URL(`${base}/sse?${query}`), {
    eventSourceInit: { fetch: (url, init) => fetch(url, { ...init, headers: { ...init?.headers, ...headers } }) },
    requestInit: { headers },
  });
  const c = new Client({ name: "test", version: "0" }, { capabilities: {} });
  await c.connect(transport);
  return c;
}

async function call(name, args, key = RUN_KEY, query) {
  const c = await connect(key, query);
  try {
    return await c.callTool({ name, arguments: args });
  } finally {
    await c.close();
  }
}

const json = (result) => JSON.parse(result.content[0].text);

test("a handshake without a run key is refused", async () => {
  assert.equal((await fetch(`${base}/sse?task=t1`)).status, 401);
});

test("a POST on a leaked session id without that session's key is refused", async () => {
  const ctrl = new AbortController();
  const res = await fetch(`${base}/sse?task=t1`, { headers: { "X-API-Key": RUN_KEY }, signal: ctrl.signal });
  const reader = res.body.getReader();
  let buf = "";
  while (!buf.includes("\n\n")) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += new TextDecoder().decode(value);
  }
  const data = buf.split("\n").find((l) => l.startsWith("data:"));
  const endpoint = new URL(data.slice(5).trim(), base).toString();
  try {
    const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" });
    const post = (headers) =>
      fetch(endpoint, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body });
    assert.equal((await post({ "X-API-Key": "oct_attacker" })).status, 401);
    assert.equal((await post({})).status, 401);
    assert.equal((await post({ "X-API-Key": RUN_KEY })).status, 202);
  } finally {
    ctrl.abort();
  }
});

test("the keyless catalog probe lists every tool and cannot call one", async () => {
  const c = await connect("", "catalog=1");
  try {
    const { tools } = await c.listTools();
    assert.deepEqual(tools.map((tl) => tl.name).sort(), [
      "comments_list",
      "file_get",
      "images_render",
      "nodes_get",
      "project_files_list",
      "team_projects_list",
    ]);
    const res = await c.callTool({ name: "file_get", arguments: { file_key: FILE_KEY } });
    assert.equal(res.isError, true);
  } finally {
    await c.close();
  }
});

test("an explicit tool list narrows what a session may call", async () => {
  const res = await call("comments_list", { file_key: FILE_KEY }, RUN_KEY, "task=t1&tools=file_get");
  assert.equal(res.isError, true);
  assert.match(res.content[0].text, /comments_list/);
});

test("the token comes from the daemon with the caller's key and reaches Figma as X-Figma-Token", async () => {
  daemonCalls.length = 0;
  figmaCalls.length = 0;
  const res = await call("file_get", { file_key: FILE_KEY });
  assert.equal(res.isError, undefined, res.content[0].text);
  const resolve = daemonCalls.find((c) => c.path.startsWith("/internal/credentials/resolve"));
  assert.equal(resolve.key, RUN_KEY);
  const q = new URL(resolve.path, "http://x").searchParams;
  assert.equal(q.get("server"), "thulu-figma");
  assert.equal(q.get("tool"), "file_get");
  assert.equal(figmaCalls[0].token, FIGMA_TOKEN);
  assert.ok(!res.content[0].text.includes(FIGMA_TOKEN), "the token never reaches the agent");
});

test("a refusal carries the daemon's own wording", async () => {
  const res = await call("file_get", { file_key: FILE_KEY }, REFUSED_KEY);
  assert.equal(res.isError, true);
  assert.match(res.content[0].text, /file_get is switched off/);
});

test("file_get returns the outline and the library, from a link as well as a key", async () => {
  figmaCalls.length = 0;
  const out = json(await call("file_get", { file_key: `https://www.figma.com/design/${FILE_KEY}/App` }));
  assert.equal(figmaCalls[0].query.depth, "2");
  assert.equal(out.name, "App");
  assert.equal(out.pages[0].children[0].id, "1:2");
  assert.equal(out.components[0].name, "Button/Primary");
  assert.deepEqual(out.styles.fill, [{ id: "S:1", name: "Brand/500" }]);
});

test("file_get refuses what is not a file reference before calling Figma", async () => {
  figmaCalls.length = 0;
  const res = await call("file_get", { file_key: "../../v1/me" });
  assert.equal(res.isError, true);
  assert.equal(figmaCalls.length, 0);
});

test("nodes_get takes the node from the link and resolves style names", async () => {
  figmaCalls.length = 0;
  const out = json(await call("nodes_get", { file_key: `https://www.figma.com/design/${FILE_KEY}/App?node-id=1-2` }));
  assert.equal(figmaCalls[0].query.ids, "1:2");
  assert.deepEqual(out.nodes[0].box, [0, 0, 390, 844]);
  assert.deepEqual(out.nodes[0].fills, ["#ffffff"]);
  assert.deepEqual(out.nodes[0].styles, { fill: "Brand/500" });
});

test("images_render shows PNGs inline, reports empty renders and attaches to the task", async () => {
  daemonCalls.length = 0;
  const res = await call("images_render", { file_key: FILE_KEY, node_ids: ["1:2", "9:9"], attach: true });
  assert.equal(res.isError, undefined, res.content[0].text);
  const summary = json(res);
  const [rendered, empty] = summary.images;
  assert.equal(rendered.inline, true);
  assert.equal(rendered.attachment.id, "att1");
  assert.equal(empty.url, null);
  assert.ok(empty.error);
  const image = res.content.find((c) => c.type === "image");
  assert.equal(image.mimeType, "image/png");
  assert.deepEqual(Buffer.from(image.data, "base64"), PNG);
  const upload = daemonCalls.find((c) => c.method === "POST");
  assert.equal(upload.key, RUN_KEY);
  assert.match(upload.body, /filename="figma-filekeyfilekey00-1-2\.png"/);
});

test("images_render with attach and no task says so instead of dropping the files", async () => {
  const res = await call("images_render", { file_key: FILE_KEY, node_ids: ["1:2"], attach: true }, RUN_KEY, "scope=read");
  assert.equal(res.isError, true);
  assert.match(res.content[0].text, /attach/);
});

test("comments_list hides resolved threads by default", async () => {
  const out = json(await call("comments_list", { file_key: FILE_KEY }));
  assert.deepEqual(out.threads.map((th) => th.id), ["c1"]);
  assert.equal(out.resolved_hidden, 1);
  assert.deepEqual(out.threads[0].anchor, { node: "1:2" });
});

test("team projects and project files come back without thumbnails", async () => {
  const projects = json(await call("team_projects_list", { team_id: "https://www.figma.com/files/team/123/Design" }));
  assert.deepEqual(projects.projects, [{ id: "9", name: "Mobile" }]);
  const files = json(await call("project_files_list", { project_id: "9" }));
  assert.deepEqual(files.files, [{ key: FILE_KEY, name: "App", last_modified: "2026-10-20T12:00:00Z" }]);
});

test("a Figma error reaches the agent with Figma's wording", async () => {
  const res = await call("project_files_list", { project_id: "404" });
  assert.equal(res.isError, true);
  assert.match(res.content[0].text, /HTTP 404: Not found/);
});
