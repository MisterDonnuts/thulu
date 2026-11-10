// Takes are data only: the engine runs in a container shared by every workspace, so a take may
// drive the page but never run code in this process.

import { z } from "zod";

import { t } from "../i18n.js";

const text = z.string().trim().min(1);
const target = text.describe(t("params.take_target"));
const ms = (max) => z.number().int().min(0).max(max);

export const TONES = ["focus", "ok", "warn", "bad"];
export const CHECKS = ["visible", "hidden", "text", "value", "count", "url", "title"];
export const MATCHES = ["equals", "contains", "regex"];

const base = {
  label: z.string().trim().max(160).optional(),
  side_effect: z.boolean().optional(),
};

const spot = z.object({
  target,
  note: z.string().max(300).optional(),
  tone: z.enum(TONES).optional(),
});

export const shotSpec = z.object({
  name: text.max(80),
  title: z.string().max(160).optional(),
  spotlight: z.array(spot).max(6).optional(),
  notes: z.array(z.string().max(300)).max(6).optional(),
  redact: z.array(target).max(20).optional(),
  dim: z.boolean().optional(),
  full_page: z.boolean().optional(),
  keep_clean: z.boolean().optional(),
  wait_ms: ms(30_000).optional(),
});

const step = (kind, fields) => z.object({ kind: z.literal(kind), ...base, ...fields }).strict();

export const stepSchema = z.discriminatedUnion("kind", [
  step("goto", { url: text, wait_for: target.optional() }),
  step("click", { target }),
  step("fill", {
    target,
    value: z.string().optional(),
    secret: z.string().regex(/^[A-Za-z_][A-Za-z0-9_]*$/).optional(),
  }),
  step("type", {
    target,
    value: z.string(),
    delay_ms: ms(1_000).optional(),
    click: z.boolean().optional(),
  }),
  step("select", { target, value: z.string() }),
  step("check", { target, checked: z.boolean().default(true) }),
  step("hover", { target }),
  step("press", { key: text, target: target.optional() }),
  step("scroll", {
    direction: z.enum(["down", "up", "top", "bottom"]).optional(),
    amount: z.number().int().min(1).max(20_000).optional(),
    target: target.optional(),
  }),
  step("wait_for", {
    target,
    state: z.enum(["visible", "hidden"]).default("visible"),
    timeout_ms: ms(60_000).optional(),
  }),
  step("wait_for_response", { url_part: text, timeout_ms: ms(60_000).optional() }),
  step("beat", { ms: ms(10_000) }),
  step("hold", { ms: ms(30_000) }),
  step("viewport", { width: z.number().int().min(240).max(3840), height: z.number().int().min(240).max(2160) }),
  step("shot", { shot: shotSpec }),
  step("verify", {
    case: text.max(80),
    expected: z.string().max(500),
    observed: z.string().max(500).optional(),
    check: z.enum(CHECKS).optional(),
    target: target.optional(),
    match: z.enum(MATCHES).optional(),
    timeout_ms: ms(30_000).optional(),
    detail: z.string().max(1_000).optional(),
  }),
  step("note", { text: text.max(300), level: z.enum(["step", "fact", "issue"]).default("step") }),
  // Runs only inside the page (page.evaluate), never in the server process.
  step("script", { code: text.max(20_000) }),
  step("use", { flow: text, params: z.record(z.string()).optional() }),
]).superRefine((s, ctx) => {
  if (s.kind === "fill" && s.value === undefined && !s.secret) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: t("errors.fill_needs_value") });
  }
  if (s.kind === "fill" && s.value !== undefined && s.secret) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: t("errors.fill_value_and_secret") });
  }
  if (s.kind === "verify") {
    if (s.observed === undefined && !s.check) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, message: t("errors.verify_needs_observed") });
    }
    if (s.check && !["url", "title"].includes(s.check) && !s.target) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, message: t("errors.verify_needs_target", { check: s.check }) });
    }
  }
  if (s.kind === "scroll" && !s.direction && !s.target) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, message: t("errors.scroll_needs_direction") });
  }
});

export const takeSchema = z.object({
  version: z.literal(1).default(1),
  title: text.max(160),
  base_url: z.string().url(),
  viewport: z.object({
    width: z.number().int().min(240).max(3840),
    height: z.number().int().min(240).max(2160),
  }).default({ width: 1440, height: 900 }),
  timezone: z.string().max(64).optional(),
  locale: z.string().max(35).optional(),
  allowed_hosts: z.array(text).max(20).optional(),
  redact_css: z.array(text).max(20).optional(),
  steps: z.array(stepSchema).min(1).max(500),
}).strict();

const flowSchema = z.object({
  description: z.string().max(300).optional(),
  params: z.array(text).max(20).optional(),
  steps: z.array(stepSchema).min(1).max(200),
}).strict();

export const librarySchema = z.object({
  targets: z.record(text).default({}),
  flows: z.record(flowSchema).default({}),
}).strict().default({});

export class TakeError extends Error {
  constructor(message, issues = []) {
    super(issues.length ? `${message}:\n${issues.map((i) => `  - ${i}`).join("\n")}` : message);
    this.name = "TakeError";
  }
}

function issuesOf(err, root) {
  return err.issues.map((i) => {
    const where = [root, ...i.path].filter((p) => p !== "").join(".");
    return `${where}: ${i.message}`;
  });
}

function parseWith(schema, input, what) {
  let value = input;
  if (typeof value === "string") {
    try {
      value = JSON.parse(value);
    } catch (e) {
      throw new TakeError(t("errors.invalid_json", { what, err: e.message }));
    }
  }
  const r = schema.safeParse(value ?? undefined);
  if (!r.success) throw new TakeError(t("errors.invalid_input", { what }), issuesOf(r.error, what));
  return r.data;
}

export const parseTake = (input) => parseWith(takeSchema, input, "take");
export const parseLibrary = (input) => parseWith(librarySchema, input, "library");
export const parseShot = (input) => parseWith(shotSpec, input, "shot");
