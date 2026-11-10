// Non-source locales land in dist/_locales/<locale>/, the path internal/api/static.go serves; their
// content-hashed assets merge into dist/_astro/.

import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { availableLocales, SOURCE_LOCALE } from "../i18n.vite.mjs";

const web = fileURLToPath(new URL("..", import.meta.url));
const dist = join(web, "dist");
const astro = join(web, "node_modules", ".bin", "astro");

function build(locale, outDir) {
  execFileSync(astro, ["build", "--outDir", outDir], {
    cwd: web,
    stdio: "inherit",
    env: { ...process.env, OCTOPUS_LOCALE: locale },
  });
}

build(SOURCE_LOCALE, dist);

for (const locale of availableLocales().filter((l) => l !== SOURCE_LOCALE)) {
  const tmp = join(web, `.dist-${locale}`);
  rmSync(tmp, { recursive: true, force: true });
  build(locale, tmp);
  const pages = join(dist, "_locales", locale);
  mkdirSync(pages, { recursive: true });
  for (const entry of readdirSync(tmp)) {
    const from = join(tmp, entry);
    if (entry === "_astro") cpSync(from, join(dist, "_astro"), { recursive: true });
    else cpSync(from, join(pages, entry), { recursive: true });
  }
  rmSync(tmp, { recursive: true, force: true });
}

if (!existsSync(join(dist, "index.html"))) throw new Error("the build left no dist/index.html");
