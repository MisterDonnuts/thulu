import { describe, expect, it } from "vitest";
import {
  applyStarter,
  authorHTML,
  authorText,
  checkFields,
  editorHTML,
  fieldWarnID,
  formToInput,
  historyRows,
  MAX_FILE_BYTES,
  MAX_FILES,
  parseCachePaths,
  parseFiles,
  parsePorts,
  parseAgentLabel,
  parseTools,
  paramRowHTML,
  prewarmHTML,
  prewarmReason,
  revisionHTML,
  setStageLabels,
  starterFor,
  statusHTML,
  toolsText,
  validFilePath,
  type InitForm,
  type InitRevision,
  type InitScript,
  type PrewarmStatus,
} from "./init-script";
import { historyRowsHTML } from "./ui";

const form = (over: Partial<InitForm> = {}): InitForm => ({
  script: "npm ci\nnpm run dev\n",
  params: [],
  ports: "",
  ready: "",
  timeout: "",
  workdir: "",
  setup: "",
  tools: "",
  cacheKey: "",
  cachePaths: "",
  files: [],
  note: "",
  ...over,
});

const script = (over: Partial<InitScript> = {}): InitScript => ({
  project_id: "p1",
  revision: 3,
  script: "make up",
  manifest: {
    params: [],
    ports: [3000],
    ready: { command: "curl -fs localhost:3000" },
    workdir: ".",
  },
  author_kind: "agent",
  author_label: "agent developer · task t1 · stage in_progress",
  created_at: "2026-10-01T12:00:00Z",
  ...over,
});

describe("parsePorts", () => {
  it("reads commas and spaces, drops duplicates", () => {
    expect(parsePorts("3000, 5173 3000")).toEqual({ ports: [3000, 5173], invalid: [] });
  });
  it("reports what is not a port", () => {
    expect(parsePorts("0 80 70000 abc 8.5").invalid).toEqual(["0", "70000", "abc", "8.5"]);
  });
});

describe("formToInput", () => {
  it("fills the defaults the server would", () => {
    const { input } = formToInput(form());
    expect(input?.manifest).toEqual({
      params: [],
      ports: [],
      ready: { command: "", timeout_seconds: 300 },
      workdir: ".",
    });
  });

  it("keeps params, drops blank rows, trims the note", () => {
    const { input, error } = formToInput(
      form({
        params: [
          { name: "DB_URL", default: "", description: " the db ", required: true },
          { name: "", default: "", description: "", required: false },
          { name: "PORT", default: "3000", description: "", required: false },
        ],
        ports: "3000",
        ready: " curl -fs localhost:3000 ",
        timeout: "120",
        workdir: "apps/web",
        note: "  pin node  ",
      }),
    );
    expect(error).toBeUndefined();
    expect(input?.manifest.params).toEqual([
      { name: "DB_URL", description: "the db", required: true },
      { name: "PORT", default: "3000" },
    ]);
    expect(input?.manifest.ready).toEqual({
      command: "curl -fs localhost:3000",
      timeout_seconds: 120,
    });
    expect(input?.manifest.workdir).toBe("apps/web");
    expect(input?.note).toBe("pin node");
  });

  it.each([
    ["an empty script", form({ script: "  \n" })],
    [
      "a lowercase param",
      form({ params: [{ name: "port", default: "", description: "", required: false }] }),
    ],
    [
      "a reserved param",
      form({ params: [{ name: "OCTOPUS_X", default: "", description: "", required: false }] }),
    ],
    [
      "a duplicate param",
      form({
        params: [
          { name: "A", default: "", description: "", required: false },
          { name: "A", default: "", description: "", required: false },
        ],
      }),
    ],
    ["a bad port", form({ ports: "3000, 99999" })],
    ["a zero timeout", form({ timeout: "0" })],
    ["a timeout past 15 minutes", form({ timeout: "901" })],
    ["an absolute workdir", form({ workdir: "/etc" })],
    ["a workdir leaving the repo", form({ workdir: "a/../.." })],
    ["a script past 64 KiB", form({ script: "x".repeat(64 * 1024 + 1) })],
    ["a setup past 64 KiB", form({ setup: "x".repeat(64 * 1024 + 1) })],
    ["a tool without a version", form({ tools: "go" })],
    ["a tool with a bad version", form({ tools: "go=$(id)" })],
    ["a tool listed twice", form({ tools: "go=1.25, go=1.24" })],
    ["an absolute cache path", form({ cachePaths: "/root/.npm" })],
    ["a cache path leaving the repo", form({ cacheKey: "../go.sum" })],
    ["caching .git", form({ cachePaths: ".git/objects" })],
  ])("refuses %s", (_, f) => {
    const { input, error } = formToInput(f);
    expect(input).toBeUndefined();
    expect(error).toBeTruthy();
  });
});

