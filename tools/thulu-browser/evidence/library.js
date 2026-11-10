import { t } from "../i18n.js";
import { TakeError, stepSchema } from "./schema.js";

const MAX_DEPTH = 5;
const PARAM_RE = /\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g;

function substitute(value, params, where, problems) {
  return value.replace(PARAM_RE, (_, name) => {
    if (Object.hasOwn(params, name)) return params[name];
    problems.push(t("errors.compile_no_value", { where, name }));
    return "";
  });
}

function resolveStrings(node, visit, key = "") {
  if (typeof node === "string") return visit(node, key);
  if (Array.isArray(node)) return node.map((n) => resolveStrings(n, visit, key));
  if (node && typeof node === "object") {
    return Object.fromEntries(Object.entries(node).map(([k, v]) => [k, resolveStrings(v, visit, k)]));
  }
  return node;
}

// Only selector-holding fields from schema.js resolve @names; add new selector fields here. An @ in
// a label or value is text.
const TARGET_FIELDS = new Set(["target", "redact", "wait_for"]);

function resolveTarget(value, key, targets, where, problems) {
  if (!TARGET_FIELDS.has(key) || !value.startsWith("@")) return value;
  const name = value.slice(1);
  if (Object.hasOwn(targets, name)) return targets[name];
  problems.push(t("errors.compile_unknown_target", { where, name }));
  return value;
}

function compileList(steps, library, params, trail, problems) {
  const out = [];
  steps.forEach((step, i) => {
    const where = trail.length
      ? t("text.where_nested_step", { trail: trail.join(" > "), step: i + 1 })
      : t("text.where_step", { step: i + 1 });
    if (step.kind === "use") {
      out.push(...expandFlow(step, library, params, [...trail, `use ${step.flow}`], where, problems)); // i18n-ignore the step as a take writes it
      return;
    }
    const resolved = resolveStrings(step, (s, key) => {
      if (key === "kind") return s;
      const withParams = substitute(s, params, where, problems);
      return resolveTarget(withParams, key, library.targets, where, problems);
    });
    out.push(resolved);
  });
  return out;
}

function expandFlow(step, library, outerParams, trail, where, problems) {
  if (trail.length > MAX_DEPTH) {
    problems.push(t("errors.compile_too_deep", { where, max: MAX_DEPTH, trail: trail.join(" > ") }));
    return [];
  }
  const flow = library.flows[step.flow];
  if (!flow) {
    problems.push(t("errors.compile_unknown_flow", { where, flow: step.flow }));
    return [];
  }
  const given = Object.fromEntries(
    Object.entries(step.params || {}).map(([k, v]) => [k, substitute(v, outerParams, where, problems)]),
  );
  for (const p of flow.params || []) {
    if (!Object.hasOwn(given, p)) problems.push(t("errors.compile_missing_param", { where, flow: step.flow, param: p }));
  }
  return compileList(flow.steps, library, given, trail, problems);
}

export function compileSteps(steps, library) {
  const problems = [];
  const out = compileList(steps, library, {}, [], problems);
  // Substitution can break a step (an empty target), so the compiled list is validated again.
  out.forEach((s, i) => {
    const r = stepSchema.safeParse(s);
    if (!r.success) {
      problems.push(t("errors.compile_invalid_step", {
        step: i + 1,
        kind: s.kind,
        issues: r.error.issues.map((x) => x.message).join("; "),
      }));
    }
  });
  if (problems.length) throw new TakeError(t("errors.take_does_not_compile"), problems);
  return out;
}
