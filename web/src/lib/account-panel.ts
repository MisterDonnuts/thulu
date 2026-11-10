import {
  blockingWorkspaces,
  deleteAccount,
  deleteWorkspace,
  emailConfirmed,
  identityProviderNames,
  listMembers,
  listMemberships,
  memberLabel,
  renameWorkspace,
  roleLabel,
  slugConfirmed,
  switchWorkspace,
  transferCandidates,
  transferOwnership,
  workspaceActions,
  workspaceMeta,
  workspaceNameProblem,
  type Membership,
  type WorkspaceAction,
} from "./account";
import { ApiError } from "./api";
import { apiBase, sessionInfo } from "./auth";
import { bindModal } from "./modal";
import {
  $,
  confirmDialog,
  emptyRowHTML,
  listRowHTML,
  makeToast,
  setFormError,
  type ButtonVariant,
} from "./ui";
import { t } from "../i18n";

const toast = makeToast(3500);

interface Me {
  id: string;
  email: string;
}

let me: Me | null = null;
let boundTo: HTMLElement | null = null;
let transferTarget: Membership | null = null;
let renameTarget: Membership | null = null;

function errText(err: unknown, fallback: string): string {
  return err instanceof ApiError && err.message ? err.message : fallback;
}

function showError(id: string, msg: string): void {
  setFormError($(id), msg);
}

const modals = {
  transfer: () => bindOnce("acct-transfer-modal"),
  rename: () => bindOnce("acct-rename-modal"),
};

// Keyed by element, not id: the router swaps in a new Settings page with the same ids.
const bindings = new WeakMap<HTMLElement, ReturnType<typeof bindModal>>();
function bindOnce(id: string): ReturnType<typeof bindModal> {
  const el = $(id);
  let b = bindings.get(el);
  if (!b) {
    b = bindModal(el);
    bindings.set(el, b);
  }
  return b;
}

export async function loadAccountLifecycle(): Promise<void> {
  bindForms();
  const listEl = $("acct-workspaces");
  const session = await sessionInfo();
  if (!session?.user?.id) {
    listEl.innerHTML = "";
    return;
  }
  me = { id: session.user.id, email: session.user.email ?? "" };
  void fillIdentity(me.email, session.identity_providers ?? []);
  try {
    renderWorkspaces(await listMemberships());
  } catch (err) {
    listEl.textContent = errText(err, t("settings.account.workspaces.load_failed"));
  }
}

async function fillIdentity(email: string, keys: string[]): Promise<void> {
  const el = $("acct-identity");
  let providers: Array<{ key: string; name: string }> = [];
  try {
    const r = await fetch(`${apiBase()}/auth/config`);
    if (r.ok)
      providers = ((await r.json()) as { sso_providers?: typeof providers }).sso_providers ?? [];
  } catch {
    /* the provider keys alone will do */
  }
  const idp = identityProviderNames(keys, providers);
  el.textContent = idp
    ? t("settings.account.identity.signed_in_via", { email, idp })
    : t("settings.account.identity.signed_in", { email });
}

function renderWorkspaces(list: Membership[]): void {
  const listEl = $("acct-workspaces");
  listEl.innerHTML = "";
  if (!list.length) {
    listEl.innerHTML = emptyRowHTML(t("settings.account.workspaces.empty"), { as: "div" });
  }
  let ownedElsewhere = false;
  for (const m of list) {
    const owner = m.owner_id === me?.id;
    if (owner && !m.current) ownedElsewhere = true;
    listEl.appendChild(workspaceRow(m, owner));
  }
  $("acct-delete-hint").hidden = !ownedElsewhere;
}

const WORKSPACE_ACTIONS: Record<
  WorkspaceAction,
  {
    label: () => string;
    variant: ButtonVariant;
    run: (m: Membership, b: HTMLButtonElement) => void;
  }
> = {
  switch: {
    label: () => t("settings.account.workspaces.switch"),
    variant: "default",
    run: (m, b) => void switchTo(m, b),
  },
  rename: {
    label: () => t("settings.account.workspaces.rename"),
    variant: "default",
    run: (m) => openRename(m),
  },
  transfer: {
    label: () => t("settings.account.workspaces.transfer"),
    variant: "default",
    run: (m) => void openTransfer(m),
  },
  delete: {
    label: () => t("settings.account.workspaces.delete"),
    variant: "danger",
    run: (m) => void deleteWs(m),
  },
};

function workspaceRow(m: Membership, owner: boolean): HTMLElement {
  const acts = workspaceActions(m, owner);
  const tpl = document.createElement("template");
  tpl.innerHTML = listRowHTML({
    title: m.name,
    meta: workspaceMeta(m),
    actions: acts.map((act) => ({
      label: WORKSPACE_ACTIONS[act].label(),
      variant: WORKSPACE_ACTIONS[act].variant,
      attrs: { "data-act": act },
    })),
  });
  const row = tpl.content.firstElementChild as HTMLElement;
  for (const act of acts) {
    const b = row.querySelector<HTMLButtonElement>(`[data-act="${act}"]`);
    b?.addEventListener("click", () => WORKSPACE_ACTIONS[act].run(m, b));
  }
  return row;
}

