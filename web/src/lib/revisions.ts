import { t } from "../i18n";
import { api } from "./api";
import { fmtDate } from "./time";
import {
  confirmDialog,
  fillHistoryList,
  historyListError,
  historyPreviewHTML,
  openHistoryView,
} from "./ui";

export interface Revision {
  id: number;
  kind: string;
  owner: string;
  path: string;
  sha256?: string;
  prev_sha256?: string;
  action: string;
  actor?: string;
  created_at: string;
  size?: number;
}

export function revisionAction(rev: Revision): string {
  switch (rev.action) {
    case "created":
      return t("lib.revisions.action.created");
    case "deleted":
      return t("lib.revisions.action.deleted");
    case "restored":
      return t("lib.revisions.action.restored");
    default:
      return t("lib.revisions.action.updated");
  }
}

export function restoreMode(rev: Revision): "reset" | "revert" {
  return rev.sha256 ? "reset" : "revert";
}

export interface HistoryOpts {
  host: HTMLElement;
  base: string;
  path: string;
  readOnly?: boolean;
  onBack: () => void | Promise<void>;
  onRestored: () => void | Promise<void>;
  toast: (msg: string, kind?: "ok" | "err" | "info", ms?: number) => void;
}

export async function renderHistory(o: HistoryOpts): Promise<void> {
  const { list, preview } = openHistoryView(o.host, {
    title: t("lib.revisions.title", { path: o.path }),
    pick: t("lib.revisions.pick"),
    backLabel: t("lib.revisions.back"),
    onBack: o.onBack,
  });
  let revs: Revision[];
  try {
    revs = await api.get<Revision[]>(
      `${o.base}/revisions?path=${encodeURIComponent(o.path)}&limit=100`,
    );
  } catch (e) {
    historyListError(list, (e as Error).message);
    return;
  }
  fillHistoryList(
    list,
    revs.map((r) => ({
      action: revisionAction(r),
      meta: `${r.actor ?? t("lib.revisions.unknown_actor")} · ${fmtDate(r.created_at)}`,
      tone: r.action === "deleted" ? ("danger" as const) : undefined,
    })),
    {
      none: t("lib.revisions.none"),
      onPick: (i) => void showRevision(o, preview, revs[i], revs[0].id),
    },
  );
}

async function showRevision(
  o: HistoryOpts,
  pane: HTMLElement,
  rev: Revision,
  newestID: number,
): Promise<void> {
  const mode = restoreMode(rev);
  const canRestore = !o.readOnly && (rev.id !== newestID || mode === "revert");
  const restoreLabel =
    mode === "revert" ? t("lib.revisions.undo_delete") : t("lib.revisions.restore");
  let body = "";
  if (rev.sha256) {
    try {
      body = await api.getText(`${o.base}/revisions/${rev.id}/body`);
    } catch (e) {
      body = (e as Error).message;
    }
  }
  pane.innerHTML = historyPreviewHTML({
    when: fmtDate(rev.created_at),
    body: rev.sha256 ? { text: body } : { empty: t("lib.revisions.deleted_body") },
    action: canRestore ? { label: restoreLabel } : undefined,
  });
  pane.querySelector<HTMLButtonElement>("[data-rev-restore]")?.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog({
        title: t("lib.revisions.confirm_title", { path: o.path }),
        message: t("lib.revisions.confirm_message"),
        confirmLabel: restoreLabel,
      });
      if (!ok) return;
      try {
        await api.post(`${o.base}/revisions/${rev.id}/restore?mode=${mode}`);
      } catch (e) {
        o.toast((e as Error).message, "err", 4500);
        return;
      }
      o.toast(t("lib.revisions.restored", { path: o.path }), "ok", 1800);
      await o.onRestored();
    })();
  });
}
