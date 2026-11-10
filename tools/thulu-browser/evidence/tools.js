import { rm } from "node:fs/promises";

import { t } from "../i18n.js";
import { OctopusHttpError, getRecording, getRecordingSecret, saveRecordingTake, uploadAttachment } from "../lib/octopus.js";
import { NoCapture } from "./capture.js";
import { describeBundle } from "./bundle.js";
import { Director } from "./director.js";
import { LiveTake } from "./live.js";
import { publishBundle, slug } from "./publish.js";
import { compileSteps } from "./library.js";
import { renderTake } from "./render.js";
import { CHECKS, MATCHES, TONES, parseLibrary, parseShot, parseTake, stepSchema } from "./schema.js";
import { pointAt } from "./selector.js";
import { ExpiringStore } from "./store.js";

const takes = new ExpiringStore({ ttlMs: 3 * 60 * 60_000, max: 200 });
const renders = new ExpiringStore({ ttlMs: 60 * 60_000, max: 8, onEvict: (r) => rm(r.bundle.dir, { recursive: true, force: true }) });

const SESSION = { type: "string", description: t("params.session_id") };
const LABEL = { type: "string", description: t("params.label") };
const REF = {
  type: "object",
  properties: {
    index: { type: "integer", description: t("params.element_index") },
    target: { type: "string", description: t("params.element_target") },
  },
};

export const EVIDENCE_TOOLS = [
  {
    name: "evidence_start",
    description: t("tools.evidence_start.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: SESSION,
        title: { type: "string", description: t("tools.evidence_start.params.title") },
        base_url: { type: "string", description: t("tools.evidence_start.params.base_url") },
        redact_css: { type: "array", items: { type: "string" }, description: t("tools.evidence_start.params.redact_css") },
        changes_data: { type: "boolean", default: false },
      },
      required: ["session_id", "title"],
    },
  },
  {
    name: "evidence_shot",
    description: t("tools.evidence_shot.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: SESSION,
        name: { type: "string", description: t("tools.evidence_shot.params.name") },
        title: { type: "string", description: t("tools.evidence_shot.params.title") },
        spotlight: {
          type: "array",
          items: { ...REF, properties: { ...REF.properties, note: { type: "string" }, tone: { type: "string", enum: TONES } } },
          description: t("tools.evidence_shot.params.spotlight"),
        },
        notes: { type: "array", items: { type: "string" }, description: t("tools.evidence_shot.params.notes") },
        redact: { type: "array", items: REF, description: t("tools.evidence_shot.params.redact") },
        dim: { type: "boolean", description: t("tools.evidence_shot.params.dim") },
        full_page: { type: "boolean", default: false },
        keep_clean: { type: "boolean", default: false, description: t("tools.evidence_shot.params.keep_clean") },
        wait_ms: { type: "integer", description: t("tools.evidence_shot.params.wait_ms") },
        label: LABEL,
        task_id: { type: "string", description: t("tools.evidence_shot.params.task_id") },
      },
      required: ["session_id", "name"],
    },
  },
  {
    name: "evidence_verify",
    description: t("tools.evidence_verify.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: SESSION,
        case: { type: "string", description: t("tools.evidence_verify.params.case") },
        expected: { type: "string", description: t("tools.evidence_verify.params.expected") },
        check: { type: "string", enum: CHECKS },
        index: REF.properties.index,
        target: REF.properties.target,
        match: { type: "string", enum: MATCHES, description: t("tools.evidence_verify.params.match") },
        observed: { type: "string", description: t("tools.evidence_verify.params.observed") },
        timeout_ms: { type: "integer", description: t("tools.evidence_verify.params.timeout_ms") },
        detail: { type: "string" },
        label: LABEL,
      },
      required: ["session_id", "case", "expected"],
    },
  },
  {
    name: "evidence_note",
    description: t("tools.evidence_note.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: SESSION,
        text: { type: "string" },
        level: { type: "string", enum: ["step", "fact", "issue"], default: "step" },
      },
      required: ["session_id", "text"],
    },
  },
  {
    name: "evidence_finish",
    description: t("tools.evidence_finish.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: SESSION,
        discard: { type: "boolean", default: false },
      },
      required: ["session_id"],
    },
  },
  {
    name: "evidence_get_take",
    description: t("tools.evidence_get_take.description"),
    inputSchema: {
      type: "object",
      properties: { take_id: { type: "string" } },
      required: ["take_id"],
    },
  },
  {
    name: "evidence_render",
    description: t("tools.evidence_render.description"),
    inputSchema: {
      type: "object",
      properties: {
        take: { type: "object", description: t("tools.evidence_render.params.take") },
        take_id: { type: "string" },
        library: {
          type: "object",
          properties: {
            targets: { type: "object", description: t("tools.evidence_render.params.library_targets") },
            flows: { type: "object", description: t("tools.evidence_render.params.library_flows") },
          },
        },
        secrets: { type: "object", description: t("tools.evidence_render.params.secrets") },
        base_url: { type: "string", description: t("tools.evidence_render.params.base_url") },
        compare_base_url: { type: "string", description: t("tools.evidence_render.params.compare_base_url") },
        labels: { type: "array", items: { type: "string" }, description: t("tools.evidence_render.params.labels") },
        pace: { type: "number", description: t("tools.evidence_render.params.pace") },
        allow_side_effects: { type: "boolean", default: false },
        task_id: { type: "string", description: t("tools.evidence_render.params.task_id") },
      },
    },
  },
  {
    name: "evidence_recording",
    description: t("tools.evidence_recording.description"),
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "evidence_save_case",
    description: t("tools.evidence_save_case.description"),
    inputSchema: {
      type: "object",
      properties: {
        take_id: { type: "string", description: t("tools.evidence_save_case.params.take_id") },
        render_id: { type: "string", description: t("tools.evidence_save_case.params.render_id") },
        take: { type: "object", description: t("tools.evidence_save_case.params.take") },
        title: { type: "string", description: t("tools.evidence_save_case.params.title") },
      },
    },
  },
  {
    name: "evidence_publish",
    description: t("tools.evidence_publish.description"),
    inputSchema: {
      type: "object",
      properties: {
        render_id: { type: "string" },
        task_id: { type: "string", description: t("tools.evidence_publish.params.task_id") },
        record_runs: { type: "boolean", default: true, description: t("tools.evidence_publish.params.record_runs") },
      },
      required: ["render_id"],
    },
  },
];

