// Openers unregister on page abort: the router keeps the header across navigations, and a stale
// opener would open a modal no longer in the document.

import { navigate } from "astro:transitions/client";
import { pageSignal, replaceURL } from "./page";

export type CreateKind = "task" | "chat" | "project" | "automation";

const PAGES: Record<Exclude<CreateKind, "chat">, string> = {
  task: "/tasks/",
  project: "/projects/",
  automation: "/automations/",
};

type Openers = Partial<Record<CreateKind, () => void>>;

function openers(): Openers {
  const w = window as unknown as { octopusCreate?: Openers };
  w.octopusCreate ??= {};
  return w.octopusCreate;
}

export function registerCreate(kind: CreateKind, open: () => void): void {
  openers()[kind] = open;
  pageSignal().addEventListener(
    "abort",
    () => {
      const all = openers();
      if (all[kind] === open) all[kind] = undefined;
    },
    { once: true },
  );
}

export function openCreate(kind: CreateKind): void {
  if (kind === "chat") {
    document.getElementById("chat-new-btn")?.click();
    return;
  }
  const open = openers()[kind];
  if (open) {
    open();
    return;
  }
  void navigate(`${PAGES[kind]}?new=1`);
}

export function takeNewParam(): boolean {
  const url = new URL(window.location.href);
  if (url.searchParams.get("new") !== "1") return false;
  url.searchParams.delete("new");
  replaceURL(url.pathname + url.search + url.hash);
  return true;
}
