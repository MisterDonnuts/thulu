import { describe, expect, it } from "vitest";

import { accepts, missingNativeDeps } from "./verify-native-deps.mjs";

const lock = {
  packages: {
    "": { name: "web" },
    "node_modules/lightningcss": { version: "1.33.0" },
    "node_modules/lightningcss-linux-arm64-musl": { optional: true, os: ["linux"], cpu: ["arm64"] },
    "node_modules/lightningcss-linux-arm64-gnu": { optional: true, os: ["linux"], cpu: ["arm64"] },
    "node_modules/lightningcss-darwin-arm64": { optional: true, os: ["darwin"], cpu: ["arm64"] },
    "node_modules/@esbuild/linux-x64": { optional: true, os: ["linux"], cpu: ["x64"] },
    "node_modules/fsevents": { dev: true, optional: true, os: ["darwin"] },
    "node_modules/@img/sharp-linux-arm64": {
      optional: true,
      os: ["linux"],
      cpu: ["arm64"],
      libc: ["glibc"],
    },
    "node_modules/@img/sharp-wasm32": { optional: true, cpu: ["wasm32"] },
    "node_modules/plain-optional": { optional: true },
    "node_modules/tool/node_modules/@esbuild/darwin-arm64": {
      optional: true,
      os: ["darwin"],
      cpu: ["arm64"],
    },
  },
};

const alpineArm = { os: "linux", cpu: "arm64", libc: "musl" };
const macArm = { os: "darwin", cpu: "arm64" };

describe("accepts", () => {
  it("follows npm's os/cpu rules", () => {
    expect(accepts(undefined, "linux")).toBe(true);
    expect(accepts(["linux"], "linux")).toBe(true);
    expect(accepts(["linux"], "darwin")).toBe(false);
    expect(accepts(["!win32"], "linux")).toBe(true);
    expect(accepts(["!win32"], "win32")).toBe(false);
  });
});

describe("missingNativeDeps", () => {
  it("passes when every binary for the platform is on disk", () => {
    const onDisk = new Set([
      "node_modules/lightningcss-linux-arm64-musl",
      "node_modules/lightningcss-linux-arm64-gnu",
    ]);
    expect(missingNativeDeps(lock, alpineArm, (k) => onDisk.has(k))).toEqual([]);
  });

  it("names the binary a failed download left out", () => {
    const onDisk = new Set(["node_modules/lightningcss-linux-arm64-gnu"]);
    expect(missingNativeDeps(lock, alpineArm, (k) => onDisk.has(k))).toEqual([
      "lightningcss-linux-arm64-musl",
    ]);
  });

  it("ignores other platforms, other libcs and packages with no platform", () => {
    expect(missingNativeDeps(lock, { ...alpineArm, cpu: "x64" }, () => false)).toEqual([
      "@esbuild/linux-x64",
    ]);
    expect(
      missingNativeDeps(lock, { os: "linux", cpu: "arm64", libc: "glibc" }, () => false),
    ).toEqual([
      "lightningcss-linux-arm64-musl",
      "lightningcss-linux-arm64-gnu",
      "@img/sharp-linux-arm64",
    ]);
  });

  it("skips dev packages when dev is omitted, and children of a parent not installed", () => {
    expect(missingNativeDeps(lock, macArm, () => false)).toEqual([
      "lightningcss-darwin-arm64",
      "fsevents",
    ]);
    expect(missingNativeDeps(lock, { ...macArm, omitDev: true }, () => false)).toEqual([
      "lightningcss-darwin-arm64",
    ]);
    const withTool = (k) => k === "node_modules/tool";
    expect(missingNativeDeps(lock, { ...macArm, omitDev: true }, withTool)).toEqual([
      "lightningcss-darwin-arm64",
      "@esbuild/darwin-arm64",
    ]);
  });
});
