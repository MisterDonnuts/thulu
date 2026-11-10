// Runner runs browse through this proxy so loopback hosts reach HOST_GATEWAY while the page keeps
// its localhost origin
// (cookies, CORS, OAuth redirects, websockets). ?host_loopback=1 is set by
// internal/api/handle_mcp_proxy.go.

import { AsyncLocalStorage } from "node:async_hooks";
import http from "node:http";
import net from "node:net";

export const HOST_GATEWAY = (process.env.HOST_GATEWAY || "").trim();

export const callerHostLoopback = new AsyncLocalStorage();

export function isLoopbackHost(hostname) {
  const h = String(hostname || "").toLowerCase().replace(/^\[|\]$/g, "").replace(/\.$/, "");
  return h === "localhost" || h.endsWith(".localhost") || h === "::1" || h === "0.0.0.0" || /^127\.\d+\.\d+\.\d+$/.test(h);
}

export function upstreamHost(hostname, gateway) {
  return isLoopbackHost(hostname) ? gateway : String(hostname).replace(/^\[|\]$/g, "");
}

const BAD_REQUEST = "HTTP/1.1 400 Bad Request\r\n\r\n"; // i18n-ignore HTTP status line
const ESTABLISHED = "HTTP/1.1 200 Connection Established\r\n\r\n"; // i18n-ignore HTTP status line
const BAD_GATEWAY = "HTTP/1.1 502 Bad Gateway\r\n\r\n"; // i18n-ignore HTTP status line

const HOP_HEADERS = ["proxy-connection", "proxy-authorization", "keep-alive"];

export function startHostProxy(gateway) {
  const server = http.createServer((req, res) => {
    let u;
    try {
      u = new URL(req.url);
    } catch {
      res.writeHead(400).end();
      return;
    }
    const headers = { ...req.headers };
    for (const h of HOP_HEADERS) delete headers[h];
    const up = http.request(
      { host: upstreamHost(u.hostname, gateway), port: u.port || 80, method: req.method, path: u.pathname + u.search, headers },
      (r) => {
        res.writeHead(r.statusCode ?? 502, r.headers);
        r.pipe(res);
      },
    );
    up.on("error", (err) => {
      if (!res.headersSent) res.writeHead(502, { "Content-Type": "text/plain" });
      res.end(String(err.message));
    });
    req.pipe(up);
  });

  server.on("connect", (req, sock, head) => {
    const m = /^(.+):(\d+)$/.exec(req.url || "");
    if (!m) {
      sock.end(BAD_REQUEST);
      return;
    }
    tunnel(sock, upstreamHost(m[1], gateway), Number(m[2]), ESTABLISHED, head);
  });

  server.on("upgrade", (req, sock, head) => {
    let u;
    try {
      u = new URL(req.url);
    } catch {
      sock.end(BAD_REQUEST);
      return;
    }
    const lines = [`${req.method} ${u.pathname}${u.search} HTTP/1.1`];
    for (let i = 0; i < req.rawHeaders.length; i += 2) {
      if (!HOP_HEADERS.includes(req.rawHeaders[i].toLowerCase())) lines.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
    }
    tunnel(sock, upstreamHost(u.hostname, gateway), Number(u.port || 80), lines.join("\r\n") + "\r\n\r\n", head);
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve({ server, url: `http://127.0.0.1:${server.address().port}` }));
  });
}

function tunnel(sock, host, port, first, head) {
  const toBrowser = first === ESTABLISHED;
  const up = net.connect(port, host, () => {
    if (toBrowser) sock.write(first);
    else up.write(first);
    if (head?.length) up.write(head);
    up.pipe(sock);
    sock.pipe(up);
  });
  up.on("error", () => {
    if (sock.writable && toBrowser) sock.end(BAD_GATEWAY);
    else sock.destroy();
  });
  sock.on("error", () => up.destroy());
}

let proxyPromise = null;

export async function hostContextOptions() {
  if (!HOST_GATEWAY || !callerHostLoopback.getStore()) return {};
  if (!proxyPromise) {
    proxyPromise = startHostProxy(HOST_GATEWAY).catch((err) => {
      proxyPromise = null;
      throw err;
    });
  }
  const { url } = await proxyPromise;
  return { proxy: { server: url } };
}

export async function closeHostProxy() {
  if (!proxyPromise) return;
  const p = proxyPromise;
  proxyPromise = null;
  const { server } = await p.catch(() => ({}));
  server?.closeAllConnections();
  server?.close();
}
