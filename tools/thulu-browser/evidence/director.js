import { t } from "../i18n.js";
import { judge } from "./checks.js";
import { cinemaCall, drawAnnotation, installCinema, removeAnnotation } from "./overlays.js";

const TIMING = {
  travel: 460, // Must match the .cursor transform transition in overlays.js (.45s).
  beforeAct: 180,
  afterAct: 380,
  afterNav: 450,
  shotHold: 2_200,
  verdictHold: 1_800,
  noteHold: 1_400,
  scroll: 650,
};
const ACTION_TIMEOUT_MS = 15_000;
const NAV_TIMEOUT_MS = 45_000;
const SETTLE_TIMEOUT_MS = 8_000;
const SPOT_WAIT_MS = 3_000;

export class GuardError extends Error {}

const PAUSES = new Set(["beat", "hold"]);

const SET_WHOLE = new Set(["date", "time", "datetime-local", "month", "week", "color", "range"]);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export function autoLabel(step) {
  const what = describeTarget(step.target);
  switch (step.kind) {
    case "goto": return t("text.auto_open", { url: step.url });
    case "click": return t("text.auto_click", { target: what });
    case "fill": return t("text.auto_fill", { target: what });
    case "type": return t("text.auto_type", { target: what });
    case "select": return t("text.auto_select", { value: step.value, target: what });
    case "check":
      return step.checked === false ? t("text.auto_uncheck", { target: what }) : t("text.auto_check", { target: what });
    case "hover": return t("text.auto_hover", { target: what });
    case "press": return t("text.auto_press", { key: step.key });
    case "scroll":
      return step.target ? t("text.auto_scroll_to", { target: what }) : t("text.auto_scroll", { direction: step.direction });
    case "shot": return step.shot.title || step.shot.name;
    case "verify": return t("text.auto_verify", { case: step.case });
    case "note": return step.text;
    default: return "";
  }
}

