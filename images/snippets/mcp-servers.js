'use strict';

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;

function readServers(raw = process.env.OCTOPUS_MCP_SERVERS) {
  if (!raw || !raw.trim()) return [];
  let list;
  try {
    list = JSON.parse(raw);
  } catch (err) {
    console.error(`mcp-servers.js: OCTOPUS_MCP_SERVERS is not JSON: ${err.message}`);
    return [];
  }
  if (!Array.isArray(list)) {
    console.error('mcp-servers.js: OCTOPUS_MCP_SERVERS is not a JSON array');
    return [];
  }
  const seen = new Set();
  const out = [];
  for (const e of list) {
    const ok =
      e &&
      typeof e.name === 'string' &&
      NAME_RE.test(e.name) &&
      typeof e.url === 'string' &&
      /^https?:\/\//.test(e.url) &&
      (e.transport === 'http' || e.transport === 'sse');
    if (!ok || seen.has(e.name)) {
      console.error(`mcp-servers.js: skipping entry ${JSON.stringify(e)}`);
      continue;
    }
    seen.add(e.name);
    out.push({ name: e.name, url: e.url, transport: e.transport, runKey: e.run_key === true });
  }
  return out;
}

function runKey() {
  return (process.env.OCTOPUS_API_KEY || '').trim();
}

module.exports = { readServers, runKey };

if (require.main === module && process.argv[2] === 'names') {
  for (const s of readServers()) process.stdout.write(s.name + '\n');
}