async function switchTo(m: Membership, b: HTMLButtonElement): Promise<void> {
  b.disabled = true;
  try {
    await switchWorkspace(m.id);
    window.location.reload();
  } catch (err) {
    b.disabled = false;
    toast(errText(err, t("layout.workspaces.switch_failed")), "err");
  }
}

async function deleteWs(m: Membership): Promise<void> {
  const ok = await confirmDialog({
    title: t("settings.account.delete_workspace.title"),
    message: t("settings.account.delete_workspace.body", { name: m.name }),
    requireText: m.slug,
    requireMatch: (typed) => slugConfirmed(typed, m.slug),
    confirmLabel: t("settings.account.delete_workspace.button"),
    danger: true,
    action: async () => {
      try {
        await deleteWorkspace(m.id, m.slug);
      } catch (err) {
        throw new Error(errText(err, t("settings.account.delete_workspace.failed")), {
          cause: err,
        });
      }
    },
  });
  if (!ok) return;
  toast(t("settings.account.delete_workspace.done"), "ok");
  window.location.href = "/login/";
}

async function deleteMyAccount(email: string): Promise<void> {
  const blocking = document.createElement("ul");
  blocking.className = "acct-blocking";
  blocking.hidden = true;
  const { ok } = await confirmDialog({
    title: t("settings.account.delete_account.title"),
    message: t("settings.account.delete_account.body"),
    requireText: email,
    requireMatch: (typed) => emailConfirmed(typed, email),
    body: blocking,
    confirmLabel: t("settings.account.delete_account.button"),
    danger: true,
    action: async () => {
      blocking.hidden = true;
      try {
        await deleteAccount(email);
      } catch (err) {
        blocking.replaceChildren(
          ...blockingWorkspaces(err).map((b) => {
            const li = document.createElement("li");
            li.textContent = t("settings.account.delete_account.blocking_item", {
              name: b.name,
              count: b.member_count,
            });
            return li;
          }),
        );
        blocking.hidden = blocking.childElementCount === 0;
        throw new Error(errText(err, t("settings.account.delete_account.failed")), {
          cause: err,
        });
      }
    },
  });
  if (ok) window.location.href = "/login/";
}

function openRename(m: Membership): void {
  renameTarget = m;
  const input = $("acct-rename-input") as HTMLInputElement;
  input.value = m.name;
  showError("acct-rename-error", "");
  ($("acct-rename-submit") as HTMLButtonElement).disabled = false;
  modals.rename().open(input);
}

async function openTransfer(m: Membership): Promise<void> {
  transferTarget = m;
  $("acct-transfer-body").textContent = t("settings.account.transfer.body", {
    name: m.name,
  });
  showError("acct-transfer-error", "");
  const select = $("acct-transfer-select") as HTMLSelectElement;
  select.innerHTML = "";
  const submit = $("acct-transfer-submit") as HTMLButtonElement;
  submit.disabled = true;
  modals.transfer().open(select);
  try {
    const candidates = transferCandidates(await listMembers(m.id), m.owner_id);
    for (const c of candidates) {
      const opt = document.createElement("option");
      opt.value = c.user_id;
      opt.textContent = `${memberLabel(c)} · ${roleLabel(c.role)}`;
      select.appendChild(opt);
    }
    $("acct-transfer-field").hidden = candidates.length === 0;
    $("acct-transfer-empty").hidden = candidates.length > 0;
    submit.disabled = candidates.length === 0;
  } catch (err) {
    showError("acct-transfer-error", errText(err, t("settings.account.transfer.failed")));
  }
}

function onSubmit(formID: string, handler: () => Promise<void>): void {
  $(formID).addEventListener("submit", (e) => {
    e.preventDefault();
    void handler();
  });
}

function bindForms(): void {
  const renameInput = $("acct-rename-input") as HTMLInputElement;
  if (boundTo === renameInput) return;
  boundTo = renameInput;

  const renameSubmit = $("acct-rename-submit") as HTMLButtonElement;
  onSubmit("acct-rename-form", async () => {
    const target = renameTarget;
    if (!target) return;
    const problem = workspaceNameProblem(renameInput.value);
    if (problem) {
      showError("acct-rename-error", problem);
      return;
    }
    renameSubmit.disabled = true;
    try {
      await renameWorkspace(target.id, renameInput.value);
      modals.rename().close();
      toast(t("settings.account.rename.done"), "ok");
      window.location.reload();
    } catch (err) {
      renameSubmit.disabled = false;
      showError("acct-rename-error", errText(err, t("settings.account.rename.failed")));
    }
  });

  const transferSubmit = $("acct-transfer-submit") as HTMLButtonElement;
  onSubmit("acct-transfer-form", async () => {
    const target = transferTarget;
    const userID = ($("acct-transfer-select") as HTMLSelectElement).value;
    if (!target || !userID) return;
    transferSubmit.disabled = true;
    try {
      await transferOwnership(target.id, userID);
      modals.transfer().close();
      toast(t("settings.account.transfer.done"), "ok");
      window.location.reload();
    } catch (err) {
      transferSubmit.disabled = false;
      showError("acct-transfer-error", errText(err, t("settings.account.transfer.failed")));
    }
  });

  ($("acct-delete-account-open") as HTMLButtonElement).addEventListener("click", () => {
    if (me?.email) void deleteMyAccount(me.email);
  });
}
