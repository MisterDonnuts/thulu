import { api, ApiError } from "./api";
import { bindModal } from "./modal";
import {
  $,
  calloutHTML,
  confirmDialog,
  esc,
  listRowHTML,
  makeToast,
  setFormError,
  badgeHTML,
  stepListHTML,
  type BadgeTone,
  type ButtonOptions,
  type StepItem,
} from "./ui";
import { t } from "../i18n";

const toast = makeToast(3500);

export type LoginPhase = "" | "preparing" | "waiting_code" | "failed";

export interface EngineLogin {
  engine: string;
  connected: boolean;
  pending: boolean;
  phase?: LoginPhase;
  logged_in?: boolean;
  email?: string;
  org?: string;
  method?: string;
}

export interface LoginFlow {
  phase: LoginPhase;
  url?: string;
  error?: string;
}

export function loginWho(l: EngineLogin): string {
  return [l.email, l.org].filter((s) => !!s).join(" · ");
}

export function loginBadge(l: EngineLogin): { label: string; tone: BadgeTone } {
  if (l.connected) return { label: t("settings.engine_login.connected"), tone: "ok" };
  if (l.pending && l.phase === "preparing")
    return { label: t("settings.engine_login.preparing"), tone: "warn" };
  if (l.pending) return { label: t("settings.engine_login.pending"), tone: "warn" };
  return { label: t("settings.engine_login.not_connected"), tone: "muted" };
}

export function loginMeta(l: EngineLogin, label = l.engine): string {
  if (l.connected) return t("settings.engine_login.meta_connected", { who: loginWho(l) || label });
  if (l.pending && l.phase === "preparing")
    return t("settings.engine_login.meta_preparing", { engine: label });
  if (l.pending) return t("settings.engine_login.meta_pending");
  return t("settings.engine_login.meta_none");
}

// The three steps of the dialog, from what the daemon reports and whether the person already
// opened the sign-in page.
export function loginSteps(
  label: string,
  flow: LoginFlow,
  seconds: number,
  opened: boolean,
): StepItem[] {
  const preparing = flow.phase === "preparing";
  const failed = flow.phase === "failed";
  const ready = flow.phase === "waiting_code";
  return [
    {
      title: t("settings.engine_login.modal.prepare_title", { engine: label }),
      state: failed ? "blocked" : preparing ? "now" : "done",
      detail: preparing ? t("settings.engine_login.modal.preparing", { seconds }) : undefined,
      note: preparing ? t("settings.engine_login.modal.prepare_note") : undefined,
    },
    {
      title: t("settings.engine_login.modal.open_title"),
      state: ready ? (opened ? "done" : "now") : "pending",
      detail: ready ? t("settings.engine_login.modal.open_detail") : undefined,
      action:
        ready && flow.url
          ? {
              label: t("settings.engine_login.modal.open"),
              href: flow.url,
              attrs: { target: "_blank", rel: "noopener noreferrer", "data-el-open": "" }, // i18n-ignore HTML attribute values
            }
          : undefined,
    },
    {
      title: t("settings.engine_login.modal.code_title"),
      state: ready && opened ? "now" : "pending",
    },
  ];
}

function errText(err: unknown, fallback: string): string {
  return err instanceof ApiError && err.message ? err.message : fallback;
}

const path = (engine: string) => `/me/engine-logins/${encodeURIComponent(engine)}`;

const POLL_MS = 1000;

// The open dialog's state. One sign-in at a time: the dialog is modal.
const dlg = {
  engine: "",
  label: "",
  flow: { phase: "" } as LoginFlow,
  opened: false,
  since: 0,
  timer: 0,
  busy: false,
};

let modal: ReturnType<typeof bindModal> | null = null;
let boundTo: HTMLElement | null = null;
let logins: EngineLogin[] = [];
let labels: Record<string, string> = {};
let listFailed = "";

// Settings renders each engine's card with an empty [data-el-slot] for this section; the cards are
// re-rendered whenever a profile changes, so paint fills them again from what was last loaded.
export async function loadEngineLogins(): Promise<void> {
  bind();
  try {
    logins = (await api.get<{ logins: EngineLogin[] }>("/me/engine-logins")).logins;
    listFailed = "";
  } catch (err) {
    logins = [];
    // 404: this install does not offer it (Kubernetes). 403: the caller is an API key, not a
    // person. Either way the section stays out of the cards.
    listFailed =
      err instanceof ApiError && (err.status === 404 || err.status === 403)
        ? ""
        : errText(err, t("settings.engine_login.errors.load"));
  }
  paintEngineLogins();
}

export function paintEngineLogins(engineLabels?: Record<string, string>): void {
  if (engineLabels) labels = engineLabels;
  document.querySelectorAll<HTMLElement>("[data-el-slot]").forEach((slot) => {
    const engine = slot.dataset.elSlot ?? "";
    const l = logins.find((x) => x.engine === engine);
    if (!l) {
      slot.innerHTML =
        listFailed && engine === "claude-code"
          ? `<p class="text-danger el-error">${esc(listFailed)}</p>`
          : "";
      return;
    }
    slot.replaceChildren(...section(l));
  });
}