function describeTarget(target = "") {
  const m = /name="((?:[^"\\]|\\.)*)"/.exec(target) || /^text="((?:[^"\\]|\\.)*)"$/.exec(target);
  if (m) return `"${m[1]}"`;
  return target.length > 48 ? `${target.slice(0, 45)}…` : target;
}

export class Director {
  #page;
  #capture;
  #pace;
  #baseUrl;
  #secrets;
  #allowedHosts;
  #violation = null;
  #guarding = null;
  #chapter = null;
  #cursor = null;
  #responses = [];
  #prevStepStart = 0;
  #stepStart = 0;

  shots = [];
  verdicts = [];
  notes = [];

  /**
   * @param {import('playwright').Page} page
   * @param {{ capture: import('./capture.js').FrameCapture | import('./capture.js').NoCapture,
   *           pace?: number, baseUrl?: string, secrets?: Record<string,string>,
   *           allowedHosts?: string[] | null }} opts
   */
  constructor(page, opts) {
    this.#page = page;
    this.#capture = opts.capture;
    this.#pace = opts.pace ?? 1;
    this.#baseUrl = opts.baseUrl;
    this.#secrets = opts.secrets ?? {};
    this.#allowedHosts = opts.allowedHosts ? new Set(opts.allowedHosts.map((h) => h.toLowerCase())) : null;

    page.on("response", (r) => {
      this.#responses.push({ url: r.url(), t: Date.now() });
      if (this.#responses.length > 500) this.#responses.splice(0, 250);
    });
    if (this.#allowedHosts) {
      // Main-frame navigations to other hosts are aborted before the request leaves, so a replay
      // cannot reach a real system.
      // Server-side redirects bypass page.route, so framenavigated is the backstop for those.
      this.#guarding = page.route("**/*", (route) => this.#guard(route));
      page.on("framenavigated", (frame) => {
        if (frame !== page.mainFrame()) return;
        if (!this.#hostAllowed(frame.url())) this.#violation = frame.url();
      });
    }
  }

  #guard(route) {
    const req = route.request();
    let mainFrame = false;
    try { mainFrame = req.isNavigationRequest() && req.frame() === this.#page.mainFrame(); } catch {}
    if (mainFrame && !this.#hostAllowed(req.url())) {
      this.#violation = req.url();
      return route.abort("blockedbyclient");
    }
    return route.fallback();
  }

  static async prepare(page, { redactCss = [], badge = "" } = {}) {
    const opts = { redactCss, badge };
    await page.context().addInitScript(installCinema, opts);
    await page.evaluate(installCinema, opts).catch(() => {});
    if (badge) await page.evaluate(cinemaCall, ["badge", [badge]]).catch(() => {});
  }

  #hostAllowed(url) {
    let u;
    try { u = new URL(url); } catch { return true; }
    if (!["http:", "https:"].includes(u.protocol)) return true;
    return this.#allowedHosts.has(u.host.toLowerCase()) || this.#allowedHosts.has(u.hostname.toLowerCase());
  }

  #ms(base) {
    return Math.round(base * this.#pace);
  }

  async #cinema(method, ...args) {
    await this.#page.evaluate(cinemaCall, [method, args]).catch(() => {});
  }

  async #hold(base) {
    await sleep(this.#ms(base));
  }

  #loc(target) {
    return this.#page.locator(target).first();
  }

  async #settle() {
    await this.#capture.idle(() =>
      this.#page.waitForLoadState("networkidle", { timeout: SETTLE_TIMEOUT_MS }).catch(() => {}));
  }

  async #approach(target) {
    const loc = this.#loc(target);
    await this.#capture.idle(async () => {
      await loc.waitFor({ state: "visible", timeout: ACTION_TIMEOUT_MS });
      await loc.scrollIntoViewIfNeeded({ timeout: ACTION_TIMEOUT_MS }).catch(() => {});
    });
    const box = await loc.boundingBox();
    if (!box) return null;
    const point = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
    await this.#cinema("moveTo", point.x, point.y, this.#cursor);
    await this.#cinema("highlight", box);
    this.#cursor = point;
    await this.#hold(TIMING.travel);
    return point;
  }

  async perform(step, index) {
    await this.#guarding;
    if (this.#violation) throw new GuardError(t("errors.navigation_left_hosts", { url: this.#violation }));
    this.#prevStepStart = this.#stepStart;
    this.#stepStart = Date.now();
    let label = step.label || autoLabel(step);
    if (PAUSES.has(step.kind) && !step.label && this.#chapter) ({ index, label } = this.#chapter);
    else this.#chapter = { index, label };
    if (label) await this.#cinema("caption", label);
    const film = (fn) => this.#capture.film(index, label, fn);

    let produced;
    switch (step.kind) {
      case "goto": {
        const url = new URL(step.url, this.#baseUrl || this.#page.url()).toString();
        if (this.#allowedHosts && !this.#hostAllowed(url)) throw new GuardError(t("errors.goto_outside_hosts", { url }));
        await this.#capture.idle(async () => {
          await this.#page.goto(url, { waitUntil: "domcontentloaded", timeout: NAV_TIMEOUT_MS });
          await this.#page.waitForLoadState("networkidle", { timeout: SETTLE_TIMEOUT_MS }).catch(() => {});
          if (step.wait_for) await this.#loc(step.wait_for).waitFor({ state: "visible", timeout: NAV_TIMEOUT_MS });
        });
        this.#cursor = null;
        if (label) await this.#cinema("caption", label);
        await film(() => this.#hold(TIMING.afterNav));
        break;
      }
      case "click":
      case "check":
      case "select":
      case "hover": {
        await film(async () => {
          const point = await this.#approach(step.target);
          await this.#hold(TIMING.beforeAct);
          if (point && step.kind !== "hover") await this.#cinema("pulse", point.x, point.y);
          await this.#act(step);
          await this.#hold(TIMING.afterAct / 2);
          await this.#cinema("clear");
        });
        await this.#settle();
        await film(() => this.#hold(TIMING.afterAct));
        break;
      }
      case "fill":
      case "type": {
        await film(async () => {
          await this.#approach(step.target);
          await this.#typeInto(step);
          await this.#cinema("clear");
          await this.#hold(TIMING.afterAct);
        });
        break;
      }
      case "press": {
        await film(async () => {
          if (step.target) {
            await this.#approach(step.target);
            await this.#loc(step.target).press(step.key, { timeout: ACTION_TIMEOUT_MS });
            await this.#cinema("clear");
          } else {
            await this.#page.keyboard.press(step.key);
          }
        });
        if (step.key === "Enter") await this.#settle();
        await film(() => this.#hold(TIMING.afterAct));
        break;
      }
      case "scroll": {
        await film(async () => {
          if (step.target) {
            await this.#loc(step.target).scrollIntoViewIfNeeded({ timeout: ACTION_TIMEOUT_MS });
          } else {
            await this.#page.evaluate(({ direction, amount }) => {
              const by = { down: amount, up: -amount }[direction];
              if (by !== undefined) window.scrollBy({ top: by, behavior: "smooth" });
              else window.scrollTo({ top: direction === "top" ? 0 : document.body.scrollHeight, behavior: "smooth" });
            }, { direction: step.direction, amount: step.amount ?? 500 });
          }
          await this.#hold(TIMING.scroll);
        });
        break;
      }
      case "wait_for":
        await this.#capture.idle(() => this.#loc(step.target).waitFor({ state: step.state ?? "visible",
          timeout: step.timeout_ms ?? ACTION_TIMEOUT_MS }));
        break;
      case "wait_for_response":
        await this.#capture.idle(() => this.#waitForResponse(step));
        break;
      case "hold":
        await this.#capture.idle(() => sleep(step.ms));
        break;
      case "beat":
        await film(async () => {
          await this.#cinema("beat", this.#ms(step.ms));
          await this.#hold(step.ms);
        });
        break;
      case "viewport":
        await this.#page.setViewportSize({ width: step.width, height: step.height });
        await film(() => this.#hold(TIMING.afterNav));
        break;
      case "shot":
        produced = await this.#shot(step, index, label);
        break;
      case "verify":
        produced = await this.#verify(step, index, label);
        break;
      case "note":
        this.notes.push({ step: index, level: step.level ?? "step", text: step.text });
        await film(async () => {
          await this.#cinema("beat", this.#ms(TIMING.noteHold));
          await this.#hold(TIMING.noteHold);
        });
        break;
      case "script":
        await this.#capture.idle(() => this.#page.evaluate(step.code));
        break;
      default:
        throw new Error(t("errors.step_not_playable", { kind: step.kind }));
    }
    if (this.#violation) throw new GuardError(t("errors.navigation_left_hosts", { url: this.#violation }));
    return produced;
  }

  async #act(step) {
    const loc = this.#loc(step.target);
    const opts = { timeout: ACTION_TIMEOUT_MS };
    if (step.kind === "click") return loc.click(opts);
    if (step.kind === "hover") return loc.hover(opts);
    if (step.kind === "select") return loc.selectOption(step.value, opts);
    return step.checked === false ? loc.uncheck(opts) : loc.check(opts);
  }

  async #typeInto(step) {
    const loc = this.#loc(step.target);
    const value = step.kind === "fill" && step.secret ? this.#secret(step.secret) : step.value;
    // Date, time and color inputs are set whole: typed keys land segment by segment and leave
    // invalid values.
    const inputType = await loc.evaluate((el) => (el instanceof HTMLInputElement ? el.type : "")).catch(() => "");
    if (SET_WHOLE.has(inputType)) {
      await loc.fill(value, { timeout: ACTION_TIMEOUT_MS });
      return;
    }
    const delay = step.delay_ms ?? Math.max(8, Math.min(55, Math.floor(1_500 / Math.max(1, value.length))));
    if (step.kind === "fill") await loc.fill("", { timeout: ACTION_TIMEOUT_MS });
    else if (step.click !== false) await loc.click({ timeout: ACTION_TIMEOUT_MS });
    await loc.pressSequentially(value, { delay: this.#ms(delay), timeout: ACTION_TIMEOUT_MS });
  }

  #secret(name) {
    if (!Object.hasOwn(this.#secrets, name)) throw new Error(t("errors.secret_missing", { name }));
    return this.#secrets[name];
  }

  // The response may have arrived during the previous step (the click that caused it), so count
  // anything since that step began.
  async #waitForResponse(step) {
    const seen = this.#responses.some((r) => r.t >= this.#prevStepStart && r.url.includes(step.url_part));
    if (seen) return;
    await this.#page.waitForResponse((r) => r.url().includes(step.url_part), {
      timeout: step.timeout_ms ?? ACTION_TIMEOUT_MS,
    });
  }

  async #boxes(targets, all) {
    const out = [];
    for (const t of targets) {
      const locs = all ? await this.#page.locator(t).all().catch(() => []) : [this.#loc(t)];
      for (const l of locs.slice(0, 20)) {
        const b = await l.boundingBox().catch(() => null);
        if (b) out.push(b);
      }
    }
    return out;
  }

  async #shot(step, index, label) {
    const spec = step.shot;
    const spots = spec.spotlight ?? [];
    const missing = [];
    await this.#capture.idle(async () => {
      for (const s of spots) {
        await this.#loc(s.target).waitFor({ state: "visible", timeout: spec.wait_ms ?? SPOT_WAIT_MS })
          .catch(() => missing.push(s.target));
      }
      const first = spots.find((s) => !missing.includes(s.target));
      if (first) await this.#loc(first.target).scrollIntoViewIfNeeded().catch(() => {});
    });

    const regions = [];
    for (const s of spots) {
      if (missing.includes(s.target)) continue;
      const [box] = await this.#boxes([s.target], false);
      if (box) regions.push({ box, note: s.note, tone: s.tone ?? "focus" });
      else missing.push(s.target);
    }
    const redactions = await this.#boxes(spec.redact ?? [], true);
    const context = [new URL(this.#page.url()).host, new Date().toISOString().slice(0, 16).replace("T", " ")];
    const fullPage = Boolean(spec.full_page);

    await this.#cinema("hide");
    let clean;
    try {
      if (spec.keep_clean) {
        await this.#page.evaluate(drawAnnotation, { regions: [], redactions, context: [], notes: [], dim: false });
        clean = await this.#page.screenshot({ type: "png", fullPage });
      }
      const drawn = await this.#page.evaluate(drawAnnotation, {
        regions, redactions, title: spec.title ?? spec.name, context, notes: spec.notes ?? [],
        dim: spec.dim ?? regions.length > 0,
      });
      // The camera must have the annotated frame before the screenshot, or the video holds the
      // pre-annotation frame.
      await this.#capture.fresh();
      const png = await this.#page.screenshot({ type: "png", fullPage });
      await this.#capture.film(index, label, () => this.#hold(TIMING.shotHold));
      const shot = { step: index, name: spec.name, title: spec.title ?? spec.name, png, clean,
        url: this.#page.url(), missing, crowded: Boolean(drawn?.crowded), notes: spec.notes ?? [],
        spots: spots.map((s) => s.note).filter(Boolean) };
      this.shots.push(shot);
      return shot;
    } finally {
      await this.#page.evaluate(removeAnnotation).catch(() => {});
      await this.#cinema("show");
    }
  }

  async #verify(step, index, label) {
    const { observed, pass, measured } = await this.#capture.idle(() => judge(this.#page, step));
    const verdict = { step: index, case: step.case, label, named: Boolean(step.label), expected: step.expected,
      observed, pass, measured, detail: step.detail };
    this.verdicts.push(verdict);
    await this.#capture.film(index, label, async () => {
      await this.#cinema("verdict", {
        pass,
        title: `${step.case} · ${pass ? t("labels.pass") : t("labels.fail")}`,
        rows: [[t("labels.what"), label], [t("labels.expected"), step.expected], [t("labels.observed"), observed]],
      });
      await this.#hold(TIMING.verdictHold);
      await this.#cinema("unverdict");
    });
    return verdict;
  }
}

