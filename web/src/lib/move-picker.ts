import { t } from "../i18n";
import { ApiError } from "./api";
import { buttonHTML, destinationOptionsHTML, type ButtonVariant, type MoveDestination } from "./ui";

export type { MoveDestination };

export function pendingTaskCount(err: unknown): number {
  if (!(err instanceof ApiError)) return 0;
  const body = err.body as { task_count?: number } | undefined;
  return Number(body?.task_count) || 0;
}

export const destinationOptions = destinationOptionsHTML;

export interface MovePicker {
  // Trusted HTML: callers put a `<code>` in it and must escape the rest themselves.
  message: string;
  destinations: MoveDestination[];
  confirmLabel: string;
  confirmVariant?: ButtonVariant;
}

export function movePickerHTML(p: MovePicker): string {
  return `
        <span>${p.message}</span>
        <select class="move-to">${destinationOptions(p.destinations)}</select>
        ${buttonHTML({ label: p.confirmLabel, variant: p.confirmVariant, attrs: { "data-act": "confirm" } })}
        ${buttonHTML({ label: t("common.buttons.cancel"), variant: "ghost", attrs: { "data-act": "cancel" } })}`;
}
