import { readFile, rm } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { t, tLog } from "../i18n.js";
import { closeBrowser } from "../lib/browser.js";
import { OctopusHttpError } from "../lib/octopus.js";
import { runVerdict } from "./cases.js";
import { extractFrame } from "./ffmpeg.js";
import { slug, uploadBundle } from "./publish.js";
import { renderTake } from "./render.js";
import { buildReport, judgedRun } from "./report.js";
import { parseLibrary, parseTake } from "./schema.js";

const RETRY_DELAYS_MS = [500, 1_000, 2_000, 4_000];
const REACH_WINDOW_MS = 30_000;
const REACH_EVERY_MS = 1_000;
const REACH_ATTEMPT_MS = 5_000;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export const artifactHref = (a) => `/evidence/artifacts/${a.id}`;

function transient(err) {
  if (err instanceof OctopusHttpError) return err.status >= 500 || err.status === 429;
  return err instanceof TypeError || Boolean(err?.cause?.code);
}

function unauthorized(err) {
  return err instanceof OctopusHttpError && (err.status === 401 || err.status === 403);
}

function describe(err) {
  const cause = err?.cause;
  const why = cause?.code ?? cause?.errors?.find((e) => e?.code)?.code ?? cause?.message;
  return `${err?.message ?? String(err)}${why ? ` (${why})` : ""}`.split("\n")[0];
}

/**
 * The runner routes, with retries on transient failures. `fetch` and the
 * delays are injectable so tests point it at a fake daemon.
 * @param {{ api: string, key: string, fetch?: typeof fetch, retryDelays?: number[],
 *           log?: (line: string) => void }} opts
 */
export function runnerClient({ api, key, fetch: f = globalThis.fetch, retryDelays = RETRY_DELAYS_MS,
  log = () => {} }) {
  const root = String(api).replace(/\/+$/, "");

  // init() rebuilds the body on every attempt: a FormData stream cannot be sent twice.
  async function request(method, route, init = () => ({})) {
    for (let attempt = 0; ; attempt++) {
      let err;
      try {
        const req = init();
        const res = await f(`${root}${route}`, { method, ...req, headers: { ...(req.headers || {}), "X-API-Key": key } });
        const text = await res.text();
        if (res.ok) {
          if (res.status === 204 || !text) return null;
          try { return JSON.parse(text); } catch { return { raw: text }; }
        }
        err = new OctopusHttpError(method, route, res.status, text);
      } catch (e) {
        err = e;
      }
      if (!transient(err) || attempt >= retryDelays.length) throw err;
      log(tLog("log.job_retry", { method, route, error: describe(err), ms: retryDelays[attempt] }));
      await sleep(retryDelays[attempt]);
    }
  }

  const json = (body) => () => ({ headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const runRoute = (batch, run, what) =>
    `/evidence/jobs/${encodeURIComponent(batch)}/runs/${encodeURIComponent(run)}/${what}`;

  return {
    next: (batch) => request("POST", `/evidence/jobs/${encodeURIComponent(batch)}/next`, json({})),
    artifact: (batch, run, { kind, filename, body, mime }) => request("POST", runRoute(batch, run, "artifacts"), () => {
      const fd = new FormData();
      fd.append("kind", kind);
      fd.append("file", new Blob([body], { type: mime }), filename);
      return { body: fd };
    }),
    result: (batch, run, result) => request("POST", runRoute(batch, run, "result"), json(result)),
    unreachable: (batch, error) =>
      request("POST", `/evidence/jobs/${encodeURIComponent(batch)}/unreachable`, json({ error })),
  };
}

/**
 * Wait until url answers at all: any HTTP status counts, since a login page
 * or an error page is still the app. A refused connection, a DNS failure or a
 * request that hangs past attemptMs does not, and is asked again every everyMs
 * until windowMs is up. `fetch` and the times are injectable for tests.
 * @returns {Promise<{ ok: boolean, error?: string }>}
 */
export async function waitReachable(url, { fetch: f = globalThis.fetch, windowMs = REACH_WINDOW_MS,
  everyMs = REACH_EVERY_MS, attemptMs = REACH_ATTEMPT_MS } = {}) {
  const deadline = Date.now() + windowMs;
  for (;;) {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), attemptMs);
    let error;
    try {
      const res = await f(url, { method: "GET", redirect: "manual", signal: ctl.signal });
      await res.body?.cancel().catch(() => {});
      return { ok: true };
    } catch (err) {
      error = describe(err);
    } finally {
      clearTimeout(timer);
    }
    if (Date.now() + everyMs >= deadline) return { ok: false, error };
    await sleep(everyMs);
  }
}

