import assert from "node:assert/strict";
import crypto from "node:crypto";
import http from "node:http";
import net from "node:net";
import { after, before, test } from "node:test";

import { chromium } from "playwright";

import { chromiumMissing } from "./chromium.js";

process.env.HOST_GATEWAY = "127.0.0.1";
const { callerHostLoopback, closeHostProxy, hostContextOptions, isLoopbackHost, startHostProxy, upstreamHost } =
  await import("../lib/host-proxy.js");

const noChromium = await chromiumMissing();

const page = `<!doctype html><title>app</title><p id=o></p><script>
fetch('/api').then(r=>r.json()).then(j=>{
  const o=document.getElementById('o'); o.textContent='api:'+j.ok+' cookie:'+document.cookie;
  const ws=new WebSocket('ws://'+location.host+'/ws'); ws.onmessage=e=>{o.textContent+=' ws:'+e.data; document.title='done'};
});</script>`;

let app;
let appPort;
const hosts = [];
const upgraded = [];
before(async () => {
  app = http.createServer((req, res) => {
    hosts.push(req.headers.host);
    if (req.url === "/api") {
      res.setHeader("Set-Cookie", "sid=abc; Path=/");
      res.setHeader("Content-Type", "application/json");
      res.end('{"ok":true}');
      return;
    }
    res.setHeader("Content-Type", "text/html");
    res.end(page);
  });
  app.on("upgrade", (req, sock) => {
    upgraded.push(sock);
    const accept = crypto.createHash("sha1")
      .update(req.headers["sec-websocket-key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
    sock.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
    sock.write(Buffer.concat([Buffer.from([0x81, 4]), Buffer.from("pong")]));
  });
  await new Promise((r) => app.listen(0, "127.0.0.1", r));
  appPort = app.address().port;
});
after(() => {
  for (const s of upgraded) s.destroy();
  app.closeAllConnections();
  app.close();
});

test("loopback names go to the gateway, the rest where they were going", () => {
  for (const h of ["localhost", "LOCALHOST.", "app.localhost", "127.0.0.1", "127.1.2.3", "[::1]", "::1", "0.0.0.0"]) {
    assert.ok(isLoopbackHost(h), h);
    assert.equal(upstreamHost(h, "host.docker.internal"), "host.docker.internal");
  }
  for (const h of ["example.com", "localhost.example.com", "10.0.0.1", "[2001:db8::1]"]) {
    assert.ok(!isLoopbackHost(h), h);
  }
  assert.equal(upstreamHost("[2001:db8::1]", "gw"), "2001:db8::1");
});

test("plain HTTP and CONNECT reach the app; an unknown host is a 502", async () => {
  const { server, url } = await startHostProxy("127.0.0.1");
  try {
    const proxyPort = new URL(url).port;
    const get = (target) => new Promise((resolve, reject) => {
      http.get({ host: "127.0.0.1", port: proxyPort, path: target, headers: { Host: new URL(target).host } }, (res) => {
        let body = "";
        res.on("data", (c) => (body += c));
        res.on("end", () => resolve({ status: res.statusCode, body }));
      }).on("error", reject);
    });
    const ok = await get(`http://app.localhost:${appPort}/api`);
    assert.equal(ok.status, 200);
    assert.equal(ok.body, '{"ok":true}');
    assert.equal(hosts.at(-1), `app.localhost:${appPort}`, "the app sees the Host the page asked for");
    assert.equal((await get("http://nowhere.invalid/")).status, 502);

    const tunneled = await new Promise((resolve, reject) => {
      const s = net.connect(Number(proxyPort), "127.0.0.1", () => {
        s.write(`CONNECT localhost:${appPort} HTTP/1.1\r\nHost: localhost:${appPort}\r\n\r\n`);
      });
      let buf = "";
      let sent = false;
      s.on("data", (d) => {
        buf += d;
        if (!sent && buf.includes("\r\n\r\n")) {
          sent = true;
          buf = "";
          s.write(`GET /api HTTP/1.1\r\nHost: localhost:${appPort}\r\nConnection: close\r\n\r\n`);
        }
      });
      s.on("end", () => resolve(buf));
      s.on("error", reject);
    });
    assert.match(tunneled, /^HTTP\/1\.1 200[\s\S]*\{"ok":true\}$/);
  } finally {
    server.closeAllConnections();
    server.close();
  }
});

test("only a run on a runner gets the proxy", async () => {
  assert.deepEqual(await hostContextOptions(), {});
  assert.deepEqual(await callerHostLoopback.run(false, hostContextOptions), {});
  const opts = await callerHostLoopback.run(true, hostContextOptions);
  assert.match(opts.proxy.server, /^http:\/\/127\.0\.0\.1:\d+$/);
  await closeHostProxy();
});

test("a page keeps its localhost origin, cookie and websocket through the proxy", { skip: noChromium }, async () => {
  const { server, url } = await startHostProxy("127.0.0.1");
  let proxied = 0;
  server.on("request", () => proxied++);
  server.on("connect", () => proxied++);
  server.on("upgrade", () => proxied++);
  const browser = await chromium.launch({ args: ["--no-sandbox"] });
  try {
    const context = await browser.newContext({ proxy: { server: url } });
    const p = await context.newPage();
    await p.goto(`http://localhost:${appPort}/`);
    await p.waitForFunction(() => document.title === "done", null, { timeout: 10_000 });
    assert.equal(await p.evaluate(() => location.origin), `http://localhost:${appPort}`);
    assert.equal(await p.textContent("#o"), "api:true cookie:sid=abc ws:pong");
    assert.ok(proxied >= 2, `the page went around the proxy (${proxied} requests through it)`);
  } finally {
    await browser.close();
    server.closeAllConnections();
    server.close();
  }
});
