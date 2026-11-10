#!/usr/bin/env node
// Cost policy, not a security boundary: anything the parser does not understand, and any hook
// error, allows the call so a bug never stalls a run.
// After MAX_FAILURES failed octopus-delegate calls (counter written by the CLI in
// stateDir/failures) everything is allowed.

"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const DEFAULT_THRESHOLD = 24576;
const MAX_FAILURES = 3;
const MAX_SCAN_BYTES = 64 * 1024 * 1024;
const GUESS_LINE_BYTES = 80;

// Read renders these itself and offset/limit does not apply; octopus-delegate refuses binaries, so
// a deny would leave no way to view them.
const SPECIAL_READ_EXT = new Set([
  ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".tif", ".tiff",
  ".pdf", ".ipynb",
]);

function threshold(env) {
  const raw = String(env.OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES || "").trim();
  return /^[0-9]+$/.test(raw) && Number(raw) > 0 ? Number(raw) : DEFAULT_THRESHOLD;
}

function stateDir(env) {
  return env.OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR || path.join(env.TMPDIR || os.tmpdir(), "octopus-token-optimizer");
}

function failures(env) {
  try {
    const n = parseInt(fs.readFileSync(path.join(stateDir(env), "failures"), "utf8").trim(), 10);
    return Number.isFinite(n) ? n : 0;
  } catch {
    return 0;
  }
}

