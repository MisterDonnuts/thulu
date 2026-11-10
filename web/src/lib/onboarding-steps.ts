import { t } from "../i18n";
import { safeUrl } from "./safe-url";
import type { StepFinish, StepItem } from "./ui";

export interface SetupStep {
  id: string;
  title: string;
  done: boolean;
  detail?: string;
  effect?: string;
  finish?: string;
  href?: string;
  command?: string;
}

export function setupFinish(
  step: Omit<SetupStep, "id" | "title" | "done">,
): StepFinish | undefined {
  const href = safeUrl(step.href);
  const fallback = href ? t("onboarding.steps.finish") : "";
  const text = step.finish === "" ? fallback : (step.finish ?? fallback);
  if (!text && !step.command) return undefined;
  return { text, href, command: step.command };
}

export function setupStepItem(step: SetupStep): StepItem {
  if (step.done) {
    return {
      title: step.title,
      state: "done",
      detail: step.detail,
      attrs: { "data-step": step.id },
    };
  }
  return {
    title: step.title,
    state: "pending",
    detail: step.detail,
    note: t("onboarding.steps.until_then", { effect: step.effect ?? "" }),
    finish: setupFinish(step),
    attrs: { "data-step": step.id },
  };
}