export function scrubber(values) {
  // Values under 4 chars are not scrubbed: they would cut ordinary words out of the report.
  const cut = [...new Set(values.filter((v) => typeof v === "string" && v.length >= 4))].sort((a, b) => b.length - a.length);
  return (text) => {
    let out = String(text ?? "");
    for (const v of cut) out = out.split(v).join("•••");
    return out;
  };
}

function emptyResult(status, durationMs, error = "") {
  return { status, checks: [], failures: [], duration_ms: durationMs, report_md: "", error, video_id: "",
    take_id: "", chapters_id: "", poster_id: "", shot_ids: [] };
}

async function posterOf(bundle, base) {
  if (bundle.video) {
    const out = path.join(bundle.dir, "poster.jpg");
    const at = Math.min(1, bundle.duration / 2_000);
    const file = await extractFrame(bundle.video, out, at).catch(() => null);
    if (file) return { kind: "poster", filename: `${base}.poster.jpg`, body: await readFile(file), mime: "image/jpeg" };
  }
  const shot = judgedRun(bundle.runs).shots[0];
  if (shot) return { kind: "poster", filename: `${base}.poster.png`, body: shot.png, mime: "image/png" };
  if (bundle.sheet) return { kind: "poster", filename: `${base}.poster.jpg`, body: bundle.sheet, mime: "image/jpeg" };
  return null;
}

/**
 * Replay one run of the batch and upload its files. Never throws for what the
 * take or the environment did wrong: that is a result (`error` when the replay
 * could not run). Throws only when the daemon cannot be reached or refuses
 * the key.
 * @param {{ run: {id: string}, take: unknown, base_url?: string, allowed_hosts?: string[],
 *           secrets?: Record<string, string> }} job
 * @param {{ upload: (file: object) => Promise<{id: string}|null>, pace?: number, scrub: (s: string) => string,
 *           log: (line: string) => void }} opts
 */
export async function replayRun(job, { upload, pace = 1, scrub, log }) {
  const started = Date.now();
  const elapsed = () => Date.now() - started;
  let take;
  try {
    take = parseTake(job.take);
  } catch (err) {
    return emptyResult("error", elapsed(), scrub(err.message));
  }
  const hosts = [...new Set([...(take.allowed_hosts ?? []), ...(job.allowed_hosts ?? [])])];
  if (hosts.length) take = { ...take, allowed_hosts: hosts };

  let bundle;
  try {
    bundle = await renderTake(take, { library: parseLibrary({}), secrets: job.secrets ?? {},
      baseUrl: job.base_url || undefined, pace });
  } catch (err) {
    return emptyResult("error", elapsed(), scrub(err.message));
  }
  const durationMs = elapsed();

  try {
    const keep = async (file) => {
      try {
        const r = await upload(file);
        return r?.id ? { id: r.id, filename: file.filename } : undefined;
      } catch (err) {
        if (transient(err) || unauthorized(err)) throw err;
        log(tLog("log.job_upload_refused", { kind: file.kind, filename: file.filename, error: scrub(describe(err)) }));
        return undefined;
      }
    };
    const { attachments } = await uploadBundle(bundle, keep, { clean: false });
    const posterFile = await posterOf(bundle, slug(bundle.title));
    const poster = posterFile ? await keep(posterFile) : undefined;
    const report = buildReport({ title: bundle.title, runs: bundle.runs, chapters: bundle.chapters,
      duration: bundle.duration, viewport: bundle.viewport, mode: bundle.mode, attachments, href: artifactHref });
    const judged = judgedRun(bundle.runs);
    return {
      status: runVerdict(bundle.runs),
      checks: judged.verdicts.map((v) => ({
        label: scrub(v.named ? v.label : v.case || v.label), expected: scrub(v.expected), observed: scrub(v.observed ?? ""),
        pass: Boolean(v.pass), measured: Boolean(v.measured),
      })),
      failures: bundle.runs.flatMap((r) => r.failures).map((x) => ({
        step: x.step, kind: x.kind, label: scrub(x.label || ""), error: scrub(x.error),
      })),
      duration_ms: durationMs,
      report_md: scrub(report.body),
      error: "",
      video_id: attachments.video?.id ?? "",
      take_id: attachments.take?.id ?? "",
      chapters_id: attachments.chapters?.id ?? "",
      poster_id: poster?.id ?? "",
      shot_ids: attachments.shots.map((s) => s.annotated?.id).filter(Boolean),
    };
  } finally {
    // The pod's /tmp is a small tmpfs: delete each run's frames and video as soon as they are
    // uploaded.
    await rm(bundle.dir, { recursive: true, force: true }).catch(() => {});
  }
}