function fmtBytes(n) {
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function shellQuote(s) {
  return /^[A-Za-z0-9_./@%+=:,-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
}

function regularSize(p) {
  try {
    const st = fs.statSync(p);
    return st.isFile() ? st.size : null;
  } catch {
    return null;
  }
}

const bufCache = new Map();
function loadFile(p) {
  if (bufCache.has(p)) return bufCache.get(p);
  let buf = null;
  const size = regularSize(p);
  if (size !== null && size <= MAX_SCAN_BYTES) {
    try {
      buf = fs.readFileSync(p);
    } catch {
      buf = null;
    }
  }
  bufCache.set(p, buf);
  return buf;
}

function countLines(buf) {
  let n = 0;
  for (let i = buf.indexOf(10); i !== -1; i = buf.indexOf(10, i + 1)) n++;
  if (buf.length > 0 && buf[buf.length - 1] !== 10) n++;
  return n;
}

function lineStart(buf, line) {
  let off = 0;
  for (let l = 1; l < line; l++) {
    const nl = buf.indexOf(10, off);
    if (nl === -1) return buf.length;
    off = nl + 1;
  }
  return off;
}

function rangeBytes(buf, from, to) {
  if (to < from) return 0;
  const a = lineStart(buf, Math.max(1, from));
  const b = to === Infinity ? buf.length : lineStart(buf, to + 1);
  return Math.max(0, b - a);
}

function tailLinesBytes(buf, n) {
  if (n <= 0) return 0;
  let end = buf.length;
  if (end > 0 && buf[end - 1] === 10) end--;
  let seen = 0;
  for (let i = end - 1; i >= 0; i--) {
    if (buf[i] === 10) {
      seen++;
      if (seen === n) return buf.length - (i + 1);
    }
  }
  return buf.length;
}

function lineEstimate(p, fn) {
  const buf = loadFile(p);
  if (buf) return fn(buf);
  const size = regularSize(p);
  if (size === null) return null;
  return { guess: true, size };
}

const REDIR_OPS = [">>", ">|", ">&", "&>>", "&>", "<<<", "<<-", "<<", "<>", "<&", ">", "<"];

function tokenize(src) {
  const toks = [];
  const heredocs = [];
  let i = 0;
  let word = null;

  const startWord = () => {
    if (!word) word = { w: "", dyn: false, glob: false, quoted: false };
  };
  const endWord = () => {
    if (word) toks.push(word);
    word = null;
  };

  const skipSubst = (j) => {
    if (src[j] === "`") {
      j++;
      while (j < src.length && src[j] !== "`") j += src[j] === "\\" ? 2 : 1;
      return j + 1;
    }
    const open = src[j + 1];
    const close = open === "(" ? ")" : "}";
    let depth = 0;
    j += 1;
    while (j < src.length) {
      const c = src[j];
      if (c === "\\") {
        j += 2;
        continue;
      }
      if (c === "'") {
        const k = src.indexOf("'", j + 1);
        j = k === -1 ? src.length : k + 1;
        continue;
      }
      if (c === open) depth++;
      else if (c === close) {
        depth--;
        if (depth === 0) return j + 1;
      }
      j++;
    }
    return j;
  };

  while (i < src.length) {
    const c = src[i];

    if (c === "\\") {
      if (src[i + 1] === "\n") {
        i += 2;
        continue;
      }
      startWord();
      word.w += src[i + 1] || "";
      i += 2;
      continue;
    }
    if (c === "'") {
      startWord();
      word.quoted = true;
      const k = src.indexOf("'", i + 1);
      const end = k === -1 ? src.length : k;
      word.w += src.slice(i + 1, end);
      i = end + 1;
      continue;
    }
    if (c === '"') {
      startWord();
      word.quoted = true;
      i++;
      while (i < src.length && src[i] !== '"') {
        if (src[i] === "\\" && '"\\$`\n'.includes(src[i + 1])) {
          if (src[i + 1] !== "\n") word.w += src[i + 1];
          i += 2;
          continue;
        }
        if (src[i] === "$" || src[i] === "`") {
          if (src[i] === "$" && !/[A-Za-z_{(0-9@*#?$!-]/.test(src[i + 1] || "")) {
            word.w += "$";
            i++;
            continue;
          }
          word.dyn = true;
          const j = src[i] === "`" || src[i + 1] === "(" || src[i + 1] === "{" ? skipSubst(i) : skipVar(src, i);
          word.w += src.slice(i, j);
          i = j;
          continue;
        }
        word.w += src[i];
        i++;
      }
      i++;
      continue;
    }
    if (c === "$" || c === "`") {
      startWord();
      if (c === "$" && src[i + 1] === "'") {
        word.dyn = true;
        const k = src.indexOf("'", i + 2);
        const end = k === -1 ? src.length : k + 1;
        word.w += src.slice(i, end);
        i = end;
        continue;
      }
      if (c === "$" && !/[A-Za-z_{(0-9@*#?$!-]/.test(src[i + 1] || "")) {
        word.w += "$";
        i++;
        continue;
      }
      word.dyn = true;
      const j = c === "`" || src[i + 1] === "(" || src[i + 1] === "{" ? skipSubst(i) : skipVar(src, i);
      word.w += src.slice(i, j);
      i = j;
      continue;
    }
    if (c === "#" && !word) {
      const k = src.indexOf("\n", i);
      i = k === -1 ? src.length : k;
      continue;
    }
    if (c === " " || c === "\t") {
      endWord();
      i++;
      continue;
    }
    if (c === "\n") {
      endWord();
      toks.push({ op: "\n" });
      i++;
      while (heredocs.length) {
        const { delim, strip } = heredocs.shift();
        for (;;) {
          if (i >= src.length) break;
          const k = src.indexOf("\n", i);
          const line = src.slice(i, k === -1 ? src.length : k);
          i = k === -1 ? src.length : k + 1;
          if ((strip ? line.replace(/^\t+/, "") : line) === delim) break;
        }
      }
      continue;
    }
    if (c === ">" || c === "<" || (c === "&" && src[i + 1] === ">")) {
      let fd = null;
      if (word && !word.quoted && /^[0-9]+$/.test(word.w)) {
        fd = word.w;
        word = null;
      } else {
        endWord();
      }
      const op = REDIR_OPS.find((o) => src.startsWith(o, i));
      i += op.length;
      toks.push({ op, fd, redir: true });
      if (op === "<<" || op === "<<-") {
        while (src[i] === " " || src[i] === "\t") i++;
        let d = "";
        while (i < src.length && !/[\s;&|<>()]/.test(src[i])) {
          if (src[i] === "'" || src[i] === '"') {
            const q = src[i];
            const k = src.indexOf(q, i + 1);
            const end = k === -1 ? src.length : k;
            d += src.slice(i + 1, end);
            i = end + 1;
            continue;
          }
          if (src[i] === "\\") {
            d += src[i + 1] || "";
            i += 2;
            continue;
          }
          d += src[i];
          i++;
        }
        heredocs.push({ delim: d, strip: op === "<<-" });
        toks.push({ w: d, dyn: false, glob: false, quoted: true });
      }
      continue;
    }
    if (c === "|" || c === "&" || c === ";" || c === "(" || c === ")") {
      endWord();
      const two = src.slice(i, i + 2);
      if (two === "||" || two === "&&" || two === "|&" || two === ";;") {
        toks.push({ op: two });
        i += 2;
      } else {
        toks.push({ op: c });
        i++;
      }
      continue;
    }
    startWord();
    if (c === "*" || c === "?" || c === "[") word.glob = true;
    word.w += c;
    i++;
  }
  endWord();
  return toks;
}

function skipVar(src, i) {
  let j = i + 1;
  if (/[0-9@*#?$!-]/.test(src[j] || "")) return j + 1;
  while (j < src.length && /[A-Za-z0-9_]/.test(src[j])) j++;
  return j;
}

function parse(toks) {
  const pipelines = [];
  let pipeline = { cmds: [] };
  let cmd = { words: [], redirs: [] };
  const endCmd = () => {
    if (cmd.words.length || cmd.redirs.length) pipeline.cmds.push(cmd);
    cmd = { words: [], redirs: [] };
  };
  const endPipeline = () => {
    endCmd();
    if (pipeline.cmds.length) pipelines.push(pipeline);
    pipeline = { cmds: [] };
  };
  for (let k = 0; k < toks.length; k++) {
    const t = toks[k];
    if (t.redir) {
      const target = toks[k + 1] && toks[k + 1].op === undefined ? toks[++k] : null;
      cmd.redirs.push({ op: t.op, fd: t.fd, target });
      continue;
    }
    if (t.op === "|" || t.op === "|&") {
      endCmd();
      continue;
    }
    if (t.op) {
      endPipeline();
      continue;
    }
    cmd.words.push(t);
  }
  endPipeline();
  return pipelines;
}

const KEYWORDS = new Set(["if", "then", "else", "elif", "do", "while", "until", "!", "{", "}", "fi", "done"]);
const WHOLE_FILE = new Set(["cat", "tac", "nl", "less", "more", "bat", "batcat", "pr", "view"]);
const PASSTHROUGH = new Set(["cat", "tac", "nl", "less", "more", "bat", "batcat", "sort", "tee", "pr", "fold", "expand", "unexpand", "column"]);

function stripPrefix(words) {
  let w = words.slice();
  for (;;) {
    if (!w.length) return w;
    const head = w[0];
    if (head.dyn) return w;
    const v = head.w;
    if (KEYWORDS.has(v) || v === "command" || v === "builtin" || v === "exec" || v === "nohup") {
      w = w.slice(1);
      continue;
    }
    if (/^[A-Za-z_][A-Za-z0-9_]*=/.test(v)) {
      w = w.slice(1);
      continue;
    }
    if (v === "env") {
      w = w.slice(1);
      while (w.length && !w[0].dyn && (/^[A-Za-z_][A-Za-z0-9_]*=/.test(w[0].w) || w[0].w.startsWith("-"))) {
        const flag = w[0].w;
        w = w.slice(1);
        if ((flag === "-u" || flag === "-C" || flag === "-S") && w.length) w = w.slice(1);
      }
      continue;
    }
    if (v === "sudo" || v === "nice" || v === "time" || v === "stdbuf" || v === "ionice") {
      w = w.slice(1);
      while (w.length && !w[0].dyn && w[0].w.startsWith("-")) {
        const flag = w[0].w;
        w = w.slice(1);
        if (["-u", "-g", "-n", "-c", "-p"].includes(flag) && w.length && !w[0].w.startsWith("-")) w = w.slice(1);
      }
      continue;
    }
    if (v === "timeout") {
      w = w.slice(1);
      while (w.length && w[0].w.startsWith("-")) {
        const flag = w[0].w;
        w = w.slice(1);
        if ((flag === "-s" || flag === "-k") && w.length) w = w.slice(1);
      }
      if (w.length) w = w.slice(1);
      continue;
    }
    return w;
  }
}

function globToRegex(pat) {
  let re = "^";
  for (let i = 0; i < pat.length; i++) {
    const c = pat[i];
    if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "[") {
      const k = pat.indexOf("]", i + 1);
      if (k === -1) re += "\\[";
      else {
        let cls = pat.slice(i + 1, k);
        if (cls.startsWith("!")) cls = "^" + cls.slice(1);
        re += "[" + cls.replace(/\\/g, "\\\\") + "]";
        i = k;
      }
    } else re += c.replace(/[.+^${}()|\\]/g, "\\$&");
  }
  return new RegExp(re + "$");
}

function resolvePaths(word, cwd, env) {
  if (word.dyn) return [];
  let v = word.w;
  if (!v || v === "-") return [];
  if (v === "~" || v.startsWith("~/")) v = path.join(env.HOME || os.homedir(), v.slice(1));
  if (!path.isAbsolute(v)) {
    if (!cwd) return [];
    v = path.join(cwd, v);
  }
  if (!word.glob) return [v];
  const dir = path.dirname(v);
  const base = path.basename(v);
  if (/[*?[]/.test(dir) || base.includes("**")) return [];
  let names;
  try {
    names = fs.readdirSync(dir);
  } catch {
    return [];
  }
  const re = globToRegex(base);
  const out = names
    .filter((n) => (base.startsWith(".") || !n.startsWith(".")) && re.test(n))
    .sort()
    .map((n) => path.join(dir, n));
  return out.length ? out : [v];
}

function operands(words) {
  const out = [];
  let ended = false;
  for (const w of words) {
    if (!ended && !w.dyn && w.w === "--") {
      ended = true;
      continue;
    }
    if (!ended && !w.dyn && w.w.startsWith("-") && w.w !== "-") continue;
    out.push(w);
  }
  return out;
}

function wholeFiles(paths) {
  const files = [];
  for (const p of paths) {
    const size = regularSize(p);
    if (size !== null) files.push({ path: p, bytes: size });
  }
  return files;
}

function flowOf(files) {
  if (!files.length) return null;
  return { bytes: files.reduce((s, f) => s + f.bytes, 0), files, lines: null };
}

function flowLines(flow) {
  if (flow.lines !== null) return flow.lines;
  let n = 0;
  for (const f of flow.files) {
    const buf = loadFile(f.path);
    n += buf ? countLines(buf) : Math.ceil(f.bytes / GUESS_LINE_BYTES);
  }
  flow.lines = n;
  return n;
}

function parseHeadTail(args, isHead) {
  const opt = { lines: 10, bytes: null, from: null, allBut: false, follow: false, files: [] };
  const num = (s) => {
    const m = /^([+-]?)([0-9]+)([kKmMbB]?)$/.exec(s || "");
    if (!m) return null;
    const unit = { k: 1024, K: 1024, m: 1048576, M: 1048576, b: 512, B: 512 }[m[3]] || 1;
    return { sign: m[1], n: Number(m[2]) * unit };
  };
  const set = (kind, v) => {
    if (!v) return false;
    if (kind === "lines") {
      opt.bytes = null;
      opt.lines = v.n;
      opt.from = v.sign === "+" && !isHead ? v.n : null;
      opt.allBut = v.sign === "-" && isHead;
    } else {
      opt.bytes = v.n;
      opt.from = v.sign === "+" && !isHead ? v.n : null;
    }
    return true;
  };
  let ended = false;
  for (let k = 0; k < args.length; k++) {
    const a = args[k];
    const v = a.w;
    if (ended || a.dyn || !v.startsWith("-") || v === "-") {
      if (a.dyn && !ended && v.startsWith("-")) return null;
      opt.files.push(a);
      continue;
    }
    let m;
    if (v === "--") ended = true;
    else if (v === "-f" || v === "-F" || v.startsWith("--follow") || v === "--retry") opt.follow = true;
    else if (v === "-n" || v === "--lines" || v === "-c" || v === "--bytes") {
      const next = args[++k];
      if (!next || next.dyn || !set(v === "-n" || v === "--lines" ? "lines" : "bytes", num(next.w))) return null;
    } else if ((m = /^--(lines|bytes)=(.*)$/.exec(v))) {
      if (!set(m[1], num(m[2]))) return null;
    } else if ((m = /^-([nc])(.+)$/.exec(v))) {
      if (!set(m[1] === "n" ? "lines" : "bytes", num(m[2]))) return null;
    } else if ((m = /^-([0-9]+)$/.exec(v))) set("lines", { sign: "", n: Number(m[1]) });
    else if (!(/^-[qvz]+$/.test(v) || v === "--quiet" || v === "--silent" || v === "--verbose")) return null;
  }
  return opt;
}

function headTailBytes(isHead, opt, buf) {
  if (opt.bytes !== null) {
    if (opt.from !== null) return Math.max(0, buf.length - opt.from + 1);
    return Math.min(opt.bytes, buf.length);
  }
  if (isHead) {
    if (opt.allBut) return Math.max(0, buf.length - tailLinesBytes(buf, opt.lines));
    return rangeBytes(buf, 1, opt.lines);
  }
  if (opt.from !== null) return rangeBytes(buf, opt.from, Infinity);
  return tailLinesBytes(buf, opt.lines);
}

function headTail(isHead, args, cwd, env, flowIn) {
  const opt = parseHeadTail(args, isHead);
  if (!opt || opt.follow) return { flow: null };
  if (!opt.files.length) {
    if (!flowIn) return { flow: null };
    if (opt.bytes !== null) return { flow: { ...flowIn, bytes: Math.min(opt.bytes, flowIn.bytes), lines: null } };
    if (opt.from !== null || opt.allBut) return { flow: flowIn };
    const lines = flowLines(flowIn);
    if (lines <= opt.lines) return { flow: flowIn };
    return { flow: { ...flowIn, bytes: Math.ceil((flowIn.bytes / Math.max(1, lines)) * opt.lines), lines: opt.lines } };
  }
  const files = [];
  for (const p of opt.files.flatMap((w) => resolvePaths(w, cwd, env))) {
    const est = lineEstimate(p, (buf) => headTailBytes(isHead, opt, buf));
    if (est === null) continue;
    if (typeof est === "object") {
      const whole = opt.from !== null || opt.allBut;
      const guess = opt.bytes !== null && !whole ? opt.bytes : whole ? est.size : opt.lines * GUESS_LINE_BYTES;
      files.push({ path: p, bytes: Math.min(est.size, guess) });
      continue;
    }
    files.push({ path: p, bytes: est });
  }
  return { flow: flowOf(files) };
}

function sedPrintRanges(script) {
  const parts = script.split(/[;\n]/).map((s) => s.trim()).filter(Boolean);
  if (!parts.length) return null;
  const ranges = [];
  for (const p of parts) {
    let m;
    if (p === "p") ranges.push([1, Infinity]);
    else if ((m = /^(\d+|\$)\s*p$/.exec(p))) ranges.push(m[1] === "$" ? ["$", "$"] : [Number(m[1]), Number(m[1])]);
    else if ((m = /^(\d+)\s*,\s*(\d+|\$)\s*p$/.exec(p))) ranges.push([Number(m[1]), m[2] === "$" ? Infinity : Number(m[2])]);
    else if ((m = /^(\d+)\s*,\s*\+(\d+)\s*p$/.exec(p))) ranges.push([Number(m[1]), Number(m[1]) + Number(m[2])]);
    else return null;
  }
  return ranges;
}

function sedPrintsWhole(script) {
  const s = script.trim();
  if (s === "" || s === "p") return true;
  return /^s([^\w\s\\;])(?:\\.|(?!\1).)*\1(?:\\.|(?!\1).)*\1[gIip0-9]*$/.test(s);
}

function sed(args, cwd, env, flowIn) {
  let quiet = false;
  const scripts = [];
  const positional = [];
  let ended = false;
  for (let k = 0; k < args.length; k++) {
    const a = args[k];
    const v = a.w;
    if (ended || a.dyn || !v.startsWith("-") || v === "-") {
      positional.push(a);
      continue;
    }
    if (v === "--") ended = true;
    else if (v === "-n" || v === "--quiet" || v === "--silent") quiet = true;
    else if (v === "-e" || v === "--expression") {
      const next = args[++k];
      if (!next || next.dyn) return { flow: null };
      scripts.push(next.w);
    } else if (v.startsWith("--expression=")) scripts.push(v.slice("--expression=".length));
    else if (/^-[nEersuz]+$/.test(v)) quiet = quiet || v.includes("n");
    else if (!/^--(regexp-extended|posix|separate|unbuffered|null-data|sandbox|debug)$/.test(v)) {
      return { flow: null };
    }
  }
  if (!scripts.length && positional.length) {
    const first = positional.shift();
    if (first.dyn) return { flow: null };
    scripts.push(first.w);
  }
  const files = positional;
  if (!scripts.length) return { flow: null };

  let ranges = null;
  if (quiet) {
    ranges = [];
    for (const s of scripts) {
      const r = sedPrintRanges(s);
      if (!r) return { flow: null };
      ranges.push(...r);
    }
  } else if (!scripts.every(sedPrintsWhole)) {
    return { flow: null };
  }

  const measure = (buf) => {
    if (!ranges) return buf.length;
    let n = 0;
    const total = countLines(buf);
    for (const [a, b] of ranges) {
      if (a === "$") n += tailLinesBytes(buf, 1);
      else n += rangeBytes(buf, a, Math.min(b, total));
    }
    return n;
  };

  if (!files.length) {
    if (!flowIn) return { flow: null };
    if (!ranges) return { flow: flowIn };
    const lines = flowLines(flowIn);
    const avg = flowIn.bytes / Math.max(1, lines);
    const count = ranges.reduce((s, [a, b]) => s + (a === "$" ? 1 : Math.max(0, Math.min(b, lines) - a + 1)), 0);
    return { flow: { ...flowIn, bytes: Math.ceil(avg * count), lines: count } };
  }
  const out = [];
  for (const p of files.flatMap((w) => resolvePaths(w, cwd, env))) {
    const est = lineEstimate(p, measure);
    if (est === null) continue;
    if (typeof est === "object") {
      out.push({ path: p, bytes: ranges ? Math.min(est.size, GUESS_LINE_BYTES * ranges.reduce((s, [a, b]) => s + (a === "$" ? 1 : Math.min(b, 1e9) - a + 1), 0)) : est.size });
      continue;
    }
    out.push({ path: p, bytes: est });
  }
  return { flow: flowOf(out) };
}

function awkProgram(prog) {
  const p = prog.replace(/\s+/g, "").replace(/;+(?=})/g, "");
  if (["1", "{print}", "{print$0}", "//", "//{print}", "1{print}", "{print$0}"].includes(p)) return [1, Infinity];
  let m;
  const body = "(?:\\{print(?:\\$0)?\\})?";
  if ((m = new RegExp(`^NR>=(\\d+)&&NR<=(\\d+)${body}$`).exec(p))) return [Number(m[1]), Number(m[2])];
  if ((m = new RegExp(`^NR>(\\d+)&&NR<(\\d+)${body}$`).exec(p))) return [Number(m[1]) + 1, Number(m[2]) - 1];
  if ((m = new RegExp(`^NR==(\\d+),NR==(\\d+)${body}$`).exec(p))) return [Number(m[1]), Number(m[2])];
  if ((m = new RegExp(`^NR>=(\\d+)${body}$`).exec(p))) return [Number(m[1]), Infinity];
  return null;
}

function awk(args, cwd, env, flowIn) {
  let prog = null;
  const files = [];
  for (let k = 0; k < args.length; k++) {
    const a = args[k];
    const v = a.w;
    if (prog === null) {
      if (a.dyn) return { flow: null };
      if (v === "-f" || v.startsWith("--file")) return { flow: null };
      if (v === "-F" || v === "-v") {
        k++;
        continue;
      }
      if (/^-[Fv]./.test(v) || v === "--") continue;
      if (v.startsWith("-")) continue;
      prog = v;
      continue;
    }
    if (!a.dyn && /^[A-Za-z_][A-Za-z0-9_]*=/.test(v)) continue;
    files.push(a);
  }
  if (prog === null) return { flow: null };
  const range = awkProgram(prog);
  if (!range) return { flow: null };
  const [from, to] = range;
  if (!files.length) {
    if (!flowIn) return { flow: null };
    if (from === 1 && to === Infinity) return { flow: flowIn };
    const lines = flowLines(flowIn);
    const count = Math.max(0, Math.min(to, lines) - from + 1);
    return { flow: { ...flowIn, bytes: Math.ceil((flowIn.bytes / Math.max(1, lines)) * count), lines: count } };
  }
  const out = [];
  for (const p of files.flatMap((w) => resolvePaths(w, cwd, env))) {
    const est = lineEstimate(p, (buf) => rangeBytes(buf, from, to));
    if (est === null) continue;
    if (typeof est === "object") {
      out.push({ path: p, bytes: to === Infinity ? est.size : Math.min(est.size, (to - from + 1) * GUESS_LINE_BYTES) });
      continue;
    }
    out.push({ path: p, bytes: est });
  }
  return { flow: flowOf(out) };
}

function git(args, cwd) {
  let k = 0;
  let dir = cwd;
  while (k < args.length && !args[k].dyn && args[k].w.startsWith("-")) {
    const v = args[k].w;
    if (v === "-C" && args[k + 1] && !args[k + 1].dyn) {
      dir = dir ? path.resolve(dir, args[k + 1].w) : null;
      k += 2;
      continue;
    }
    if (v === "-c") {
      k += 2;
      continue;
    }
    k++;
  }
  const sub = args[k] && args[k].w;
  let objs = [];
  if (sub === "show") {
    const rest = args.slice(k + 1);
    if (rest.some((a) => a.dyn)) return { flow: null };
    objs = rest.filter((a) => !a.w.startsWith("-") && a.w.includes(":")).map((a) => a.w);
    if (!objs.length) return { flow: null };
  } else if (sub === "cat-file") {
    const rest = args.slice(k + 1).map((a) => a.w);
    if (args.slice(k + 1).some((a) => a.dyn)) return { flow: null };
    if (rest[0] === "-p" || rest[0] === "blob") objs = rest.slice(1, 2);
    else return { flow: null };
  } else {
    return { flow: null };
  }
  if (!dir) return { flow: null };
  const files = [];
  for (const obj of objs) {
    const r = spawnSync("git", ["-C", dir, "cat-file", "-s", obj], { encoding: "utf8", timeout: 3000 });
    if (r.status !== 0) continue;
    const n = parseInt(String(r.stdout).trim(), 10);
    if (Number.isFinite(n)) files.push({ path: obj, bytes: n, label: `${obj} (git)` });
  }
  return { flow: flowOf(files) };
}

function inlineScript(cmd, args, cwd, env) {
  let code = null;
  let printFlag = false;
  for (let k = 0; k < args.length; k++) {
    const v = args[k].w;
    if (cmd.startsWith("python") && v === "-c") {
      code = args[k + 1];
      break;
    }
    if (cmd === "node" && (v === "-e" || v === "--eval" || v === "-p" || v === "--print")) {
      printFlag = v === "-p" || v === "--print";
      code = args[k + 1];
      break;
    }
  }
  if (!code) return { flow: null };
  const src = code.w;
  const lits = [];
  let m;
  if (cmd.startsWith("python")) {
    if (!/\bprint\s*\(|sys\.stdout\.write\s*\(/.test(src)) return { flow: null };
    const reOpen = /open\(\s*(['"])([^'"]+)\1[^)]*\)\s*\.read\(\s*\)/g;
    while ((m = reOpen.exec(src))) lits.push(m[2]);
    const rePath = /Path\(\s*(['"])([^'"]+)\1\s*\)\s*\.read_(?:text|bytes)\(/g;
    while ((m = rePath.exec(src))) lits.push(m[2]);
  } else {
    if (!printFlag && !/console\.log\s*\(|process\.stdout\.write\s*\(/.test(src)) return { flow: null };
    const re = /readFileSync\(\s*(['"`])([^'"`]+)\1/g;
    while ((m = re.exec(src))) lits.push(m[2]);
  }
  const paths = lits.flatMap((l) => resolvePaths({ w: l, dyn: false, glob: false }, cwd, env));
  return { flow: flowOf(wholeFiles(paths)) };
}

function commandName(word) {
  return word && !word.dyn ? path.basename(word.w) : null;
}

function stage(cmd, cwd, env, flowIn, depth) {
  const words = stripPrefix(cmd.words);
  for (const r of cmd.redirs) {
    if ((r.op === "<" || r.op === "<>") && (r.fd === null || r.fd === "0") && r.target) {
      const paths = resolvePaths(r.target, cwd, env);
      flowIn = flowOf(wholeFiles(paths.slice(0, 1)));
    }
    if (r.op === "<<" || r.op === "<<-" || r.op === "<<<") flowIn = null;
  }
  const name = commandName(words[0]);
  const args = words.slice(1);
  if (!name) return { flow: null };

  if (name === "cd") {
    const target = args.find((a) => a.dyn || !a.w.startsWith("-"));
    if (!target) return { flow: null, cwd: env.HOME || os.homedir() };
    if (target.dyn || target.w === "-") return { flow: null, cwd: null };
    const t = target.w === "~" || target.w.startsWith("~/") ? path.join(env.HOME || os.homedir(), target.w.slice(1)) : target.w;
    return { flow: null, cwd: path.isAbsolute(t) ? t : cwd ? path.resolve(cwd, t) : null };
  }
  if (name === "pushd") return { flow: null, cwd: null };

  if (WHOLE_FILE.has(name)) {
    const ops = operands(args);
    if (!ops.length) return { flow: PASSTHROUGH.has(name) ? flowIn : null };
    return { flow: flowOf(wholeFiles(ops.flatMap((w) => resolvePaths(w, cwd, env)))) };
  }
  if (name === "head" || name === "tail") return headTail(name === "head", args, cwd, env, flowIn);
  if (name === "sed" || name === "gsed") return sed(args, cwd, env, flowIn);
  if (name === "awk" || name === "gawk" || name === "mawk" || name === "nawk") return awk(args, cwd, env, flowIn);
  if (name === "git") return git(args, cwd);
  if (/^python[0-9.]*$/.test(name) || name === "node") return inlineScript(name, args, cwd, env);
  if ((name === "bash" || name === "sh" || name === "zsh" || name === "dash") && depth < 3) {
    const k = args.findIndex((a) => !a.dyn && /^-[a-z]*c$/.test(a.w));
    if (k !== -1 && args[k + 1] && !args[k + 1].dyn) return { flow: null, sub: args[k + 1].w };
    return { flow: null };
  }
  if (PASSTHROUGH.has(name) && !operands(args).length) return { flow: flowIn };
  if (name === "sort" || name === "tee") {
    if (name === "sort") return { flow: flowOf(wholeFiles(operands(args).flatMap((w) => resolvePaths(w, cwd, env)))) };
    return { flow: flowIn };
  }
  return { flow: null };
}

function stdoutRedirected(cmd) {
  return cmd.redirs.some(
    (r) =>
      (r.op === ">" || r.op === ">>" || r.op === ">|" || r.op === "&>" || r.op === "&>>") &&
      (r.fd === null || r.fd === "1") &&
      r.target,
  );
}

function analyzeBash(command, cwd, env, limit, depth = 0) {
  let worst = null;
  const pipelines = parse(tokenize(command));
  for (const pl of pipelines) {
    let flow = null;
    for (const cmd of pl.cmds) {
      const r = stage(cmd, cwd, env, flow, depth);
      if (r.sub !== undefined) {
        const inner = analyzeBash(r.sub, cwd, env, limit, depth + 1);
        if (inner && (!worst || inner.bytes > worst.bytes)) worst = inner;
      }
      if (r.cwd !== undefined && pl.cmds.length === 1) cwd = r.cwd;
      flow = stdoutRedirected(cmd) ? null : r.flow;
    }
    if (flow && flow.bytes > limit && (!worst || flow.bytes > worst.bytes)) worst = flow;
  }
  return worst;
}

// Relative to cwd because octopus-delegate resolves --paths from the same directory.
function display(p, cwd) {
  if (!cwd) return p;
  const rel = path.relative(cwd, p);
  return rel && !rel.startsWith("..") && !path.isAbsolute(rel) ? rel : p;
}

function denyReason(kind, est, limit, cwd) {
  const names = est.files.map((f) => f.label || display(f.path, cwd));
  const shown = names.length > 4 ? `${names.slice(0, 4).join(", ")} and ${names.length - 4} more` : names.join(", ");
  const what = kind === "read" ? `This Read would put all of ${shown} (${fmtBytes(est.bytes)})` : `This command would print ${fmtBytes(est.bytes)} of ${shown}`;
  const paths = est.files.filter((f) => !f.label).map((f) => shellQuote(display(f.path, cwd)));
  const delegate = paths.length
    ? `octopus-delegate read --question "<what you need to know>" --paths ${paths.join(" ")}`
    : `octopus-delegate read --question "<what you need to know>" --paths <file>`;
  return [
    `Token optimizer: ${what} into your context; this run's limit for reading a whole file is ${fmtBytes(limit)}.`,
    `To understand it, delegate the read and get only the answer back: ${delegate}`,
    `If you need exact lines (to edit or quote them), read only that range: Read with offset and limit, or grep -n first to find the lines.`,
  ].join("\n");
}

function decideRead(input, env) {
  const ti = (input && input.tool_input) || {};
  if (ti.offset !== undefined && ti.offset !== null) return null;
  if (ti.limit !== undefined && ti.limit !== null) return null;
  if (typeof ti.file_path !== "string" || !ti.file_path) return null;
  let p = ti.file_path;
  if (!path.isAbsolute(p)) p = path.resolve(input.cwd || process.cwd(), p);
  if (SPECIAL_READ_EXT.has(path.extname(p).toLowerCase())) return null;
  const size = regularSize(p);
  const limit = threshold(env);
  if (size === null || size <= limit) return null;
  return denyReason("read", { bytes: size, files: [{ path: p, bytes: size }] }, limit, null);
}

function decideBash(input, env) {
  const ti = (input && input.tool_input) || {};
  if (typeof ti.command !== "string" || !ti.command.trim()) return null;
  const limit = threshold(env);
  const cwd = input.cwd || process.cwd();
  const est = analyzeBash(ti.command, cwd, env, limit);
  if (!est) return null;
  return denyReason("bash", est, limit, cwd);
}

function decide(mode, input, env) {
  if (env.OCTOPUS_TOKEN_OPTIMIZER !== "enforce") return null;
  if (failures(env) >= MAX_FAILURES) return null;
  if (mode === "read") return decideRead(input, env);
  if (mode === "bash") return decideBash(input, env);
  return null;
}

function denyOutput(reason) {
  return JSON.stringify({
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: reason,
    },
  });
}

function main() {
  const mode = process.argv[2];
  let raw = "";
  try {
    raw = fs.readFileSync(0, "utf8");
  } catch {
    process.exit(0);
  }
  try {
    const reason = decide(mode, JSON.parse(raw || "{}"), process.env);
    if (reason) process.stdout.write(denyOutput(reason) + "\n");
  } catch {
    // Allow: a hook bug must never stall the run.
  }
  process.exit(0);
}

if (require.main === module) main();

module.exports = { decide, analyzeBash, tokenize, parse, denyOutput, MAX_FAILURES, DEFAULT_THRESHOLD };
