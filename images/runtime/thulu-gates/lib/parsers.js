// Parsers for the reports tools leave on disk: coverage (LCOV, Go cover profiles, Cobertura,
// JaCoCo) and JUnit XML. Each coverage parser returns Map<path, Map<line, hits>> with paths as
// the tool wrote them; the coverage check maps them to repo paths.

export function decodeXml(s) {
  return String(s ?? "")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, n) => String.fromCodePoint(Number(n)))
    .replace(/&amp;/g, "&");
}

function attrs(tag) {
  const out = {};
  for (const m of tag.matchAll(/([A-Za-z_:][\w:.-]*)\s*=\s*("([^"]*)"|'([^']*)')/g)) out[m[1]] = decodeXml(m[3] ?? m[4]);
  return out;
}

function addHit(map, file, line, hits) {
  let lines = map.get(file);
  if (!lines) map.set(file, (lines = new Map()));
  lines.set(line, Math.max(lines.get(line) || 0, hits));
}

export function parseLcov(text) {
  const map = new Map();
  let file = null;
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line.startsWith("SF:")) {
      file = line.slice(3);
      if (!map.has(file)) map.set(file, new Map());
    } else if (line.startsWith("DA:") && file) {
      const [n, hits] = line.slice(3).split(",");
      addHit(map, file, Number(n), Number(hits));
    } else if (line === "end_of_record") file = null;
  }
  return map;
}

// parseGoCover reads `go test -coverprofile` output. A block spans lines start..end; every line
// in it takes the block's count. Files are import paths (module/pkg/file.go).
export function parseGoCover(text) {
  const map = new Map();
  for (const raw of text.split("\n")) {
    const m = /^(.+):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$/.exec(raw.trim());
    if (!m) continue;
    const [, file, start, end, , count] = m;
    for (let n = Number(start); n <= Number(end); n++) addHit(map, file, n, Number(count));
  }
  return map;
}

// parseCobertura reads Cobertura XML (coverage.py, gocover-cobertura, cargo llvm-cov, Jest).
// Class filenames are relative to one of the <source> roots, returned alongside.
export function parseCobertura(text) {
  const map = new Map();
  const sources = [...text.matchAll(/<source>([^<]*)<\/source>/g)].map((m) => decodeXml(m[1]).trim());
  const classRe = /<class\b([^>]*)>([\s\S]*?)<\/class>/g;
  for (const m of text.matchAll(classRe)) {
    const file = attrs(m[1]).filename;
    if (!file) continue;
    if (!map.has(file)) map.set(file, new Map());
    for (const l of m[2].matchAll(/<line\b([^>]*)\/?>/g)) {
      const a = attrs(l[1]);
      if (a.number) addHit(map, file, Number(a.number), Number(a.hits || 0));
    }
  }
  return { map, sources };
}

// parseJacoco reads JaCoCo XML: package name (a/b/c) plus sourcefile name; a line is covered when
// it has covered instructions (ci > 0).
export function parseJacoco(text) {
  const map = new Map();
  for (const pkg of text.matchAll(/<package\b([^>]*)>([\s\S]*?)<\/package>/g)) {
    const pname = attrs(pkg[1]).name || "";
    for (const sf of pkg[2].matchAll(/<sourcefile\b([^>]*)>([\s\S]*?)<\/sourcefile>/g)) {
      const file = (pname ? pname + "/" : "") + attrs(sf[1]).name;
      if (!map.has(file)) map.set(file, new Map());
      for (const l of sf[2].matchAll(/<line\b([^>]*)\/>/g)) {
        const a = attrs(l[1]);
        if (a.nr) addHit(map, file, Number(a.nr), Number(a.ci || 0));
      }
    }
  }
  return map;
}

export function coverageFormat(path, text) {
  if (/\.info$/.test(path) || /^(TN:|SF:)/m.test(text.slice(0, 2000))) return "lcov";
  if (/^mode: (set|count|atomic)/.test(text)) return "gocover";
  if (/<report\b/.test(text) && /<sourcefile\b/.test(text)) return "jacoco";
  if (/<coverage\b/.test(text)) return "cobertura";
  return null;
}

// parseJUnit returns failed and errored test cases from JUnit XML (pytest, surefire, gradle).
export function parseJUnit(text) {
  const out = [];
  for (const m of text.matchAll(/<testcase\b([^>]*?)(\/>|>([\s\S]*?)<\/testcase>)/g)) {
    const body = m[3] || "";
    const fail = /<(failure|error)\b([^>]*)>?/.exec(body);
    if (!fail) continue;
    const a = attrs(m[1]);
    const fa = attrs(fail[2] || "");
    const at = a.line ? null : failurePlace(decodeXml(body), a.classname, a.name);
    out.push({
      name: [a.classname, a.name].filter(Boolean).join("."),
      classname: a.classname || "",
      file: a.file || at?.file || "",
      line: Number(a.line || at?.line || 0),
      message: fa.message || fail[1],
    });
  }
  return out;
}

// failurePlace reads where a test failed from the failure text, for reports that leave out the
// file and line attributes: pytest's xunit2 ends with "tests/test_x.py:15: AssertionError", a
// Surefire stack trace has "at com.example.FooTest.bar(FooTest.java:10)" (line only; the file
// comes from the classname).
function failurePlace(text, classname, name) {
  const py = [...text.matchAll(/^([^\s:]+\.py):(\d+): \w/gm)].pop();
  if (py) return { file: py[1], line: Number(py[2]) };
  if (!classname || !name) return null;
  const esc = (x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const jv = new RegExp(`\\bat ${esc(classname)}\\.${esc(name)}\\([^:)]+:(\\d+)\\)`).exec(text);
  return jv ? { file: "", line: Number(jv[1]) } : null;
}

// parseJestJson reads the --json report Jest and Vitest share.
export function parseJestJson(doc, relPath) {
  const out = [];
  for (const suite of doc?.testResults || []) {
    const file = relPath(suite.name || suite.testFilePath || "");
    let failed = false;
    for (const a of suite.assertionResults || []) {
      if (a.status !== "failed") continue;
      failed = true;
      out.push({ name: a.fullName || a.title, file, line: a.location?.line || 0, message: (a.failureMessages || [])[0] || "" });
    }
    if (!failed && suite.status === "failed") out.push({ name: file, file, line: 0, message: suite.message || "suite failed" });
  }
  return out;
}

// parseGoTestJson reads `go test -json`: failed tests, plus packages that failed without a
// failing test (build errors).
export function parseGoTestJson(text) {
  const failedTests = [];
  const failedPkgs = new Set();
  const pkgsWithTestFail = new Set();
  const output = new Map();
  for (const raw of text.split("\n")) {
    if (!raw.startsWith("{")) continue;
    let ev;
    try {
      ev = JSON.parse(raw);
    } catch {
      continue;
    }
    if (ev.Action === "output" && ev.Test) {
      const k = `${ev.Package}\0${ev.Test}`;
      output.set(k, (output.get(k) || "") + ev.Output);
    }
    if (ev.Action === "fail") {
      if (ev.Test) {
        failedTests.push({ pkg: ev.Package, test: ev.Test, output: output.get(`${ev.Package}\0${ev.Test}`) || "" });
        pkgsWithTestFail.add(ev.Package);
      } else if (ev.Package) failedPkgs.add(ev.Package);
    }
  }
  const buildFailures = [...failedPkgs].filter((p) => !pkgsWithTestFail.has(p));
  return { failedTests, buildFailures };
}
