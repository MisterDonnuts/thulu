// npm treats a failed download of an optional native dependency (lightningcss, esbuild, rolldown)
// as success; this postinstall fails the install instead.

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export function accepts(field, value) {
  if (!field || field.length === 0) return true;
  const list = Array.isArray(field) ? field : [field];
  if (list.includes(`!${value}`)) return false;
  const allowed = list.filter((v) => !v.startsWith("!"));
  return allowed.length === 0 || allowed.includes(value);
}

export function currentLibc() {
  if (process.platform !== "linux") return undefined;
  return process.report.getReport().header.glibcVersionRuntime ? "glibc" : "musl";
}

function parentOf(key) {
  const i = key.lastIndexOf("/node_modules/");
  return i === -1 ? "" : key.slice(0, i);
}

/**
 * Optional packages the lockfile says this platform gets but are not on disk.
 * @param {{ packages: Record<string, any> }} lock  parsed package-lock.json
 * @param {{ os: string, cpu: string, libc?: string, omitDev?: boolean }} platform
 * @param {(key: string) => boolean} installed  whether node_modules holds key
 */
export function missingNativeDeps(lock, platform, installed) {
  const missing = [];
  for (const [key, pkg] of Object.entries(lock.packages ?? {})) {
    if (!key.startsWith("node_modules/")) continue;
    if (!(pkg.optional || pkg.devOptional)) continue;
    if (!(pkg.os || pkg.cpu || pkg.libc)) continue;
    if (platform.omitDev && pkg.dev) continue;
    if (!accepts(pkg.os, platform.os) || !accepts(pkg.cpu, platform.cpu)) continue;
    if (platform.libc && !accepts(pkg.libc, platform.libc)) continue;
    const parent = parentOf(key);
    if (parent && !installed(parent)) continue;
    if (!installed(key))
      missing.push(key.slice(key.lastIndexOf("node_modules/") + "node_modules/".length));
  }
  return missing;
}

function main() {
  const root = process.cwd();
  const lock = JSON.parse(readFileSync(join(root, "package-lock.json"), "utf8"));
  const omit = `${process.env.npm_config_omit ?? ""}`;
  const missing = missingNativeDeps(
    lock,
    {
      os: process.platform,
      cpu: process.arch,
      libc: currentLibc(),
      omitDev: omit.includes("dev") || process.env.NODE_ENV === "production",
    },
    (key) => existsSync(join(root, key, "package.json")),
  );
  if (missing.length === 0) return;
  console.error(
    `npm install is incomplete: ${missing.length} native package(s) for ${process.platform}-${process.arch} ` +
      `were not installed:\n  ${missing.join("\n  ")}\n` +
      "npm skips an optional dependency whose download fails, which is almost always a network error. " +
      "Run the install again.",
  );
  process.exit(1);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
