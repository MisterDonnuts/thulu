import { t } from "../i18n";
import { fmtDate, fmtMs } from "./time";
import { tableHTML, type TableColumn } from "./ui";

export function eventDescription(event: string): string {
  switch (event) {
    case "task.created":
      return t("lib.webhooks.events.task_created");
    case "task.stage_changed":
      return t("lib.webhooks.events.task_stage_changed");
    case "task.deleted":
      return t("lib.webhooks.events.task_deleted");
    case "chat.message":
      return t("lib.webhooks.events.chat_message");
    case "stages.changed":
      return t("lib.webhooks.events.stages_changed");
    case "boards.changed":
      return t("lib.webhooks.events.boards_changed");
    case "task_types.changed":
      return t("lib.webhooks.events.task_types_changed");
    case "question.asked":
      return t("lib.webhooks.events.question_asked");
    case "question.answered":
      return t("lib.webhooks.events.question_answered");
    case "question.canceled":
      return t("lib.webhooks.events.question_canceled");
    case "approval.requested":
      return t("lib.webhooks.events.approval_requested");
    case "approval.decided":
      return t("lib.webhooks.events.approval_decided");
    default:
      return "";
  }
}

export interface TestDelivery {
  success: boolean;
  status_code?: number;
  duration_ms?: number;
  error?: string;
}

export function testResultToast(r: TestDelivery): { text: string; ok: boolean } {
  if (r.success) {
    return {
      ok: true,
      text: t("lib.webhooks.test.ok", { status: r.status_code ?? 0, ms: r.duration_ms ?? 0 }),
    };
  }
  if (r.status_code) {
    return { ok: false, text: t("lib.webhooks.test.failed_status", { status: r.status_code }) };
  }
  if (r.error) return { ok: false, text: t("lib.webhooks.test.failed_error", { error: r.error }) };
  return { ok: false, text: t("lib.webhooks.test.failed") };
}

export interface Delivery {
  id: string;
  event: string;
  status_code: number;
  attempt: number;
  duration_ms: number;
  error?: string;
  delivered: boolean;
  created_at: string;
  // Absent from older daemons: fall back on `delivered`.
  state?: "delivered" | "retrying" | "retried" | "failed";
  retryable?: boolean;
}

export function deliveryState(d: Delivery): string {
  switch (d.state ?? (d.delivered ? "delivered" : "failed")) {
    case "delivered":
      return t("lib.webhooks.deliveries.state_delivered");
    case "retrying":
      return t("lib.webhooks.deliveries.state_retrying");
    case "retried":
      return t("lib.webhooks.deliveries.state_retried");
    case "failed":
      return t("lib.webhooks.deliveries.state_failed");
  }
}

// Retrying while automatic retries still run, or from an earlier attempt, sends the event twice.
// Older daemons omit `retryable`: fall back to any failed attempt.
export function canRetryDelivery(d: Delivery): boolean {
  return d.retryable ?? !d.delivered;
}

export function deliveriesPending(list: Delivery[]): boolean {
  return list.some((d) => d.state === "retrying");
}

export function deliveryResult(d: Delivery): string {
  if (d.status_code) return t("lib.webhooks.deliveries.http", { status: d.status_code });
  if (d.error) return d.error;
  return t("lib.webhooks.deliveries.no_response");
}

export function deliveryColumns(): TableColumn[] {
  return [
    { label: t("settings.webhooks.deliveries.when"), width: "shrink" },
    { label: t("settings.webhooks.deliveries.event"), width: "shrink", mono: true },
    { label: t("settings.webhooks.deliveries.attempt"), width: "shrink", num: true },
    { label: t("settings.webhooks.deliveries.result"), width: "fill" },
    { label: t("settings.webhooks.deliveries.state"), width: "shrink" },
    { label: t("settings.webhooks.deliveries.duration"), width: "shrink", num: true },
    { label: t("settings.webhooks.deliveries.actions"), width: "shrink", hideLabel: true },
  ];
}

export function deliveriesTableHTML(list: Delivery[]): string {
  return tableHTML(
    deliveryColumns(),
    list.map((d) => ({
      cells: [
        fmtDate(d.created_at),
        d.event,
        d.attempt,
        deliveryResult(d),
        deliveryState(d),
        fmtMs(d.duration_ms),
        canRetryDelivery(d)
          ? {
              actions: [
                {
                  label: t("settings.webhooks.deliveries.retry"),
                  cls: "wh-retry-btn",
                  attrs: { "data-id": d.id },
                },
              ],
            }
          : "",
      ],
      tone: d.delivered ? undefined : "danger",
    })),
    { compact: true },
  );
}

export function validateWebhookForm(url: string, events: string[]): string | null {
  if (!url) return t("lib.webhooks.errors.url_required");
  try {
    new URL(url);
  } catch {
    return t("lib.webhooks.errors.url_invalid");
  }
  if (events.length === 0) return t("lib.webhooks.errors.events_required");
  return null;
}

// Some endpoints sign with HMAC keyed by hex(sha256(secret)), not the secret shown,
// so receivers following the docs cannot verify them; only rotating fixes it.
export const LEGACY_SIGNING_TITLE = t("lib.webhooks.legacy.title");

export function legacySigningNote(count: number): string | null {
  if (count <= 0) return null;
  return t("lib.webhooks.legacy.note", { count });
}

export function secretRevealLabel(url: string, rotated: boolean): string {
  return rotated
    ? t("lib.webhooks.secret.rotated", { url })
    : t("lib.webhooks.secret.created", { url });
}