function labelOf(engine: string): string {
  return labels[engine] || engine;
}

function section(l: EngineLogin): Node[] {
  const label = labelOf(l.engine);
  const badge = loginBadge(l);
  let actions: ButtonOptions[];
  if (l.connected) {
    actions = [
      {
        label: t("settings.engine_login.disconnect"),
        variant: "danger",
        attrs: { "data-el": "disconnect" },
      },
    ];
  } else if (l.pending) {
    actions = [
      {
        label: t("settings.engine_login.resume"),
        variant: "primary",
        attrs: { "data-el": "resume" },
      },
      {
        label: t("settings.engine_login.cancel"),
        variant: "ghost",
        attrs: { "data-el": "cancel" },
      },
    ];
  } else {
    actions = [{ label: t("settings.engine_login.connect"), attrs: { "data-el": "connect" } }];
  }
  const tpl = document.createElement("template");
  tpl.innerHTML =
    `<h3 class="sub-head">${esc(t("settings.engine_login.title"))}</h3>` +
    `<p class="muted el-lede">${esc(t("settings.engine_login.lede", { engine: label }))}</p>` +
    listRowHTML({
      boxed: true,
      title: t("settings.engine_login.row_title", { engine: label }),
      badgesHTML: badgeHTML(badge.label, badge.tone, {
        icon: l.pending && l.phase === "preparing" ? "refresh" : undefined,
        spin: l.pending && l.phase === "preparing",
      }),
      meta: loginMeta(l, label),
      actions,
    }) +
    (l.connected
      ? ""
      : calloutHTML({
          tone: "warn",
          icon: "alert",
          body: t("settings.engine_login.warning"),
          cls: "el-warning",
        }));
  const nodes = [...tpl.content.childNodes];
  for (const n of nodes) {
    if (!(n instanceof HTMLElement)) continue;
    n.querySelectorAll<HTMLButtonElement>("[data-el]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const act = btn.dataset.el;
        if (act === "connect") void begin(l.engine, btn);
        if (act === "resume")
          openDialog(l.engine, {
            phase: l.phase === "waiting_code" ? "waiting_code" : "preparing",
          });
        if (act === "cancel") void cancel(l.engine, btn);
        if (act === "disconnect") void disconnect(l.engine, btn);
      });
    });
  }
  return nodes;
}

// The modal markup is rendered once per Settings visit, so rebind when it changes.
function bind(): void {
  const overlay = $("el-modal");
  if (boundTo === overlay) return;
  boundTo = overlay;
  modal = bindModal(overlay, {
    onClose: () => {
      const engine = dlg.engine;
      stopPolling();
      dlg.engine = "";
      // Closing the dialog gives up the sign-in; a reload does not, and the card offers to resume.
      if (engine)
        void api
          .post(`${path(engine)}/cancel`)
          .catch(() => undefined)
          .then(loadEngineLogins);
    },
  });
  $("el-form").addEventListener("submit", (e) => {
    e.preventDefault();
    if (dlg.flow.phase === "failed") void restart();
    else if (dlg.flow.phase === "waiting_code") void complete();
  });
  // Someone may copy the link another way; reaching for the code field is enough.
  $("el-code").addEventListener("focus", () => {
    if (dlg.opened || dlg.flow.phase !== "waiting_code") return;
    dlg.opened = true;
    render();
  });
  $("el-steps").addEventListener("click", (e) => {
    if (!(e.target as HTMLElement).closest("[data-el-open]")) return;
    dlg.opened = true;
    render();
    ($("el-code") as HTMLInputElement).focus();
  });
}

async function begin(engine: string, btn: HTMLButtonElement): Promise<void> {
  btn.disabled = true;
  try {
    const flow = await api.post<LoginFlow>(path(engine));
    openDialog(engine, flow);
  } catch (err) {
    toast(errText(err, t("settings.engine_login.errors.start")), "err");
  } finally {
    btn.disabled = false;
    void loadEngineLogins();
  }
}

async function restart(): Promise<void> {
  const engine = dlg.engine;
  if (!engine) return;
  setBusy(true);
  try {
    const flow = await api.post<LoginFlow>(path(engine));
    dlg.flow = flow;
    dlg.opened = false;
    dlg.since = Date.now();
    setFormError($("el-error"), "");
    render();
    poll();
  } catch (err) {
    setFormError($("el-error"), errText(err, t("settings.engine_login.errors.start")));
  } finally {
    setBusy(false);
  }
}

function openDialog(engine: string, flow: LoginFlow): void {
  bind();
  dlg.engine = engine;
  dlg.label = labelOf(engine);
  dlg.flow = flow;
  dlg.opened = false;
  dlg.since = Date.now();
  $("el-modal-title").textContent = t("settings.engine_login.modal.title", { engine: dlg.label });
  const code = $("el-code") as HTMLInputElement;
  code.value = "";
  setFormError($("el-error"), "");
  render();
  modal?.open($("el-modal").querySelector<HTMLElement>("[data-modal-close]"));
  poll();
}

