import { t } from "../i18n";
import { ApiError } from "./api";

export const LISTED_SUBTASKS = 8;

export interface DeleteWithSubtasks {
  message: string;
  details: string[];
  notes: string[];
}

export function deleteWithSubtasks(titles: string[]): DeleteWithSubtasks {
  const details = titles.slice(0, LISTED_SUBTASKS);
  const rest = titles.length - details.length;
  if (rest > 0) details.push(t("lib.task_delete.more", { count: rest }));
  return {
    message: t("lib.task_delete.intro", { count: titles.length }),
    details,
    notes: [t("lib.task_delete.outro")],
  };
}

export function refusedForSubtasks(err: unknown): boolean {
  if (!(err instanceof ApiError) || err.status !== 409) return false;
  const body = err.body as { code?: string } | undefined;
  return body?.code === "api.tasks.errors.delete_subtasks_first";
}