describe("install phase", () => {
  it("sends setup, tools and cache only when filled in", () => {
    const { input, error } = formToInput(
      form({
        setup: "npm ci\n",
        tools: "node=22 go=1.25.1",
        cacheKey: "package-lock.json, web/package-lock.json",
        cachePaths: "node_modules\nweb/node_modules",
      }),
    );
    expect(error).toBeUndefined();
    expect(input?.manifest.setup).toBe("npm ci\n");
    expect(input?.manifest.tools).toEqual({ node: "22", go: "1.25.1" });
    expect(input?.manifest.cache).toEqual({
      key: ["package-lock.json", "web/package-lock.json"],
      paths: ["node_modules", "web/node_modules"],
    });
    const bare = formToInput(form()).input?.manifest;
    expect(bare && "setup" in bare).toBe(false);
    expect(bare && "cache" in bare).toBe(false);
  });
  it("round-trips tools through the field text", () => {
    expect(toolsText({ node: "22", go: "1.25.1" })).toBe("go=1.25.1, node=22");
    expect(parseTools(toolsText({ "npm:typescript": "5.6.2" })).tools).toEqual({
      "npm:typescript": "5.6.2",
    });
    expect(parseCachePaths("a, a\n b ").paths).toEqual(["a", "b"]);
  });
  it("reads tools one per comma or line and reports a bad entry whole", () => {
    expect(parseTools("node = 22,\ngo=1.25.1 python=3.12")).toEqual({
      tools: { node: "22", go: "1.25.1", python: "3.12" },
      invalid: [],
    });
    expect(parseTools("node 22, go=1.25.1")).toEqual({
      tools: { go: "1.25.1" },
      invalid: ["node 22"],
    });
  });
  it("keeps the install fields in the editor and in a revision", () => {
    const sc = script({
      manifest: {
        params: [],
        ports: [],
        ready: {},
        workdir: ".",
        setup: "npm ci",
        tools: { node: "22" },
        cache: { key: ["package-lock.json"], paths: ["node_modules"] },
      },
    });
    const html = editorHTML(sc, false);
    expect(html).toContain('id="is-setup"');
    expect(html).toContain('value="node=22"');
    expect(html).toContain('value="package-lock.json"');
    expect(html).toContain('value="node_modules"');
    expect(html).toContain("data-prewarm");
    expect(revisionHTML(sc, 3, false)).toContain("node=22");
  });
});

