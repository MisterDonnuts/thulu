// Credentials are referenced as {env:...}, never written into the config.
// Every MCP server is "remote": OpenCode tries streamable HTTP and falls back to SSE itself.
'use strict';
const path = require('path');
const { readServers } = require(path.join(__dirname, 'mcp-servers.js'));

const model = (process.argv[2] || '').trim();
const skillsDir = (process.argv[3] || '').trim();
if (!model) {
  console.error('opencode-config.js: missing model argument');
  process.exit(2);
}

const provider = (process.env.OCTOPUS_LLM_PROVIDER || 'openai-compatible').trim();
const base = (process.env.OCTOPUS_LLM_BASE_URL || '').trim().replace(/\/+$/, '');

// Anthropic's SDK wants the /v1 that Claude Code's ANTHROPIC_BASE_URL leaves out; the OpenAI bases
// already carry it.
const providers = {
  anthropic: { npm: '@ai-sdk/anthropic', baseURL: base ? base + '/v1' : '' },
  openai: { npm: '@ai-sdk/openai', baseURL: base },
  'openai-compatible': { npm: '@ai-sdk/openai-compatible', baseURL: base, name: 'OpenAI-compatible' },
};
const p = providers[provider];
if (!p) {
  console.error(`opencode-config.js: unknown provider ${JSON.stringify(provider)}`);
  process.exit(2);
}
if (provider === 'openai-compatible' && !p.baseURL) {
  console.error('opencode-config.js: an openai-compatible profile needs a base URL');
  process.exit(2);
}

const options = { apiKey: '{env:LLM_API_KEY}' };
if (p.baseURL) options.baseURL = p.baseURL;
const block = { npm: p.npm, options, models: { [model]: {} } };
if (p.name) block.name = p.name;

const config = {
  share: 'disabled',
  autoupdate: false,
  enabled_providers: [provider],
  provider: { [provider]: block },
  model: `${provider}/${model}`,
  small_model: `${provider}/${model}`,
  mcp: mcpServers(),
};
if (skillsDir) config.skills = { paths: [skillsDir] };

process.stdout.write(JSON.stringify(config));

function mcpServers() {
  const out = {};
  for (const s of readServers()) {
    const entry = { type: 'remote', url: s.url, enabled: true, oauth: false, timeout: 30000 };
    if (s.runKey) entry.headers = { 'X-API-Key': '{env:OCTOPUS_API_KEY}' };
    out[s.name] = entry;
  }
  return out;
}