/**
 * Work through a batch. Returns the exit code: 0 when the daemon says nothing
 * is left, or when the environment's URL never answered and the daemon was
 * told so; 1 on a fatal error (after trying to post `error` for the run in
 * hand); 2 when the environment is incomplete.
 * @param {{ api?: string, key?: string, batch?: string, client?: ReturnType<typeof runnerClient>,
 *           pace?: number, retryDelays?: number[], log?: (line: string) => void,
 *           reach?: Parameters<typeof waitReachable>[1] }} opts
 */
export async function runJob({ api, key, batch, client, pace = 1, retryDelays, log = (line) => console.log(line),
  reach }) {
  if (!api || !key || !batch) {
    log(tLog("log.job_env_missing"));
    return 2;
  }
  client ??= runnerClient({ api, key, retryDelays, log });
  let current = null;
  let scrub = scrubber([key]);
  let reached = false;
  try {
    for (;;) {
      const job = await client.next(batch);
      if (!job) {
        log(tLog("log.job_done", { batch }));
        return 0;
      }
      if (!job.run?.id) throw new Error(t("errors.job_bad_next"));
      current = job.run;
      scrub = scrubber([key, ...Object.values(job.secrets ?? {})]);
      const url = job.base_url || job.take?.base_url;
      if (!reached && url) {
        const r = await waitReachable(url, reach);
        if (!r.ok) {
          const error = scrub(r.error);
          log(tLog("log.job_unreachable", { url: scrub(url), error }));
          await client.unreachable(batch, error);
          return 0;
        }
      }
      reached = true;
      log(tLog("log.job_run_start", { run: job.run.id, case: job.run.case_key ?? "" }));
      const result = await replayRun(job, {
        upload: (file) => client.artifact(batch, job.run.id, file), pace, scrub, log,
      });
      try {
        await client.result(batch, job.run.id, result);
        log(tLog("log.job_run_done", { run: job.run.id, status: result.status, ms: result.duration_ms }));
      } catch (err) {
        if (transient(err) || unauthorized(err) || !(err instanceof OctopusHttpError)) throw err;
        log(tLog("log.job_result_refused", { run: job.run.id, error: scrub(describe(err)) }));
      }
      current = null;
    }
  } catch (err) {
    const error = scrub(describe(err));
    log(tLog("log.job_fatal", { error }));
    if (current) {
      await client.result(batch, current.id, emptyResult("error", 0, error)).catch(() => {});
    }
    return 1;
  }
}

const isMain = process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url;
if (isMain) {
  process.on("SIGTERM", () => {
    closeBrowser().catch(() => {}).finally(() => process.exit(143));
  });
  const code = await runJob({
    api: process.env.OCTOPUS_API,
    key: process.env.OCTOPUS_API_KEY,
    batch: process.env.EVIDENCE_BATCH_ID,
  });
  await closeBrowser().catch(() => {});
  process.exit(code);
}
