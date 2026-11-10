'use strict';
const fs = require('fs');
const path = require('path');
const { readServers, runKey } = require(path.join(__dirname, 'mcp-servers.js'));

const out = process.argv[2];
if (!out) {
  console.error('mcp-json.js: missing output path argument');
  process.exit(2);
}
const key = runKey();
const servers = {};
for (const s of readServers()) {
  const entry = { type: s.transport, url: s.url };
  if (s.runKey && key) entry.headers = { 'X-API-Key': key };
  servers[s.name] = entry;
}
fs.writeFileSync(out, JSON.stringify({ mcpServers: servers }, null, 2));
