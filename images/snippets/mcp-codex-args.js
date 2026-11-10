// http: the key goes through env_http_headers so it never lands in a flag. sse: codex has no SSE
// client, so mcp-remote --transport sse-only (http-first only 404s).
// Output is one token per line so the caller can mapfile -t it without word-splitting.
'use strict';
const path = require('path');
const { readServers, runKey } = require(path.join(__dirname, 'mcp-servers.js'));

const toml = (s) => '"' + String(s).replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';

const haveRemote = process.argv[2] !== '--no-mcp-remote';
const key = runKey();
const tokens = [];
for (const s of readServers()) {
  const k = `mcp_servers.${s.name}`; // mcp-servers.js NAME_RE guarantees a valid TOML bare key
  if (s.transport === 'http') {
    tokens.push('-c', `${k}.url=${toml(s.url)}`);
    if (s.runKey && key) tokens.push('-c', `${k}.env_http_headers={ "X-API-Key" = "OCTOPUS_API_KEY" }`);
  } else {
    if (!haveRemote) {
      console.error(`mcp-codex-args.js: skipping SSE server ${s.name}: mcp-remote is not installed`);
      continue;
    }
    const args = [s.url, '--transport', 'sse-only'];
    if (s.runKey && key) args.push('--header', `X-API-Key: ${key}`);
    tokens.push('-c', `${k}.command=${toml('mcp-remote')}`);
    tokens.push('-c', `${k}.args=[${args.map(toml).join(', ')}]`);
  }
  tokens.push('-c', `${k}.startup_timeout_sec=30`);
}
if (tokens.length) process.stdout.write(tokens.join('\n') + '\n');
