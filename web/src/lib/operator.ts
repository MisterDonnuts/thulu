import { t } from "../i18n";
import { ApiError } from "./api";
import { sessionInfo } from "./auth";
import { calloutClasses } from "./ui/callout";

export const OPERATOR_ENV = "OCTOPUS_OPERATOR_WORKSPACE";

// Unknown reads as writable: the server still decides every write, and read-only would lock out
// no-login installs.
export async function mayWriteInstallWide(): Promise<boolean> {
  const me = await sessionInfo();
  return me?.operator !== false;
}

// Recognized by the daemon's 403 message naming OCTOPUS_OPERATOR_WORKSPACE; changing that message
// breaks this.
export function isOperatorRefusal(e: unknown): boolean {
  return e instanceof ApiError && e.status === 403 && e.message.includes(OPERATOR_ENV);
}

export function operatorNote(lead: string): string {
  return t("lib.operator.read_only_note", { lead, env: OPERATOR_ENV });
}

export function showReadOnly(
  host: HTMLElement,
  lead: string,
  controls: Iterable<Element | null | undefined>,
): void {
  let note = host.querySelector<HTMLElement>(":scope > .readonly-note");
  if (!note) {
    note = document.createElement("p");
    note.className = calloutClasses({ tone: "warn", bare: true, cls: "readonly-note" });
    note.setAttribute("role", "note");
    host.prepend(note);
  }
  note.textContent = operatorNote(lead);
  for (const c of controls) {
    if (
      c instanceof HTMLButtonElement ||
      c instanceof HTMLInputElement ||
      c instanceof HTMLSelectElement ||
      c instanceof HTMLTextAreaElement
    ) {
      c.disabled = true;
    }
  }
}
