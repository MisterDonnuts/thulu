import { readdir, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'astro/config';
import { octopusMessages } from './i18n.vite.mjs';

// The daemon's CSP (internal/middleware/security.go) silently blocks inline <script> and on*=
// handlers; fail the build instead.
const inlineScript = /<script\b(?![^>]*\ssrc=)[^>]*>/gi;
const inlineHandler = /<[a-z][^>]*\son[a-z]+\s*=/gi;

async function htmlFiles(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await htmlFiles(p)));
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out;
}

const noInlineScript = {
  name: 'octopus:no-inline-script',
  hooks: {
    'astro:build:done': async ({ dir, logger }) => {
      const root = fileURLToPath(dir);
      const bad = [];
      for (const f of await htmlFiles(root)) {
        const html = await readFile(f, 'utf8');
        for (const m of html.matchAll(inlineScript)) bad.push(`${f}: ${m[0]}`);
        for (const m of html.matchAll(inlineHandler)) bad.push(`${f}: ${m[0]}`);
      }
      if (bad.length) {
        throw new Error(
          `inline script in the build; the dashboard CSP will block it:\n${bad.join('\n')}`,
        );
      }
      logger.info('no inline script in the build');
    },
  },
};

export default defineConfig({
  server: { port: 4321 },
  // 0 stops Astro inlining small scripts, which the CSP would block.
  vite: { build: { assetsInlineLimit: 0 }, plugins: [octopusMessages()] },
  integrations: [noInlineScript],
});
