import { api, ApiError } from "./api";
import { bindModal } from "./modal";
import {
  $,
  confirmDialog,
  emptyRowHTML,
  listRowHTML,
  makeToast,
  setFormError,
  badgeHTML,
  type BadgeTone,
} from "./ui";
import { t } from "../i18n";

const toast = makeToast(3500);

export interface EngineLogin {
  engine: string;
  connected: boolean;
  pending: boolean;
  logged_in?: boolean;
  email?: string;
  org?: string;
  method?: string;
}

export function loginWho(l: EngineLogin): string {
  return [l.email, l.org].filter((s) => !!s).join(" · ");
}

export function loginBadge(l: EngineLogin): { label: string; tone: BadgeTone } {
  if (l.connected) return { label: t("settings.engine_login.connected"), tone: "ok" };
  if (l.pending) return { label: t("settings.engine_login.pending"), tone: "warn" };
  return { label: t("settings.engine_login.not_connected"), tone: "muted" };
}

export function loginMeta(l: EngineLogin): string {
  if (!l.connected) return t("settings.engine_login.meta_none");
  return t("settings.engine_login.meta_connected", { who: loginWho(l) || l.engine });
}

function errText(err: unknown, fallback: string): string {
  return err instanceof ApiError && err.message ? err.message : fallback;
}

const path = (engine: string) => `/me/engine-logins/${encodeURIComponent(engine)}`;

let signingIn = "";
let modal: ReturnType<typeof bindModal> | null = null;
let boundTo: HTMLElement | null = null;

export async function loadEngineLogins(): Promise<void> {
  const panel = $("el-panel");
  const list = $("el-list");
  bind(panel);
  let logins: EngineLogin[];
  try {
    logins = (await api.get<{ logins: EngineLogin[] }>("/me/engine-logins")).logins;
  } catch (err) {
    // 404: this install does not offer it (Kubernetes). 403: the caller is an API key, not a
    // person.
    if (err instanceof ApiError && (err.status === 404 || err.status === 403)) {
      panel.hidden = true;
      return;
    }
    panel.hidden = false;
    list.innerHTML = emptyRowHTML(errText(err, t("settings.engine_login.errors.load")), {
      as: "div",
      tone: "danger",
    });
    return;
  }
  panel.hidden = logins.length === 0;
  list.replaceChildren(...logins.map(row));
}

function row(l: EngineLogin): HTMLElement {
  const badge = loginBadge(l);
  const tpl = document.createElement("template");
  tpl.innerHTML = listRowHTML({
    title: l.engine,
    mono: true,
    badgesHTML: badgeHTML(badge.label, badge.tone),
    meta: loginMeta(l),
    actions: [
      l.connected
        ? {
            label: t("settings.engine_login.disconnect"),
            variant: "danger",
            attrs: { "data-act": "disconnect" },
          }
        : {
            label: t("settings.engine_login.connect"),
            variant: "primary",
            attrs: { "data-act": "connect" },
          },
    ],
  });
  const el = tpl.content.firstElementChild as HTMLElement;
  el.querySelector<HTMLButtonElement>('[data-act="connect"]')?.addEventListener(
    "click",
    (e) => void connect(l.engine, e.currentTarget as HTMLButtonElement),
  );
  el.querySelector<HTMLButtonElement>('[data-act="disconnect"]')?.addEventListener(
    "click",
    (e) => void disconnect(l.engine, e.currentTarget as HTMLButtonElement),
  );
  return el;
}

// Settings renders fresh markup on every visit, so rebind when the panel element changes.
function bind(panel: HTMLElement): void {
  if (boundTo === panel) return;
  boundTo = panel;
  modal = bindModal($("el-modal"), {
    onClose: () => {
      const engine = signingIn;
      signingIn = "";
      if (engine) void api.post(`${path(engine)}/cancel`).catch(() => undefined);
    },
  });
  $("el-form").addEventListener("submit", (e) => {
    e.preventDefault();
    void complete();
  });
}

async function connect(engine: string, btn: HTMLButtonElement): Promise<void> {
  btn.disabled = true;
  try {
    const { url } = await api.post<{ url: string }>(path(engine));
    signingIn = engine;
    $("el-modal-title").textContent = t("settings.engine_login.modal.title", { engine });
    const link = $("el-url") as HTMLAnchorElement;
    link.href = url;
    const code = $("el-code") as HTMLInputElement;
    code.value = "";
    setFormError($("el-error"), "");
    modal?.open(code);
  } catch (err) {
    toast(errText(err, t("settings.engine_login.errors.start")), "err");
  } finally {
    btn.disabled = false;
  }
}

async function complete(): Promise<void> {
  const engine = signingIn;
  if (!engine) return;
  const code = $("el-code") as HTMLInputElement;
  const submit = $("el-submit") as HTMLButtonElement;
  submit.disabled = true;
  try {
    const st = await api.post<EngineLogin>(`${path(engine)}/code`, { code: code.value });
    code.value = "";
    signingIn = ""; // clear before close(): onClose cancels a pending sign-in
    modal?.close(true);
    toast(t("settings.engine_login.toast.connected", { who: loginWho(st) || engine }), "ok");
  } catch (err) {
    code.value = "";
    setFormError($("el-error"), errText(err, t("settings.engine_login.errors.complete")));
  } finally {
    submit.disabled = false;
    void loadEngineLogins();
  }
}

async function disconnect(engine: string, btn: HTMLButtonElement): Promise<void> {
  const ok = await confirmDialog({
    title: t("settings.engine_login.confirm.title", { engine }),
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
