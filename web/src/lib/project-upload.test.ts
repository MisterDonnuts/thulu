import { describe, expect, it } from "vitest";

import {
  buildTar,
  classifyPath,
  formatBytes,
  isSecretPath,
  planUpload,
  stripTopFolder,
  type PickedFile,
} from "./project-upload";

const file = (path: string, body = "x"): PickedFile => ({ path, file: new Blob([body]) });

describe("isSecretPath", () => {
  // Same table as TestIsSecretPath in internal/project/hosted_test.go; keep both in sync.
  const cases: Record<string, boolean> = {
    ".env": true,
    "app/.env.local": true,
    ".env.production": true,
    ".env.example": false,
    "config/.env.sample": false,
    "certs/server.key": true,
    id_rsa: true,
    "store.p12": true,
    "home/.aws/credentials": true,
    "docs/credentials": false,
    "src/env.ts": false,
    "README.md": false,
  };
  for (const [path, want] of Object.entries(cases)) {
    it(`${path} → ${want}`, () => expect(isSecretPath(path)).toBe(want));
  }
});

describe("classifyPath", () => {
  it("keeps project files and the history of the top .git", () => {
    expect(classifyPath("src/main.ts").kind).toBe("file");
    expect(classifyPath(".git/HEAD").kind).toBe("history");
    expect(classifyPath(".git/objects/ab/cdef").kind).toBe("history");
    expect(classifyPath(".git/refs/heads/main").kind).toBe("history");
  });

  it("drops the rest of .git, junk and nested repositories", () => {
    expect(classifyPath(".git/hooks/pre-commit").kind).toBe("drop");
    expect(classifyPath(".git/objects/info/alternates").kind).toBe("drop");
    expect(classifyPath(".DS_Store").kind).toBe("drop");
    expect(classifyPath("vendor/lib/.git/config")).toEqual({
      kind: "skip",
      folder: "vendor/lib/.git/",
    });
  });

  it("skips dependency folders wherever they are", () => {
    expect(classifyPath("node_modules/a/index.js")).toEqual({
      kind: "skip",
      folder: "node_modules/",
    });
    expect(classifyPath("web/node_modules/b.js")).toEqual({
      kind: "skip",
      folder: "web/node_modules/",
    });
    expect(classifyPath("api/.venv/bin/python")).toEqual({ kind: "skip", folder: "api/.venv/" });
  });

  it("leaves secrets out", () => {
    expect(classifyPath("config/.env").kind).toBe("secret");
  });
});

describe("stripTopFolder", () => {
  it("drops the folder every path shares", () => {
    expect(stripTopFolder(["app/a", "app/src/b"])).toBe("app/");
  });
  it("keeps paths that do not share one", () => {
    expect(stripTopFolder(["a", "src/b"])).toBe("");
    expect(stripTopFolder(["x/a", "y/b"])).toBe("");
    expect(stripTopFolder([".git/HEAD", ".git/config"])).toBe("");
  });
});

describe("planUpload", () => {
  it("sorts a dropped folder into what goes and what stays", () => {
    const plan = planUpload([
      file("my-app/README.md", "hello"),
      file("my-app/.env", "SECRET=1"),
      file("my-app/.env.example"),
      file("my-app/node_modules/.skipped"),
      file("my-app/node_modules/a/i.js"),
      file("my-app/.git/HEAD"),
      file("my-app/.git/hooks/post-checkout"),
    ]);
    expect(plan.files.map((f) => f.path)).toEqual(["README.md", ".env.example", ".git/HEAD"]);
    expect(plan.count).toBe(2);
    expect(plan.bytes).toBe(6);
    expect(plan.history).toBe(1);
    expect(plan.excluded).toEqual([".env"]);
    expect(plan.skipped).toEqual(["node_modules/"]);
  });
});

async function readTar(blob: Blob): Promise<Record<string, string>> {
  const buf = new Uint8Array(await blob.arrayBuffer());
  const dec = new TextDecoder();
  const out: Record<string, string> = {};
  let off = 0;
  let paxPath = "";
  while (off + 512 <= buf.length) {
    const h = buf.subarray(off, off + 512);
    if (h.every((b) => b === 0)) break;
    const name = dec.decode(h.subarray(0, 100)).replace(/\0.*$/s, "");
    const size = parseInt(dec.decode(h.subarray(124, 136)).replace(/\0.*$/s, ""), 8);
    const type = String.fromCharCode(h[156]);
    let sum = 0;
    for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 32 : h[i];
    expect(parseInt(dec.decode(h.subarray(148, 155)), 8)).toBe(sum);
    const body = dec.decode(buf.subarray(off + 512, off + 512 + size));
    off += 512 + Math.ceil(size / 512) * 512;
    if (type === "x") {
      const m = /^(\d+) path=(.*)\n$/s.exec(body);
      if (!m) throw new Error(`not a PAX path record: ${body}`);
      expect(Number(m[1])).toBe(new TextEncoder().encode(body).length);
      paxPath = m[2];
      continue;
    }
    out[paxPath || name] = body;
    paxPath = "";
  }
  return out;
}

describe("buildTar", () => {
  it("writes files a tar reader can read back", async () => {
    const long = "deep/".repeat(30) + "file.txt";
    const got = await readTar(
      buildTar([file("README.md", "hello\n"), file(long, "long"), file("docs/año.md", "ñ")]),
    );
    expect(got).toEqual({ "README.md": "hello\n", [long]: "long", "docs/año.md": "ñ" });
  });
});

describe("formatBytes", () => {
  it("reads like a person would say it", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(50 * 1024 * 1024)).toBe("50 MB");
  });
});
