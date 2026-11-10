import { t } from "../i18n";
import { MOD_KEY } from "./shortcuts";
import {
  buttonHTML,
  esc,
  fileRowHTML,
  mdEditorHTML,
  sectionHeadHTML,
  type ButtonOptions,
} from "./ui";

const SAVE_KEY = `${MOD_KEY}S`;

export interface StageEntry {
  stage: string;
  label: string;
  path: string;
  exists: boolean;
}

export interface StageSet {
  kind: "prompts" | "hooks";
  title: string;
  description: string;
  stages: StageEntry[];
}

export interface StageFiles {
  stage_dir: string;
  sets: StageSet[];
  orphans?: string[];
}

export const HOOK_TEMPLATE =
  // i18n-ignore the shebang, not a message
  "#!/usr/bin/env bash\n" + t("lib.stage_files.hook_template_comments") + "\nset -e\n\n";

export function stageSetsHTML(files: StageFiles, stageOrder: string[]): string {
  const sets = files.sets
    .map((set) => {
      const rows = stageOrder.flatMap((stID): string[] => {
        const e = set.stages.find((x) => x.stage === stID);
        if (!e) return [];
        const ref = `${set.kind}|${e.stage}`;
        const actions: ButtonOptions[] = e.exists
          ? [
              { label: t("common.buttons.edit"), icon: "edit", attrs: { "data-sf-edit": ref } },
              {
                label: t("common.buttons.delete"),
                icon: "trash",
                variant: "danger",
                attrs: { "data-sf-del": ref },
              },
            ]
          : [{ label: t("common.buttons.create"), icon: "plus", attrs: { "data-sf-edit": ref } }];
        return [
          fileRowHTML({
            label: e.label,
            ...(e.exists
              ? { status: e.path, tone: "ok" as const }
              : { status: t("lib.stage_files.labels.not_set"), statusMono: false }),
            actions,
            attrs: { "data-sf-stage": ref },
          }),
        ];
      });
      return `<div class="sf-set">${sectionHeadHTML({ title: set.title, level: 4 })}<p class="sf-intro">${esc(set.description)}</p>${rows.join("")}</div>`;
    })
    .join("");
  return sets + orphansHTML(files.orphans ?? []);
}

export function orphansHTML(orphans: string[]): string {
  if (!orphans.length) return "";
  return `<div class="sf-set">${sectionHeadHTML({ title: t("lib.stage_files.orphans.title"), level: 4 })}
             <p class="sf-intro">${esc(t("lib.stage_files.orphans.intro", { files: orphans.join(", ") }))}</p>
             ${buttonHTML({ label: t("lib.stage_files.orphans.clean_up"), id: "sf-prune" })}</div>`;
}

export function stageEditorHTML(entry: StageEntry, initial: string): string {
  return mdEditorHTML({
    path: entry.path,
    pathId: "sf-path",
    note: entry.exists ? undefined : t("lib.stage_files.labels.new_suffix"),
    textId: "sm-text",
    value: initial,
    back: { id: "sf-back" },
    stateId: "sf-state",
    save: { id: "sf-save", kbd: SAVE_KEY, disabled: entry.exists },
    box: true,
  });
}

export function parseStageRef(ref: string): { kind: string; stage: string } {
  const parts = ref.split("|");
  return { kind: parts.at(0) ?? "", stage: parts.at(1) ?? "" };
}