function poll(): void {
  stopPolling();
  if (!dlg.engine || dlg.flow.phase === "failed") return;
  const engine = dlg.engine;
  const tick = async () => {
    if (dlg.engine !== engine) return;
    try {
      const flow = await api.get<LoginFlow>(`${path(engine)}/flow`);
      if (dlg.engine !== engine) return;
      if (flow.phase === "") {
        // Closed elsewhere: cancelled from another tab, or the 10 minutes ran out.
        dlg.flow = { phase: "failed", error: t("settings.engine_login.errors.lost") };
      } else {
        dlg.flow = flow;
      }
    } catch (err) {
      dlg.flow = { phase: "failed", error: errText(err, t("settings.engine_login.errors.lost")) };
    }
    render();
    if (dlg.flow.phase === "preparing") dlg.timer = window.setTimeout(() => void tick(), POLL_MS);
    else void loadEngineLogins(); // the card behind the dialog still says "Preparing…"
  };
  if (dlg.flow.phase === "preparing") {
    // Each poll also redraws the elapsed seconds.
    dlg.timer = window.setTimeout(() => void tick(), POLL_MS);
  } else if (dlg.flow.phase === "waiting_code" && !dlg.flow.url) {
    void tick(); // resumed after a reload: fetch the link
  }
}

function stopPolling(): void {
  window.clearTimeout(dlg.timer);
  dlg.timer = 0;
}

function render(): void {
  const seconds = Math.max(0, Math.round((Date.now() - dlg.since) / 1000));
  const steps = $("el-steps");
  steps.innerHTML = stepListHTML(loginSteps(dlg.label, dlg.flow, seconds, dlg.opened));
  const ready = dlg.flow.phase === "waiting_code";
  const code = $("el-code") as HTMLInputElement;
  code.disabled = !ready;
  $("el-code-field").hidden = !ready;
  const submit = $("el-submit") as HTMLButtonElement;
  submit.textContent =
    dlg.flow.phase === "failed"
      ? t("settings.engine_login.modal.retry")
      : t("settings.engine_login.modal.submit");
  submit.disabled = dlg.busy || dlg.flow.phase === "preparing";
  if (dlg.flow.phase === "failed")
    setFormError($("el-error"), dlg.flow.error ?? t("settings.engine_login.errors.start"));
}

function setBusy(on: boolean): void {
  dlg.busy = on;
  const submit = $("el-submit") as HTMLButtonElement;
  submit.disabled = on;
  submit.setAttribute("aria-busy", String(on));
  if (on && dlg.flow.phase === "waiting_code")
    submit.textContent = t("settings.engine_login.modal.verifying");
  if (!on) render();
}

async function complete(): Promise<void> {
  const engine = dlg.engine;
  if (!engine || dlg.busy) return;
  const code = $("el-code") as HTMLInputElement;
  setFormError($("el-error"), "");
  setBusy(true);
  try {
    const st = await api.post<EngineLogin>(`${path(engine)}/code`, { code: code.value });
    code.value = "";
    dlg.engine = ""; // clear before close(): onClose cancels an open sign-in
    stopPolling();
    modal?.close(true);
    toast(t("settings.engine_login.toast.connected", { who: loginWho(st) || dlg.label }), "ok");
  } catch (err) {
    code.value = "";
    // 409: the sign-in closed under us (cancelled, timed out, the CLI exited). Start over.
    if (err instanceof ApiError && err.status === 409) {
      dlg.flow = { phase: "failed", error: errText(err, t("settings.engine_login.errors.lost")) };
    } else {
      setFormError($("el-error"), errText(err, t("settings.engine_login.errors.complete")));
    }
  } finally {
    setBusy(false);
    if (dlg.engine) code.focus();
    void loadEngineLogins();
  }
}

async function cancel(engine: string, btn: HTMLButtonElement): Promise<void> {
  btn.disabled = true;
  try {
    await api.post(`${path(engine)}/cancel`);
  } catch (err) {
    toast(errText(err, t("settings.engine_login.errors.cancel")), "err");
  } finally {
    void loadEngineLogins();
  }
}

async function disconnect(engine: string, btn: HTMLButtonElement): Promise<void> {
  const label = labelOf(engine);
  const ok = await confirmDialog({
    title: t("settings.engine_login.confirm.title", { engine: label }),
    message: t("settings.engine_login.confirm.message"),
    confirmLabel: t("settings.engine_login.confirm.button"),
    danger: true,
  });
  if (!ok) return;
  btn.disabled = true;
  try {
    await api.del(path(engine));
    toast(t("settings.engine_login.toast.disconnected"), "ok");
  } catch (err) {
    toast(errText(err, t("settings.engine_login.errors.disconnect")), "err");
  } finally {
    void loadEngineLogins();
  }
}