describe("support files", () => {
  const file = (path: string, content = "x") => ({ path, content });
  const withFiles = (files: Record<string, string>) =>
    script({ manifest: { params: [], ports: [], ready: {}, workdir: ".", files } });

  it("takes relative, normalized paths", () => {
    for (const p of ["create-table.mjs", "sandbox/static.mjs", ".env.example", "a/b.c/d"])
      expect(validFilePath(p), p).toBe(true);
    for (const p of [
      "",
      "/etc/passwd",
      "../x",
      "a/../b",
      "a/..",
      "./a",
      "a/./b",
      ".",
      "a/",
      "a//b",
      "a\tb",
      "a\u0000b",
      "a\u007fb",
      "x".repeat(257),
    ])
      expect(validFilePath(p), JSON.stringify(p)).toBe(false);
    expect(validFilePath("x".repeat(256))).toBe(true);
    expect(validFilePath("ñ".repeat(129))).toBe(false);
  });

  it("sends the files keyed by path, trims the path, drops blank rows", () => {
    const { input, error } = formToInput(
      form({
        files: [
          file(" sandbox/static.mjs ", "export {}\n"),
          file("", ""),
          file("create-table.mjs", "\nCREATE TABLE t();\n"),
        ],
      }),
    );
    expect(error).toBeUndefined();
    expect(input?.manifest.files).toEqual({
      "sandbox/static.mjs": "export {}\n",
      "create-table.mjs": "\nCREATE TABLE t();\n",
    });
  });

  it("leaves files out of the manifest when there are none", () => {
    const m = formToInput(form({ files: [file("", "")] })).input?.manifest;
    expect(m && "files" in m).toBe(false);
    expect(parseFiles([])).toEqual({});
  });

  it("keeps an empty file", () => {
    expect(parseFiles([file("empty.txt", "")]).files).toEqual({ "empty.txt": "" });
  });

  it.each([
    ["a file without a path", [file("", "echo hi")]],
    ["an absolute path", [file("/tmp/a")]],
    ["a path leaving the directory", [file("../a")]],
    ["a non-normalized path", [file("a//b")]],
    ["a duplicate path", [file("a.mjs"), file(" a.mjs")]],
    ["a NUL byte", [file("a.bin", "a\u0000b")]],
    ["a file past 64 KiB", [file("big.txt", "x".repeat(MAX_FILE_BYTES + 1))]],
    ["more than 32 files", Array.from({ length: MAX_FILES + 1 }, (_, i) => file(`f${i}.txt`))],
    [
      "more than 256 KiB in all",
      Array.from({ length: 5 }, (_, i) => file(`f${i}.txt`, "x".repeat(MAX_FILE_BYTES))),
    ],
  ])("refuses %s", (_, files) => {
    const { input, error } = formToInput(form({ files }));
    expect(input).toBeUndefined();
    expect(error).toBeTruthy();
    expect(parseFiles(files).files).toBeUndefined();
  });

  it("takes 32 files and exactly 256 KiB", () => {
    const many = parseFiles(Array.from({ length: MAX_FILES }, (_, i) => file(`f${i}.txt`)));
    expect(Object.keys(many.files ?? {}).length).toBe(MAX_FILES);
    const full = Array.from({ length: 4 }, (_, i) => file(`f${i}.txt`, "x".repeat(MAX_FILE_BYTES)));
    expect(parseFiles(full).error).toBeUndefined();
  });

  it("draws one row per file, sorted and escaped, inside an open install block", () => {
    const html = editorHTML(
      withFiles({ "z.mjs": "</textarea><script>x</script>", "a/b.mjs": "1" }),
      false,
    );
    expect(html).toContain(
      '<details class="disclosure disclosure-fields disclosure-ruled is-install" open>',
    );
    expect(html.match(/data-file-row="/g)?.length).toBe(2);
    expect(html.indexOf('value="a/b.mjs"')).toBeLessThan(html.indexOf('value="z.mjs"'));
    expect(html).not.toContain("</textarea><script>");
    expect(html).toContain("data-file-del");
    expect(html).toContain("data-file-add");
    expect(html).not.toContain("data-files-empty");
    expect(html).toContain('aria-label="Content">\n1</textarea>');
  });

  it("read-only shows the files without the controls", () => {
    const html = editorHTML(withFiles({ "a.mjs": "1" }), true);
    expect(html).toContain('value="a.mjs"');
    expect(html).not.toContain("data-file-add");
    expect(html).not.toContain("data-file-del");
    expect(html).toMatch(/data-f="content"[^>]*disabled/);
    expect(editorHTML(script(), false)).toContain(
      'class="empty-state empty-state-inline" data-files-empty>',
    );
  });

  it("round-trips the editor's files through the form", () => {
    const files = { "create-table.mjs": "CREATE TABLE t();\n", "sandbox/static.mjs": "x" };
    const html = editorHTML(withFiles(files), false);
    const paths = [...html.matchAll(/data-f="path" class="mono" value="([^"]*)"/g)].map(
      (m) => m[1],
    );
    const contents = [...html.matchAll(/<textarea data-f="content"[^>]*>\n([^<]*)</g)].map(
      (m) => m[1],
    );
    const rows = paths.map((path, i) => ({ path, content: contents[i] }));
    expect(parseFiles(rows).files).toEqual(files);
  });

  it("lists the files in a revision, with their content", () => {
    const html = revisionHTML(withFiles({ "b.mjs": "<b>", "a.mjs": "1" }), 3, false);
    expect(html).toContain("2 support files: a.mjs, b.mjs");
    expect(html.match(/class="disclosure disclosure-code rev-file"/g)?.length).toBe(2);
    expect(html.indexOf("<span>a.mjs</span>")).toBeLessThan(html.indexOf("<span>b.mjs</span>"));
    expect(html).toContain("&lt;b&gt;");
    expect(revisionHTML(withFiles({ "a.mjs": "1" }), 3, false)).toContain("1 support file: a.mjs");
    expect(revisionHTML(script(), 3, false)).not.toContain("rev-file");
  });
});

describe("prewarm block", () => {
  const status = (over: Partial<PrewarmStatus> = {}): PrewarmStatus => ({
    available: true,
    eligible: true,
    running: false,
    prewarms: [],
    ...over,
  });
  const ok = {
    id: "a",
    setup_key: "0123456789abcdef", // gitleaks:allow
    snapshot_key: "fedcba9876543210",
    status: "ok" as const,
    started_at: "2026-10-01T00:00:00Z",
    finished_at: "2026-10-01T00:05:00Z",
    size_bytes: 3 * 1024 * 1024,
  };
  it("shows the last snapshot and the button for an admin", () => {
    const html = prewarmHTML(status({ prewarms: [ok] }), true);
    expect(html).toContain("fedcba987654");
    expect(html).not.toContain("fedcba9876543210");
    expect(html).toContain("data-prewarm-run");
    expect(prewarmHTML(status({ prewarms: [ok] }), false)).not.toContain("data-prewarm-run");
  });
  it("shows a failure after the last snapshot, escaped", () => {
    const failed = {
      ...ok,
      id: "b",
      status: "failed" as const,
      error: "<b>npm ERR</b>",
      started_at: "2026-10-02T00:00:00Z",
    };
    const html = prewarmHTML(status({ prewarms: [failed, ok] }), true);
    expect(html).toContain("&lt;b&gt;npm ERR");
    expect(html).toContain("code-block-danger");
    expect(html).toContain('<details class="disclosure disclosure-code"');
    expect(html).toContain("is-prewarm-reason");
  });
  it("disables the button while one runs, and has no button when not eligible", () => {
    expect(prewarmHTML(status({ running: true }), true)).toContain("disabled");
    expect(prewarmHTML(status({ eligible: false }), true)).not.toContain("data-prewarm-run");
    expect(prewarmHTML(status({ available: false }), true)).not.toContain("data-prewarm-run");
  });
});

describe("authorship", () => {
  it("words the agent run's signature, or a fallback per kind", () => {
    expect(authorText({ author_kind: "agent", author_label: "agent qa · task t · stage s" })).toBe(
      "qa in s · task #t",
    );
    expect(authorText({ author_kind: "user", author_label: "ana@example.com" })).toBe(
      "ana@example.com",
    );
    expect(authorText({ author_kind: "agent" })).not.toBe(authorText({ author_kind: "user" }));
  });
  it("takes the signature apart, and nothing else", () => {
    expect(parseAgentLabel("agent dev ops · task 578dfc0011 · stage in_progress")).toEqual({
      agent: "dev ops",
      task: "578dfc0011",
      stage: "in_progress",
    });
    expect(parseAgentLabel("ana@example.com")).toBeNull();
    expect(parseAgentLabel(undefined)).toBeNull();
  });
  it("names the stage by its label and links the task", () => {
    setStageLabels((id) => (id === "in_progress" ? "Development" : id));
    try {
      const html = authorHTML({
        author_kind: "agent",
        author_label: "agent <x> · task 578dfc0011 · stage in_progress",
      });
      expect(html).toContain("in Development");
      expect(html).toContain('href="/tasks/#task-578dfc0011"');
      expect(html).toContain("task #578dfc");
      expect(html).toContain("&lt;x&gt;");
      expect(authorHTML({ author_kind: "user", author_label: "<b>" })).toBe("&lt;b&gt;");
    } finally {
      setStageLabels((id) => id);
    }
  });
  it("badges agent revisions only", () => {
    expect(statusHTML(script())).toContain('class="badge badge-accent badge-pill badge-edge"');
    expect(
      statusHTML(script({ author_kind: "user", author_label: "ana@example.com" })),
    ).not.toContain("badge");
  });
  it("says when there is no script yet", () => {
    expect(statusHTML(script({ revision: 0 }))).not.toContain("Revision");
  });
});

describe("editorHTML", () => {
  it("escapes the script and the manifest", () => {
    const html = editorHTML(
      script({
        script: "echo '</textarea><script>x</script>'",
        manifest: {
          params: [{ name: "A", default: '"><img src=x>' }],
          ports: [],
          ready: {},
          workdir: ".",
        },
      }),
      false,
    );
    expect(html).not.toContain("</textarea><script>");
    expect(html).not.toContain('"><img');
    expect(html).toContain("data-param-del");
  });
  it("starts an empty project from the template", () => {
    expect(editorHTML(script({ revision: 0, script: "" }), false)).toContain("#!/usr/bin/env bash");
  });
  it("read-only drops every control that writes", () => {
    const html = editorHTML(
      script({ manifest: { params: [{ name: "A" }], ports: [], ready: {}, workdir: "." } }),
      true,
    );
    expect(html).not.toContain("data-param-add");
    expect(html).not.toContain("data-param-del");
    expect(html).not.toContain('id="is-note"');
    expect(html).toContain("disabled");
  });
  it("writes each control in a Field whose label names it", () => {
    const html = editorHTML(script(), false);
    for (const id of ["is-script", "is-ports", "is-ready", "is-timeout", "is-workdir", "is-note"])
      expect(html).toContain(`<label class="field-label" for="${id}">`);
    expect(html).toContain('<span class="field-optional">');
  });
  it("gives each checked field a hidden error line and a hidden warning line", () => {
    const html = editorHTML(script(), false);
    for (const id of [
      "is-ports",
      "is-timeout",
      "is-workdir",
      "is-tools",
      "is-cache-key",
      "is-cache-paths",
    ] as const) {
      expect(html).toContain(`<p class="field-error" id="${id}-error" hidden></p>`);
      expect(html).toContain(`<p class="field-warn" id="${fieldWarnID(id)}" hidden></p>`);
    }
  });
});

describe("paramRowHTML", () => {
  it("writes Required as a check row the form reads by data-p", () => {
    const html = paramRowHTML({ name: "A", required: true }, 0, false);
    expect(html).toMatch(/<label class="check[^"]*\bis-req"/);
    const input = html.match(/<input[^>]*data-p="required"[^>]*>/)?.[0] ?? "";
    expect(input).toContain('type="checkbox"');
    expect(input).toMatch(/\schecked\b/);
    expect(input).not.toMatch(/\sdisabled\b/);
  });
  it("turns the check off in read-only", () => {
    const html = paramRowHTML({ name: "A" }, 0, true);
    const input = html.match(/<input[^>]*data-p="required"[^>]*>/)?.[0] ?? "";
    expect(input).toMatch(/\sdisabled\b/);
    expect(input).not.toMatch(/\schecked\b/);
    expect(html).not.toContain("data-param-del");
  });
});

describe("history", () => {
  it("has no row when there is no revision: the list's own empty row says so", () => {
    expect(historyRows([])).toEqual([]);
  });

  it("lists revisions with their author, note and origin", () => {
    const html = historyRowsHTML(
      historyRows([
        {
          revision: 2,
          author_kind: "user",
          author_label: "ana",
          restored_from: 1,
          size: 7,
          created_at: "2026-10-02T00:00:00Z",
        },
        {
          revision: 1,
          author_kind: "agent",
          author_label: "agent dev · task t",
          note: "<b>pin</b>",
          size: 7,
          created_at: "2026-10-01T00:00:00Z",
        },
      ]),
    );
    expect(html.match(/data-rev="/g)?.length).toBe(2);
    expect(html).toContain("restored from revision 1");
    expect(html).toContain("agent dev · task t");
    expect(html).toContain("&lt;b&gt;pin");
    expect(html.indexOf('data-rev="0"')).toBeLessThan(html.indexOf('data-rev="1"'));
  });
  it("draws each revision as a button row with its lines under the title", () => {
    const one: InitRevision = {
      revision: 4,
      author_kind: "user",
      author_label: "ana",
      size: 7,
      created_at: "2026-10-02T00:00:00Z",
    };
    const html = historyRowsHTML(historyRows([one]));
    expect(html).toMatch(/^<li><button class="row row-sm" type="button" data-rev="0">/);
    expect(html).toContain('<span class="row-title">Revision 4</span>');
    expect(historyRows([one])[0].metaHTML).not.toContain("<br>");
    const full = historyRows([{ ...one, note: "pin", restored_from: 2 }])[0].metaHTML ?? "";
    expect(full.split("<br>")).toHaveLength(3);
  });
  it("offers restore only for an older revision", () => {
    expect(revisionHTML(script({ revision: 1 }), 3, true)).toContain("data-rev-restore");
    expect(revisionHTML(script({ revision: 3 }), 3, true)).not.toContain("data-rev-restore");
    expect(revisionHTML(script({ revision: 1 }), 3, false)).not.toContain("data-rev-restore");
  });
  it("draws restore as a primary button", () => {
    expect(revisionHTML(script({ revision: 1 }), 3, true)).toContain(
      '<button class="btn btn-primary btn-sm" type="button" data-rev-restore>',
    );
  });
});

describe("prewarmReason", () => {
  it("reads both languages of the daemon's errors", () => {
    const image = prewarmReason(
      "image octopus-sandbox:local is not present on the Docker host: build or load it there",
    );
    expect(image).toBe(
      prewarmReason("la imagen octopus-sandbox:local no está en el host de Docker: construila"),
    );
    expect(image).toContain("sandbox image");
    expect(prewarmReason("the setup script failed on the default branch:\nnpm ERR!")).toContain(
      "setup script",
    );
    expect(prewarmReason("el precalentado tardó más de 20 minutos y se detuvo")).toContain(
      "too long",
    );
  });
  it("falls back for an error it does not know", () => {
    expect(prewarmReason("something odd")).toContain("detail");
  });
});

describe("checkFields", () => {
  it("puts each problem under its field", () => {
    const { errors } = checkFields(
      form({
        ports: "3000, 99999",
        timeout: "0",
        workdir: "../x",
        tools: "go",
        cacheKey: "/etc/passwd",
        cachePaths: ".git/x",
      }),
    );
    expect(Object.keys(errors).sort()).toEqual([
      "is-cache-key",
      "is-cache-paths",
      "is-ports",
      "is-timeout",
      "is-tools",
      "is-workdir",
    ]);
    expect(errors["is-ports"]).toContain("99999");
  });
  it("agrees with save on a good form", () => {
    const f = form({
      ports: "3000",
      timeout: "60",
      tools: "node=22",
      cacheKey: "package-lock.json",
    });
    expect(checkFields(f).errors).toEqual({});
    expect(formToInput(f).input).toBeDefined();
  });
  it("warns about a lock file the repo does not have", () => {
    const f = form({ cacheKey: "package-lock.json, web/package-lock.json" });
    expect(checkFields(f, ["package.json", "yarn.lock"]).warnings["is-cache-key"]).toContain(
      "package-lock.json",
    );
    expect(checkFields(f, ["package-lock.json"]).warnings).toEqual({});
    expect(checkFields(f, null).warnings).toEqual({});
    expect(checkFields(form({ ...f, workdir: "web" }), []).warnings).toEqual({});
  });
});

describe("starterFor", () => {
  it("picks the lock file over the manifest", () => {
    const st = starterFor(["package.json", "pnpm-lock.yaml", "README.md"]);
    expect(st?.marker).toBe("pnpm-lock.yaml");
    expect(st?.setup).toContain("pnpm install --frozen-lockfile");
    expect(st?.cacheKey).toEqual(["pnpm-lock.yaml"]);
    expect(st?.cachePaths).toEqual(["node_modules"]);
  });
  it("keys Go on go.sum when there is one, and caches inside the repo", () => {
    expect(starterFor(["go.mod", "go.sum"])?.cacheKey).toEqual(["go.sum"]);
    const st = starterFor(["go.mod"]);
    expect(st?.cacheKey).toEqual(["go.mod"]);
    expect(st?.setup).toContain("GOMODCACHE");
    expect(st?.cachePaths).toEqual([".cache/go-mod"]);
  });
  it("knows npm and pip, and nothing else", () => {
    expect(starterFor(["package-lock.json"])?.setup).toBe("npm ci");
    expect(starterFor(["requirements.txt"])?.cachePaths).toEqual([".venv"]);
    expect(starterFor(["Cargo.toml"])).toBeNull();
  });
  it("fills a script the save accepts", () => {
    const st = starterFor(["package-lock.json"]);
    if (!st) throw new Error("no npm starter");
    const sc = applyStarter(script({ revision: 0, script: "" }), st);
    expect(sc.script.startsWith("#!/usr/bin/env bash")).toBe(true);
    expect(sc.manifest.cache).toEqual({ key: ["package-lock.json"], paths: ["node_modules"] });
    const html = editorHTML(sc, false);
    expect(html).toContain("npm run dev");
    expect(html).toContain("node_modules");
  });
});