const text = (body, extra = []) => ({ content: [{ type: "text", text: body }, ...extra] });
const sheet = (b) => (b.sheet ? [{ type: "image", data: b.sheet.toString("base64"), mimeType: "image/jpeg" }] : []);

function caseLines(cases, reportId) {
  if (!cases) return [];
  const lines = [];
  if (cases.recorded.length) {
    lines.push(t("text.cases_recorded", { runs: cases.recorded.map((r) => `${r.key} ${r.verdict}`).join(", ") }));
  }
  if (cases.unknown.length) lines.push(t("text.cases_unknown", { keys: cases.unknown.join(", ") }));
  if (cases.error) lines.push(t("text.cases_error", { error: cases.error, report: reportId }));
  return lines;
}

function requireTake(s) {
  if (!s.take) throw new Error(t("errors.no_take_recording"));
  return s.take;
}

function stillDirector(page) {
  return new Director(page, { capture: new NoCapture() });
}

async function refs(page, list = []) {
  return Promise.all(list.map(async (r) => ({ ...r, target: await pointAt(page, r) })));
}

function keepRender(bundle, taskId, takeId) {
  const id = renders.put({ bundle, taskId });
  const lines = [describeBundle(id, bundle)];
  if (takeId) lines.push(`take_id: ${takeId}`);
  return text(lines.join("\n"), sheet(bundle));
}

// The daemon answers 404 on recording routes for any key that is not a recording chat's.
async function inRecording(fn) {
  try {
    return await fn();
  } catch (err) {
    if (err instanceof OctopusHttpError && err.status === 404) throw new Error(t("errors.not_recording_chat"));
    throw err;
  }
}

function block(title, body) {
  const text = String(body ?? "").trim();
  return text ? [title, text, ""] : [];
}

function describeRecording(r) {
  const c = r.case ?? {};
  const env = r.environment ?? {};
  const names = env.secret_names ?? [];
  return [
    t("text.recording_case", { case: [c.key, c.title].filter(Boolean).join(" · ") }),
    "",
    ...block(t("text.recording_description"), c.description),
    ...block(t("text.recording_steps"), c.steps),
    ...block(t("text.recording_expected"), c.expected),
    t("text.recording_environment", { name: env.name ?? "", url: env.base_url ?? "" }),
    names.length ? t("text.recording_logins", { names: names.join(", ") }) : t("text.recording_no_logins"),
    "",
    t("text.recording_next"),
  ].join("\n");
}

