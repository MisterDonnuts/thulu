import { ApiError } from "./api";
import {
  canInvite,
  createLockText,
  createWorkspace,
  listMemberships,
  roleLabel,
  switchTarget,
  switchWorkspace,
  workspaceColor,
  workspaceInitial,
  type Membership,
} from "./account";
import { hasLoginSession, sessionInfo } from "./auth";
import { bindModal } from "./modal";
import { bindMenu, esc, makeToast, menuItemHTML } from "./ui";
import { t } from "../i18n";

const NAME_KEY = "octopus.workspace.name";

const toast = makeToast();

function readCached(): { name: string; id: string } | null {
  try {
    const raw = localStorage.getItem(NAME_KEY);
    const v = raw ? (JSON.parse(raw) as { name?: unknown; id?: unknown }) : null;
    return v && typeof v.name === "string" && typeof v.id === "string"
      ? { name: v.name, id: v.id }
      : null;
  } catch {
    return null;
  }
}

function writeCached(m: Membership): void {
  try {
    localStorage.setItem(NAME_KEY, JSON.stringify({ name: m.name, id: m.id }));
  } catch {
    // private window or blocked storage: deliberately ignored
  }
}

function paintMark(el: HTMLElement, name: string, id: string): void {
  el.textContent = workspaceInitial(name);
  el.style.setProperty("--ws-color", workspaceColor(id));
}

export function workspaceItemHTML(m: Membership): string {
  const mark =
    `<span class="sb-ws-mark" aria-hidden="true" style="--ws-color:${esc(workspaceColor(m.id))}">` +
    `${esc(workspaceInitial(m.name))}</span>`;
  return menuItemHTML({
    title: m.name,
    sub: roleLabel(m.role),
    leadHTML: mark,
    checked: m.current,
    attrs: { "data-ws": m.id, title: m.name },
  });
}

export function initWorkspaceMenu(): void {
  // A CSRF cookie left by an earlier login does not mean logins are on: asking
  // /auth/workspaces on a no-login install only logged a 401.
  void hasLoginSession().then((has) => {
    if (has) startWorkspaceMenu();
  });
}

function startWorkspaceMenu(): void {
  const wrap = document.getElementById("sb-ws");
  const staticLabel = document.getElementById("sb-ws-static");
  const btn = document.getElementById("sb-ws-btn") as HTMLButtonElement | null;
  const menu = document.getElementById("sb-ws-menu");
  const head = document.getElementById("sb-ws-head");
  const nameEl = document.getElementById("sb-ws-name");
  const markEl = document.getElementById("sb-ws-mark");
  const invite = document.getElementById("sb-ws-invite");
  const create = document.getElementById("sb-ws-create");
  const hint = document.getElementById("sb-ws-create-hint");
  if (!wrap || !staticLabel || !btn || !menu || !head || !nameEl || !markEl || !invite || !create)
    return;
  const list = document.createElement("div");
  list.className = "sb-ws-list";
  list.setAttribute("role", "group");
  head.after(list);

  const showMenu = (name: string, id: string) => {
    nameEl.textContent = name;
    paintMark(markEl, name, id);
    btn.setAttribute("aria-label", t("layout.workspaces.current", { name }));
    staticLabel.hidden = true;
    wrap.hidden = false;
  };
  const cached = readCached();
  if (cached) showMenu(cached.name, cached.id);

  const openCreate = bindCreateDialog(btn);
  // bindMenu never runs an aria-disabled item, so onSelect needs no check.
  create.setAttribute("aria-disabled", "true");
  if (hint) hint.hidden = true;
  let memberships: Membership[] = [];
  bindMenu(menu, btn, {
    focus: "checked",
    onSelect: (item) => {
      if (item === create) openCreate?.();
      else if (item.dataset.ws) void pick(item.dataset.ws, memberships, btn);
    },
  });

  void (async () => {
    try {
      memberships = await listMemberships();
    } catch {
      return;
    }
    const current = memberships.find((m) => m.current) ?? memberships.at(0);
    if (!current) return;
    showMenu(current.name, current.id);
    writeCached(current);
    list.innerHTML = memberships.map(workspaceItemHTML).join("");
    invite.hidden = !canInvite(current.role);

    const me = await sessionInfo();
    const mayCreate = me?.can_create_workspace === true;
    create.setAttribute("aria-disabled", String(!mayCreate));
    if (hint) {
      hint.textContent = me ? createLockText(me) : t("layout.workspaces.create_unavailable");
      hint.hidden = mayCreate;
    }
  })();
}

async function pick(id: string, memberships: Membership[], btn: HTMLButtonElement): Promise<void> {
  const m = memberships.find((x) => x.id === id);
  if (!m || m.current) return;
  btn.setAttribute("aria-busy", "true");
  try {
    await switchWorkspace(m.id);
    window.location.href = switchTarget(window.location.pathname);
  } catch (err) {
    btn.removeAttribute("aria-busy");
    toast(
      err instanceof Error && err.message ? err.message : t("layout.workspaces.switch_failed"),
      "err",
    );
  }
}

function bindCreateDialog(btn: HTMLButtonElement): (() => void) | null {
  const overlay = document.getElementById("ws-create-modal");
  const form = document.getElementById("ws-create-form") as HTMLFormElement | null;
  const input = document.getElementById("ws-create-name") as HTMLInputElement | null;
  const error = document.getElementById("ws-create-error");
  const submit = document.getElementById("ws-create-submit") as HTMLButtonElement | null;
  if (!overlay || !form || !input || !error || !submit) return null;
  const submitLabel = submit.textContent;

  const showError = (msg: string) => {
    error.textContent = msg;
    error.hidden = msg === "";
    input.setAttribute("aria-invalid", String(msg !== ""));
  };

  // The dialog sits outside any page's keyboard cascade, so it captures Esc ahead of the page
  // (the confirm dialog in Layout.astro does the same).
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopImmediatePropagation();
    modal.close();
  };
  const modal = bindModal(overlay, {
    onClose: () => {
      document.removeEventListener("keydown", onKey, true);
      btn.focus();
    },
  });

  input.addEventListener("input", () => {
    if (!error.hidden) showError("");
  });

  const onSubmit = async () => {
    const name = input.value.trim();
    if (!name) {
      showError(t("layout.workspaces.name_required"));
      input.focus();
      return;
    }
    submit.disabled = true;
    submit.textContent = t("layout.workspaces.creating");
    let created: Membership;
    try {
      created = await createWorkspace(name);
    } catch (err) {
      showError(
        err instanceof ApiError && err.message ? err.message : t("layout.workspaces.create_failed"),
      );
      submit.disabled = false;
      submit.textContent = submitLabel;
      input.focus();
      return;
    }
    try {
      await switchWorkspace(created.id);
      window.location.href = "/";
    } catch {
      window.location.reload();
    }
  };
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void onSubmit();
  });

  return () => {
    form.reset();
    showError("");
    submit.disabled = false;
    submit.textContent = submitLabel;
    document.addEventListener("keydown", onKey, true);
    modal.open(input);
  };
}
