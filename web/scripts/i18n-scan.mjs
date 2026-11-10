// A string that is not UI text carries `i18n-ignore` in a comment on its line or the line above.
import { parse } from "@astrojs/compiler";
import ts from "typescript";

const IGNORE = "i18n-ignore";
const anyWord = /[A-Za-z]{2,}/;
const words = /[A-Za-z][a-z']+[,;:!?]*\s+[A-Za-z]{2,}/;
const spacedWord = /^[A-Za-z][a-z]+[.,:;!?…)]*$/;
const classList = /^\s*[a-z0-9_-]+(\s+[a-z0-9_-]+)*\s*$/;
const capitalized = /^[A-Z][a-z']+[.!?…:]*$/;
const keyShape = /^[a-z0-9_]+(\.[a-z0-9_]+)+$/;
const htmlTag = /<\/?[a-zA-Z][^>]*>/g;
const entity = /&[a-z]+;|&#\d+;/g;
const textAttr =
  /\s(?:title|aria-label|aria-description|aria-placeholder|placeholder|alt|label|data-tooltip|data-title)\s*=\s*(["'])(.*?)\1/g;
const KEY_CALLS = new Set(["t", "tParts", "has", "tLog"]);
const placeholderRe = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;
const TEXT_ATTRS = new Set([
  "title",
  "aria-label",
  "aria-description",
  "aria-placeholder",
  "aria-roledescription",
  "aria-valuetext",
  "placeholder",
  "alt",
  "label",
  "data-tooltip",
  "data-title",
]);
const TEXT_PROPS = new Set([
  "textContent",
  "innerText",
  "innerHTML",
  "outerHTML",
  "title",
  "placeholder",
  "ariaLabel",
  "alt",
  "label",
]);
const MESSAGE_CALLS = new Map([
  ["alert", 0],
  ["confirm", 0],
  ["prompt", 0],
  ["toast", 0],
  ["showToast", 0],
  ["notify", 0],
  ["setText", 1],
]);
const NAME_CALLS = new Set([
  "querySelector",
  "querySelectorAll",
  "getElementById",
  "getElementsByClassName",
  "closest",
  "matches",
  "getAttribute",
  "hasAttribute",
  "removeAttribute",
  "toggleAttribute",
  "createElement",
  "addEventListener",
  "removeEventListener",
  "getItem",
  "setItem",
  "removeItem",
  "getPropertyValue",
  "setProperty",
  "get",
  "set",
  "has",
  "delete",
  "append",
  "fetch",
  "post",
  "put",
  "patch",
  "del",
  "startsWith",
  "endsWith",
  "includes",
  "indexOf",
  "split",
  "join",
  "replace",
  "replaceAll",
  "toLocaleString",
  "toLocaleDateString",
  "toLocaleTimeString",
  "localeCompare",
  "add",
  "remove",
  "toggle",
  "contains",
  "on",
  "emit",
]);
const stringLit = /"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'|`((?:[^`\\]|\\.)*)`/g;

function clip(s) {
  const one = s.replace(/\n/g, "⏎");
  return one.length > 90 ? one.slice(0, 90) + "…" : one;
}

function visible(s) {
  return s.replace(htmlTag, " ").replace(entity, " ");
}

function readsAsWords(s) {
  const v = visible(s)
    .replace(/\u0001/g, " ")
    .trim();
  if (classList.test(v) && /[-_]/.test(v)) return false;
  if (words.test(v) || capitalized.test(v)) return true;
  return /\s/.test(v) && v.split(/\s+/).some((tok) => spacedWord.test(tok));
}

function hasTextAttr(s) {
  for (const m of s.matchAll(textAttr)) if (anyWord.test(m[2])) return m[0].trim();
  return undefined;
}

export function placeholders(msg) {
  const out = new Set();
  const tmpls = msg === undefined ? [] : typeof msg === "string" ? [msg] : Object.values(msg);
  for (const tmpl of tmpls) for (const m of tmpl.matchAll(placeholderRe)) out.add(m[1]);
  return out;
}

function calleeName(call) {
  const e = call.expression;
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) return e.name.text;
  return "";
}

function isKeyCall(call) {
  const name = calleeName(call);
  if (!KEY_CALLS.has(name)) return false;
  return name !== "has" || ts.isIdentifier(call.expression);
}

function isIgnored(lines, line) {
  return (lines[line - 1] ?? "").includes(IGNORE) || (lines[line - 2] ?? "").includes(IGNORE);
}

function context(node) {
  let p = node.parent;
  let child = node;
  while (
    p &&
    (ts.isParenthesizedExpression(p) ||
      ts.isConditionalExpression(p) ||
      (ts.isBinaryExpression(p) &&
        (p.operatorToken.kind === ts.SyntaxKind.PlusToken ||
          p.operatorToken.kind === ts.SyntaxKind.BarBarToken ||
          p.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken)) ||
      ts.isTemplateSpan(p) ||
      ts.isTemplateExpression(p) ||
      ts.isAsExpression(p))
  ) {
    if (ts.isConditionalExpression(p) && p.condition === child) return "skip";
    child = p;
    p = p.parent;
  }
  if (!p) return "free";
  if (ts.isImportDeclaration(p) || ts.isExportDeclaration(p) || ts.isExternalModuleReference(p))
    return "skip";
  if (ts.isLiteralTypeNode(p)) return "skip";
  if (ts.isPropertyAssignment(p) && p.name === child) return "skip";
  if (ts.isElementAccessExpression(p) && p.argumentExpression === child) return "skip";
  if (ts.isCaseClause(p)) return "skip";
  if (
    ts.isBinaryExpression(p) &&
    [
      ts.SyntaxKind.EqualsEqualsEqualsToken,
      ts.SyntaxKind.ExclamationEqualsEqualsToken,
      ts.SyntaxKind.EqualsEqualsToken,
      ts.SyntaxKind.ExclamationEqualsToken,
      ts.SyntaxKind.InKeyword,
    ].includes(p.operatorToken.kind)
  )
    return "skip";
  if (
    ts.isBinaryExpression(p) &&
    p.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
    p.right === child
  ) {
    const left = p.left;
    if (ts.isPropertyAccessExpression(left) && TEXT_PROPS.has(left.name.text)) return "message";
    if (ts.isPropertyAccessExpression(left)) {
      const target = left.name.text;
      const onStyle =
        ts.isPropertyAccessExpression(left.expression) && left.expression.name.text === "style";
      if (target === "className" || target === "cssText" || onStyle) return "skip";
    }
    return "free";
  }
  if (ts.isCallExpression(p) || ts.isNewExpression(p)) {
    const name = calleeName(p);
    const args = p.arguments ?? ts.factory.createNodeArray();
    const idx = args.indexOf(child);
    if (isKeyCall(p)) return idx === 0 ? "key" : "free";
    if (ts.isNewExpression(p) && name === "Error") return "message";
    if (ts.isCallExpression(p)) {
      const e = p.expression;
      if (
        ts.isPropertyAccessExpression(e) &&
        ts.isIdentifier(e.expression) &&
        e.expression.text === "console"
      )
        return "message";
      if (MESSAGE_CALLS.has(name)) return idx === MESSAGE_CALLS.get(name) ? "message" : "skip";
      if (name === "setAttribute") {
        const first = args[0];
        if (idx === 1 && first && ts.isStringLiteralLike(first) && TEXT_ATTRS.has(first.text))
          return "message";
        return idx === 0 || (first && ts.isStringLiteralLike(first)) ? "skip" : "free";
      }
      if (NAME_CALLS.has(name) && idx === 0) return "skip";
    }
  }
  if (ts.isThrowStatement(p)) return "message";
  return "free";
}

export function createScanner(source) {
  const problems = [];
  const used = new Set();
  const groups = new Set();
  for (const k of Object.keys(source)) {
    const parts = k.split(".");
    for (let i = 2; i < parts.length; i++) groups.add(parts.slice(0, i).join("."));
  }
  function keyLiteral(file, line, s) {
    if (!keyShape.test(s)) return false;
    if (s in source) {
      used.add(s);
      return true;
    }
    const parts = s.split(".");
    if (parts.length < 3 || !groups.has(parts[0] + "." + parts[1])) return false;
    used.add(s);
    if (!(s in source)) problems.push({ file, line, msg: `key ${s} is not in the catalog` });
    return true;
  }
  function checkTCall(file, sf, call, lineOffset) {
    const [keyArg, paramsArg] = call.arguments;
    if (!keyArg || !ts.isStringLiteralLike(keyArg)) return;
    const line = sf.getLineAndCharacterOfPosition(keyArg.getStart(sf)).line + 1 + lineOffset;
    const key = keyArg.text;
    used.add(key);
    if (!(key in source)) {
      problems.push({ file, line, msg: `key ${key} is not in the catalog` });
      return;
    }
    if (calleeName(call) === "has") return;
    const want = placeholders(source[key]);
    const given = new Set();
    if (paramsArg) {
      if (!ts.isObjectLiteralExpression(paramsArg)) return;
      for (const p of paramsArg.properties) {
        if (ts.isSpreadAssignment(p)) return;
        const n = p.name;
        if (n && (ts.isIdentifier(n) || ts.isStringLiteral(n))) given.add(n.text);
      }
    }
    for (const p of want)
      if (!given.has(p))
        problems.push({
          file,
          line,
          msg: `key ${key}: the message uses {${p}} but the call does not pass it`,
        });
    for (const p of given)
      if (!want.has(p))
        problems.push({
          file,
          line,
          msg: `key ${key}: the call passes ${p} but the message does not use it`,
        });
  }
  function checkScript(file, code, lineOffset) {
    const sf = ts.createSourceFile(
      file,
      code,
      ts.ScriptTarget.Latest,
      true,
      /\.m?js$/.test(file) ? ts.ScriptKind.JS : ts.ScriptKind.TS,
    );
    const lines = code.split("\n");
    const visit = (node) => {
      if (ts.isCallExpression(node) && isKeyCall(node)) {
        checkTCall(file, sf, node, lineOffset);
      }
      let text;
      if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) text = node.text;
      else if (ts.isTemplateExpression(node)) {
        text = node.head.text + node.templateSpans.map((s) => " \u0001 " + s.literal.text).join("");
      }
      if (text !== undefined) {
        const at = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
        const line = at + lineOffset;
        const ctx = context(node);
        const ignored = isIgnored(lines, at);
        if (ctx === "key" || keyLiteral(file, line, text)) {
          // checked as a key
        } else if (ctx !== "skip" && !ignored) {
          const attr = hasTextAttr(text);
          if (attr)
            problems.push({ file, line, msg: `text attribute outside the catalog: ${clip(attr)}` });
          else if (ctx === "message" ? anyWord.test(visible(text)) : readsAsWords(text))
            problems.push({ file, line, msg: `text outside the catalog: ${clip(text)}` });
        }
        if (ts.isTemplateExpression(node)) {
          node.templateSpans.forEach((s) => visit(s.expression));
          return;
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  async function checkAstro(file, code) {
    const { ast } = await parse(code, { position: true });
    const lines = code.split("\n");
    const lineOf = (n) => n.position?.start.line ?? 0;
    const walk = (node, inExpr) => {
      if (node.type === "frontmatter" && node.value !== undefined) {
        checkScript(file, node.value, lineOf(node) - 1);
        return;
      }
      if (node.type === "element" && (node.name === "script" || node.name === "style")) {
        if (node.name === "script") {
          const body = (node.children ?? []).map((c) => c.value ?? "").join("");
          checkScript(file, body, lineOf(node.children?.[0] ?? node) - 1);
        }
        return;
      }
      if (node.type === "text" && node.value !== undefined) {
        const line = lineOf(node);
        if (!isIgnored(lines, line)) {
          if (inExpr) {
            for (const m of node.value.matchAll(stringLit)) {
              const s = m[1] ?? m[2] ?? m[3] ?? "";
              if (keyLiteral(file, line, s)) continue;
              if (readsAsWords(s) || hasTextAttr(s))
                problems.push({ file, line, msg: `text outside the catalog: ${clip(s)}` });
            }
          } else if (anyWord.test(visible(node.value))) {
            problems.push({
              file,
              line,
              msg: `text outside the catalog: ${clip(node.value.trim())}`,
            });
          }
        }
      }
      for (const a of node.attributes ?? []) {
        const line = lineOf(a) || lineOf(node);
        if (isIgnored(lines, line) || !a.name) continue;
        if (a.kind === "expression") {
          for (const m of (a.value ?? "").matchAll(stringLit)) {
            const lit = m[1] ?? m[2] ?? m[3] ?? "";
            if (keyLiteral(file, line, lit)) continue;
            if (readsAsWords(lit) || (TEXT_ATTRS.has(a.name) && anyWord.test(lit)))
              problems.push({
                file,
                line,
                msg: `attribute ${a.name} outside the catalog: ${clip(lit)}`,
              });
          }
          continue;
        }
        if (a.kind !== "quoted" && a.kind !== "template-literal") continue;
        const v = a.value ?? "";
        const isComponent = node.type === "component" || /^[A-Z]/.test(node.name ?? "");
        const shown = TEXT_ATTRS.has(a.name) || a.name === "subtitle" || a.name === "heading";
        if (shown ? anyWord.test(v) : isComponent && readsAsWords(v))
          problems.push({ file, line, msg: `attribute ${a.name} outside the catalog: ${clip(v)}` });
      }
      if (node.type === "expression") {
        for (const c of node.children ?? []) walk(c, c.type === "text");
        return;
      }
      for (const c of node.children ?? []) walk(c, false);
    };
    walk(ast, false);
  }
  return { problems, used, checkScript, checkAstro };
}

export function localeReport(loc, source, other) {
  const report = [];
  for (const [k, msg] of Object.entries(source)) {
    if (!(k in other)) {
      report.push(`${loc}: missing ${k}`);
      continue;
    }
    const a = [...placeholders(msg)].sort().join(",");
    const b = [...placeholders(other[k])].sort().join(",");
    if (a !== b) report.push(`${loc}: ${k} has {${b}}, the source has {${a}}`);
  }
  for (const k of Object.keys(other))
    if (!(k in source)) report.push(`${loc}: not in the source: ${k}`);
  return report;
}