function takeToSave(a) {
  const given = [a.take, a.take_id, a.render_id].filter((v) => v !== undefined && v !== null && v !== "");
  if (given.length !== 1) throw new Error(t("errors.save_one_take"));
  let raw = a.take;
  if (a.take_id) raw = takes.require(a.take_id, "take");
  if (a.render_id) {
    raw = renders.require(a.render_id, "render").bundle.take;
    if (!raw) throw new Error(t("errors.render_has_no_take", { id: a.render_id }));
  }
  const take = parseTake(raw);
  return a.title ? parseTake({ ...take, title: a.title }) : take;
}

export async function typedText(args) {
  if (args.text !== undefined && args.text !== null) return args;
  if (!args.secret) throw new Error(t("errors.type_needs_text"));
  try {
    return { ...args, text: await getRecordingSecret(args.secret) };
  } catch (err) {
    if (err instanceof OctopusHttpError && err.status === 404) {
      throw new Error(t("errors.secret_not_available", { name: args.secret }));
    }
    throw err;
  }
}

/** @param {{ requireSession: (id: string) => any }} deps */
export function evidenceHandlers({ requireSession }) {
  return {
    async evidence_start(a) {
      const s = requireSession(a.session_id);
      if (s.take) throw new Error(t("errors.take_already_recording", { title: s.take.title }));
      s.take = await LiveTake.start(s.page, { title: a.title, baseUrl: a.base_url, redactCss: a.redact_css,
        changesData: a.changes_data });
      return text(t("text.recording_started", { title: a.title, url: s.take.baseUrl }));
    },

    async evidence_shot(a) {
      const s = requireSession(a.session_id);
      const spec = parseShot({
        name: a.name, title: a.title, notes: a.notes, dim: a.dim, full_page: a.full_page, keep_clean: a.keep_clean,
        wait_ms: a.wait_ms,
        spotlight: (await refs(s.page, a.spotlight)).map(({ target, note, tone }) => ({ target, note, tone })),
        redact: (await refs(s.page, a.redact)).map((r) => r.target),
      });
      const step = { kind: "shot", label: a.label, shot: spec };
      const missing = (shot) => (shot.missing.length ? t("text.shot_not_on_screen", { missing: shot.missing.join(", ") }) : "") +
        (shot.crowded ? t("text.shot_crowded") : "");
      if (s.take) {
        const shot = await s.take.perform(step);
        return text(t("text.shot_recorded", { name: spec.name }) + missing(shot));
      }
      const taskId = a.task_id || s.meta.task_id;
      if (!taskId) throw new Error(t("errors.shot_needs_task"));
      const shot = await stillDirector(s.page).perform(step, 0);
      const name = slug(spec.name, "capture");
      const att = await uploadAttachment(taskId, `${name}.png`, shot.png, "image/png");
      const lines = [t("text.shot_attached", { name, task: taskId, id: att.id })];
      if (shot.clean) {
        const clean = await uploadAttachment(taskId, `${name}-clean.png`, shot.clean, "image/png");
        lines.push(t("text.shot_clean", { name, id: clean.id }));
      }
      return text(lines.join("\n") + missing(shot));
    },

    async evidence_verify(a) {
      const s = requireSession(a.session_id);
      const target = a.index !== undefined || a.target ? await pointAt(s.page, a) : undefined;
      const step = stepSchema.parse({
        kind: "verify", label: a.label, case: a.case, expected: a.expected, observed: a.observed, check: a.check,
        target, match: a.match, timeout_ms: a.timeout_ms, detail: a.detail,
      });
      const v = s.take ? await s.take.perform(step) : await stillDirector(s.page).perform(step, 0);
      return text(
        t("text.verify_result", { result: v.pass ? "PASS" : "FAIL", case: v.case, expected: v.expected, observed: v.observed }) +
          (v.measured ? t("text.verify_measured") : "") +
          (s.take ? t("text.verify_in_take") : ""),
      );
    },

    async evidence_note(a) {
      const s = requireSession(a.session_id);
      await requireTake(s).perform(stepSchema.parse({ kind: "note", text: a.text, level: a.level }));
      return text(t("text.noted", { level: a.level || "step" }));
    },

    async evidence_finish(a) {
      const s = requireSession(a.session_id);
      const take = requireTake(s);
      s.take = null;
      if (a.discard) {
        await take.discard();
        return text(t("text.take_discarded", { title: take.title }));
      }
      const bundle = await take.finish();
      const takeId = bundle.take ? takes.put(bundle.take) : null;
      return keepRender(bundle, s.meta.task_id, takeId);
    },

    async evidence_get_take(a) {
      return text(JSON.stringify(takes.require(a.take_id, "take"), null, 2));
    },

    async evidence_render(a) {
      if (!a.take && !a.take_id) throw new Error(t("errors.take_or_id"));
      const take = parseTake(a.take ?? takes.require(a.take_id, "take"));
      const bundle = await renderTake(take, {
        library: parseLibrary(a.library ?? {}), secrets: a.secrets, baseUrl: a.base_url,
        compareBaseUrl: a.compare_base_url, labels: a.labels, pace: a.pace, allowSideEffects: a.allow_side_effects,
      });
      return keepRender(bundle, a.task_id, takes.put(take));
    },

    async evidence_recording() {
      return text(describeRecording(await inRecording(() => getRecording())));
    },

    async evidence_save_case(a) {
      const take = takeToSave(a);
      // The batch runner (job.js) replays with an empty library: flows or @targets would always
      // fail there.
      const steps = compileSteps(take.steps, parseLibrary({}));
      // Read the recording first: outside a recording chat it fails before the take is sent.
      const rec = await inRecording(() => getRecording());
      const saved = await inRecording(() => saveRecordingTake(take));
      const key = saved?.case?.key || rec.case?.key || "";
      const env = rec.environment?.name || "";
      const batch = saved?.batch?.id || "";
      const lines = [batch ? t("text.case_saved_queued", { key, env, batch }) : t("text.case_saved", { key })];
      if (steps.some((st) => st.side_effect)) lines.push(t("text.case_saved_changes_data"));
      const verifies = steps.filter((st) => st.kind === "verify");
      if (!verifies.length) lines.push(t("text.case_saved_no_checks"));
      else if (verifies.some((st) => !st.check)) lines.push(t("text.case_saved_observed_only"));
      return text(lines.join("\n"));
    },

    async evidence_publish(a) {
      const { bundle, taskId } = renders.require(a.render_id, "render");
      const out = await publishBundle(bundle, a.task_id || taskId, { recordRuns: a.record_runs !== false });
      const { video, shots } = out.attachments;
      const lines = [
        t("text.report_posted", { id: out.report_id }),
        video ? t("text.publish_video", { filename: video.filename, id: video.id }) : t("text.publish_no_video"),
        t("text.publish_captures", { count: shots.length }),
        ...caseLines(out.cases, out.report_id),
      ];
      return text(lines.join("\n"));
    },
  };
}

