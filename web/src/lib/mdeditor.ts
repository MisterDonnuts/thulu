import { t } from "../i18n";
import { setSaveState } from "./ui/catalog";
import { confirmDialog } from "./ui/confirm";

export function confirmDiscard(
  message: string,
  title = t("lib.mdeditor.discard.title"),
): Promise<boolean> {
  return confirmDialog({
    title,
    message,
    confirmLabel: t("lib.mdeditor.discard.confirm"),
    danger: true,
  });
}

export interface MdEditorOpts {
  ta: HTMLTextAreaElement;
  saveBtn: HTMLButtonElement;
  stateEl?: HTMLElement;
  backBtn: HTMLElement;
  isCreate: boolean;
  saveLabel?: string;
  getOriginal: () => string;
  setOriginal: (v: string) => void;
  save: (text: string) => Promise<unknown>;
  onSaveError: (err: unknown) => void;
  onSaved: (text: string) => void | Promise<void>;
  discardMessage: string;
  onBack: () => Promise<void>;
}

export function wireMdEditor(o: MdEditorOpts): void {
  const saveLabel = o.saveLabel ?? o.saveBtn.innerHTML;
  const markDirty = (dirty: boolean) => {
    if (o.stateEl) setSaveState(o.stateEl, dirty);
  };
  o.ta.addEventListener("input", () => {
    const dirty = o.ta.value !== o.getOriginal();
    o.saveBtn.disabled = !dirty && !o.isCreate;
    markDirty(dirty);
  });
  o.saveBtn.addEventListener("click", () => {
    void (async () => {
      o.saveBtn.disabled = true;
      o.saveBtn.textContent = t("common.status.saving");
      try {
        await o.save(o.ta.value);
      } catch (err) {
        o.onSaveError(err);
        o.saveBtn.disabled = false;
        o.saveBtn.innerHTML = saveLabel;
        return;
      }
      o.setOriginal(o.ta.value);
      o.saveBtn.innerHTML = saveLabel;
      o.saveBtn.disabled = true;
      markDirty(false);
      await o.onSaved(o.ta.value);
    })();
  });
  o.ta.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "s") {
      e.preventDefault();
      if (!o.saveBtn.disabled) o.saveBtn.click();
    }
  });
  o.backBtn.addEventListener("click", () => {
    void (async () => {
      if (o.ta.value !== o.getOriginal()) {
        const ok = await confirmDiscard(o.discardMessage);
        if (!ok) return;
      }
      await o.onBack();
    })();
  });
}