export async function recordAction(s, kind, args) {
  if (!s.take) return null;
  const take = s.take;
  const label = args.label || undefined;
  const target = ["click", "hover", "type", "select"].includes(kind) ? await take.target(args) : undefined;
  let step;
  switch (kind) {
    case "goto": step = { kind, label, url: args.url }; break;
    case "click":
    case "hover": step = { kind, label, target }; break;
    case "select": step = { kind, label, target, value: args.value }; break;
    case "type":
      if (args.secret) step = { kind: "fill", label, target, secret: args.secret };
      else step = args.clear_first === false ? { kind: "type", label, target, value: args.text } : { kind: "fill", label, target, value: args.text };
      break;
    case "press": step = { kind, label, key: args.key }; break;
    case "scroll": step = { kind, label, direction: args.direction, amount: args.amount }; break;
    case "beat": step = { kind, label, ms: Math.min(Math.max(Number(args.ms) || 1000, 0), 10_000) }; break;
    default: throw new Error(t("errors.cannot_record", { kind }));
  }
  const parsed = stepSchema.parse(step);
  await take.perform(parsed, args.secret ? args.text : undefined);
  return parsed;
}

export async function discardSessionTake(s) {
  if (s?.take) {
    const take = s.take;
    s.take = null;
    await take.discard().catch(() => {});
  }
}

export function clearEvidenceStores() {
  renders.clear();
  takes.clear();
}
