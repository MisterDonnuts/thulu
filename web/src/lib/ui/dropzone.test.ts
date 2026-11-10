// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { t } from "../../i18n";
import type { UploadPlan, UploadReport } from "../project-upload";
import {
  bindDropOverlay,
  bindDropZone,
  dropAccepts,
  dropZoneClasses,
  dropKind,
  dropZoneHTML,
  resetDropZone,
  uploadLeftOutHTML,
  uploadPlanHTML,
  uploadReportHTML,
} from "./dropzone";

function parse(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = `<div>${html}</div>`;
  return tpl.content.firstElementChild as HTMLElement;
}

function item(name: string, isFile: boolean) {
  return { webkitGetAsEntry: () => ({ name, isFile, isDirectory: !isFile }) as FileSystemEntry };
}

function plan(over: Partial<UploadPlan> = {}): UploadPlan {
  return { files: [], count: 12, bytes: 2048, history: 0, skipped: [], excluded: [], ...over };
}

afterEach(() => {
  document.body.innerHTML = "";
});

describe("dropAccepts", () => {
  it("says which kinds the zone takes", () => {
    expect(dropAccepts()).toEqual({ folder: true, zip: true, json: false });
    expect(dropAccepts("zip")).toEqual({ folder: false, zip: true, json: false });
    expect(dropAccepts("json")).toEqual({ folder: false, zip: false, json: true });
    expect(() => dropAccepts("tar" as never)).toThrow(/no accept "tar"/);
  });
});

describe("dropZoneHTML", () => {
  it("writes a named group with a button and an input per kind", () => {
    const zone = parse(
      dropZoneHTML({ id: "m-drop", icon: "folder", title: "Drop it", sub: "or pick it" }),
    ).firstElementChild as HTMLElement;
    expect(zone.className).toBe("drop-zone");
    expect(zone.getAttribute("role")).toBe("group");
    expect(zone.getAttribute("aria-labelledby")).toBe("m-drop-title");
    expect(zone.hasAttribute("tabindex")).toBe(false);
    expect(zone.querySelector("#m-drop-title")?.textContent).toBe("Drop it");
    expect(zone.querySelector(".drop-sub")?.textContent).toBe("or pick it");
    expect(zone.querySelector("svg.drop-ico")).not.toBeNull();
    const picks = Array.from(zone.querySelectorAll("button"), (b) => b.dataset.dropPick);
    expect(picks).toEqual(["folder", "zip"]);
    expect(zone.querySelector("button")?.textContent).toBe(t("projects.upload.pick_folder"));
    const folder = zone.querySelector<HTMLInputElement>('input[data-drop-input="folder"]');
    expect(folder?.hasAttribute("webkitdirectory")).toBe(true);
    expect(folder?.hidden).toBe(true);
    expect(zone.querySelector('input[data-drop-input="zip"]')?.getAttribute("accept")).toBe(
      ".zip,application/zip",
    );
  });

  it("leaves out what a zone that takes one kind does not take", () => {
    const zone = parse(dropZoneHTML({ id: "z", title: "Zip", accept: "zip" }));
    expect(zone.querySelectorAll("button")).toHaveLength(1);
    expect(zone.querySelector('[data-drop-input="folder"]')).toBeNull();
  });

  it("writes one .json button and input for a json zone", () => {
    const zone = parse(dropZoneHTML({ id: "j", title: "Bundle", accept: "json" }));
    const picks = Array.from(zone.querySelectorAll("button"), (b) => b.dataset.dropPick);
    expect(picks).toEqual(["json"]);
    expect(zone.querySelector("button")?.textContent).toBe(t("ui.dropzone.pick_json"));
    expect(zone.querySelector('input[data-drop-input="json"]')?.getAttribute("accept")).toBe(
      ".json,application/json",
    );
  });

  it("writes an overlay with no buttons and no inputs", () => {
    const zone = parse(
      dropZoneHTML({
        id: "o",
        title: "Drop",
        accept: "json",
        overlay: true,
        attrs: { hidden: true },
      }),
    ).firstElementChild as HTMLElement;
    expect(zone.className).toBe("drop-zone drop-zone-overlay");
    expect(zone.hidden).toBe(true);
    expect(zone.querySelector(".drop-actions")).toBeNull();
    expect(zone.querySelector("input")).toBeNull();
    expect(dropZoneClasses({ cls: "x" })).toBe("drop-zone x");
  });

  it("escapes the texts", () => {
    const evil = '<img src=x onerror="alert(1)">';
    const zone = parse(dropZoneHTML({ id: "z", title: evil, sub: evil }));
    expect(zone.querySelector("img")).toBeNull();
    expect(zone.querySelector(".drop-title")?.textContent).toBe(evil);
  });
});

describe("dropKind", () => {
  it("tells one .zip from a folder", () => {
    expect(dropKind({ items: [item("app.ZIP", true)] })).toBe("zip");
    expect(dropKind({ items: [item("app", false)] })).toBe("folder");
    expect(dropKind({ items: [item("a.zip", true), item("b.zip", true)] })).toBe("folder");
    expect(dropKind({ items: [item("main.go", true)] })).toBe("folder");
    expect(dropKind({ items: [] })).toBeNull();
  });

  it("refuses what the zone does not take", () => {
    expect(dropKind({ items: [item("app", false)] }, "zip")).toBeNull();
    expect(dropKind({ items: [item("app.zip", true)] }, "folder")).toBeNull();
  });

  it("takes one file for a json zone, and leaves its content to the handler", () => {
    expect(dropKind({ items: [item("agent.json", true)] }, "json")).toBe("json");
    expect(dropKind({ items: [item("agent.txt", true)] }, "json")).toBe("json");
    expect(dropKind({ items: [item("agents", false)] }, "json")).toBeNull();
    expect(dropKind({ items: [item("a.json", true), item("b.json", true)] }, "json")).toBeNull();
    expect(dropKind({ items: [item("a.json", true)] })).toBe("folder");
  });
});

describe("bindDropZone", () => {
  function mountZone(accept?: "both" | "zip") {
    document.body.innerHTML = dropZoneHTML({ id: "z", title: "Drop", accept });
    return document.getElementById("z") as HTMLElement;
  }

  it("opens the input of the button picked", () => {
    const zone = mountZone();
    bindDropZone(zone, {});
    const folder = zone.querySelector<HTMLInputElement>('[data-drop-input="folder"]');
    const zip = zone.querySelector<HTMLInputElement>('[data-drop-input="zip"]');
    const folderClick = vi.spyOn(folder as HTMLInputElement, "click");
    const zipClick = vi.spyOn(zip as HTMLInputElement, "click");
    zone.querySelector<HTMLButtonElement>('[data-drop-pick="zip"]')?.click();
    expect(zipClick).toHaveBeenCalledTimes(1);
    expect(folderClick).not.toHaveBeenCalled();
  });

  it("shows .over while dragged onto, and not while moving between its children", () => {
    const zone = mountZone();
    bindDropZone(zone, {});
    zone.dispatchEvent(new Event("dragover", { cancelable: true }));
    expect(zone.classList.contains("over")).toBe(true);
    const leaveToChild = new MouseEvent("dragleave", {
      relatedTarget: zone.querySelector(".drop-title"),
    });
    zone.dispatchEvent(leaveToChild);
    expect(zone.classList.contains("over")).toBe(true);
    zone.dispatchEvent(new MouseEvent("dragleave", { relatedTarget: document.body }));
    expect(zone.classList.contains("over")).toBe(false);
  });

  it("hands a dropped .zip and a dropped folder to their handlers", () => {
    const zone = mountZone();
    const onZip = vi.fn();
    const onFolderDrop = vi.fn();
    bindDropZone(zone, { onZip, onFolderDrop });
    const drop = (items: unknown[], file: File | null) => {
      const e = new Event("drop", { cancelable: true });
      Object.defineProperty(e, "dataTransfer", {
        value: { items, files: { item: () => file } },
      });
      zone.dispatchEvent(e);
      return e;
    };
    const zipFile = new File(["x"], "app.zip");
    expect(drop([item("app.zip", true)], zipFile).defaultPrevented).toBe(true);
    expect(onZip).toHaveBeenCalledWith(zipFile);
    const items = [item("app", false)];
    drop(items, null);
    expect(onFolderDrop).toHaveBeenCalledWith(items);
  });

  it("stops listening when the signal aborts", () => {
    const zone = mountZone();
    const ctl = new AbortController();
    bindDropZone(zone, { signal: ctl.signal });
    ctl.abort();
    zone.dispatchEvent(new Event("dragover", { cancelable: true }));
    expect(zone.classList.contains("over")).toBe(false);
  });

  it("resets the inputs", () => {
    const zone = mountZone();
    const spy = vi.fn();
    zone
      .querySelectorAll("input")
      .forEach((i) => Object.defineProperty(i, "value", { set: spy, configurable: true }));
    resetDropZone(zone);
    expect(spy).toHaveBeenCalledTimes(2);
  });
});

describe("bindDropOverlay", () => {
  function mountOverlay() {
    document.body.innerHTML = `<div id="box"><p>content</p>${dropZoneHTML({
      id: "o",
      title: "Drop",
      accept: "json",
      overlay: true,
      attrs: { hidden: true },
    })}</div>`;
    return {
      box: document.getElementById("box") as HTMLElement,
      zone: document.getElementById("o") as HTMLElement,
    };
  }

  function drag(type: string, o: { files?: boolean; items?: unknown[]; file?: File | null } = {}) {
    const e = new Event(type, { cancelable: true, bubbles: true });
    Object.defineProperty(e, "dataTransfer", {
      value: {
        types: o.files === false ? ["text/plain"] : ["Files"],
        dropEffect: "copy",
        items: o.items ?? [],
        files: { item: () => o.file ?? null },
      },
    });
    return e;
  }

  it("shows while files are dragged over the container, through its children", () => {
    const { box, zone } = mountOverlay();
    bindDropOverlay(box, zone, {});
    const child = box.querySelector("p") as HTMLElement;
    box.dispatchEvent(drag("dragenter"));
    child.dispatchEvent(drag("dragenter"));
    expect(zone.hidden).toBe(false);
    child.dispatchEvent(drag("dragleave"));
    expect(zone.hidden).toBe(false);
    box.dispatchEvent(drag("dragleave"));
    expect(zone.hidden).toBe(true);
  });

  it("hands the dropped file to onJson and hides", () => {
    const { box, zone } = mountOverlay();
    const onJson = vi.fn();
    bindDropOverlay(box, zone, { onJson });
    box.dispatchEvent(drag("dragenter"));
    const file = new File(["{}"], "agent.json");
    const e = drag("drop", { items: [item("agent.json", true)], file });
    box.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(onJson).toHaveBeenCalledWith(file);
    expect(zone.hidden).toBe(true);
  });

  it("stays hidden while not enabled, but swallows the drop so the browser keeps the page", () => {
    const { box, zone } = mountOverlay();
    const onJson = vi.fn();
    bindDropOverlay(box, zone, { onJson, enabled: () => false });
    const enter = drag("dragenter");
    box.dispatchEvent(enter);
    expect(zone.hidden).toBe(true);
    expect(enter.defaultPrevented).toBe(true);
    const over = drag("dragover");
    box.dispatchEvent(over);
    expect(over.defaultPrevented).toBe(true);
    expect((over as DragEvent).dataTransfer?.dropEffect).toBe("none");
    const e = drag("drop", { items: [item("a.json", true)], file: new File(["{}"], "a.json") });
    box.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
    expect(onJson).not.toHaveBeenCalled();
  });

  it("hides when the drop stops being welcome in the middle of a drag", () => {
    const { box, zone } = mountOverlay();
    let on = true;
    bindDropOverlay(box, zone, { enabled: () => on });
    box.dispatchEvent(drag("dragenter"));
    expect(zone.hidden).toBe(false);
    on = false;
    box.dispatchEvent(drag("dragover"));
    expect(zone.hidden).toBe(true);
  });

  it("leaves a drag without files to the browser", () => {
    const { box, zone } = mountOverlay();
    const onJson = vi.fn();
    bindDropOverlay(box, zone, { onJson });
    const enter = drag("dragenter", { files: false });
    box.dispatchEvent(enter);
    expect(zone.hidden).toBe(true);
    expect(enter.defaultPrevented).toBe(false);
    const e = drag("drop", { files: false, items: [item("a.json", true)] });
    box.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
    expect(onJson).not.toHaveBeenCalled();
  });

  it("stops listening when the signal aborts", () => {
    const { box, zone } = mountOverlay();
    const ctl = new AbortController();
    bindDropOverlay(box, zone, { signal: ctl.signal });
    ctl.abort();
    box.dispatchEvent(drag("dragenter"));
    expect(zone.hidden).toBe(true);
  });
});

describe("uploadPlanHTML", () => {
  it("is empty with neither a plan nor a zip", () => {
    expect(uploadPlanHTML(null)).toBe("");
  });

  it("shows a zip's size and that the daemon sorts it", () => {
    const el = parse(uploadPlanHTML(plan(), { zip: { size: 4096 } }));
    expect(el.querySelectorAll(".badge")).toHaveLength(1);
    expect(el.querySelector(".plan-note")?.textContent).toBe(t("projects.upload.zip_note"));
  });

  it("badges the files and their size, and the kept history for a new project", () => {
    const el = parse(uploadPlanHTML(plan({ history: 3 })));
    const badges = Array.from(el.querySelectorAll(".plan-line .badge"), (b) => b.textContent);
    expect(badges).toEqual([
      t("projects.upload.files", { count: 12 }),
      "2.0 KB",
      t("projects.upload.with_history"),
    ]);
  });

  it("says a new version ignores the folder's .git instead", () => {
    const el = parse(uploadPlanHTML(plan({ history: 3 }), { history: "ignore" }));
    expect(el.querySelectorAll(".plan-line .badge")).toHaveLength(2);
    expect(el.querySelector(".plan-note")?.textContent).toBe(t("project.code.history_ignored"));
  });

  it("says when nothing is left to send", () => {
    const el = parse(uploadPlanHTML(plan({ count: 0, bytes: 0 })));
    expect(el.querySelector(".field-error")?.textContent).toBe(t("projects.upload.nothing_left"));
  });

  it("lists what stays out in disclosures", () => {
    const el = parse(uploadPlanHTML(plan({ excluded: [".env"], skipped: ["node_modules"] })));
    const lists = el.querySelectorAll("details.disclosure");
    expect(lists).toHaveLength(2);
    expect(lists[0].querySelector("summary")?.textContent).toBe(
      t("projects.upload.excluded", { count: 1 }),
    );
    expect(lists[1].querySelector(".upload-paths li")?.textContent).toBe("node_modules");
  });
});

describe("uploadLeftOutHTML", () => {
  it("says how many more the daemon cut, and escapes the paths", () => {
    const el = parse(uploadLeftOutHTML(["<b>.env</b>"], 5, [], 0));
    const items = Array.from(el.querySelectorAll("li"), (li) => li.textContent);
    expect(items).toEqual(["<b>.env</b>", t("projects.upload.and_more", { count: 4 })]);
    expect(el.querySelector("b")).toBeNull();
    expect(uploadLeftOutHTML([], 0, [], 0)).toBe("");
  });
});

describe("uploadReportHTML", () => {
  const report: UploadReport = {
    files: 3,
    bytes: 10,
    excluded_count: 0,
    skipped: ["dist"],
    skipped_count: 1,
    history: false,
    history_ignored: true,
    branch: "main",
    commit: "abc",
    changed: true,
  };

  it("badges what went up, notes the ignored history, lists what stayed out", () => {
    const el = parse(uploadReportHTML(report));
    expect(el.querySelectorAll(".plan-line .badge")).toHaveLength(2);
    expect(el.querySelector(".plan-note")?.textContent).toBe(t("projects.upload.history_ignored"));
    expect(el.querySelectorAll("details")).toHaveLength(1);
  });

  it("writes the page's sentence instead of the badges", () => {
    const el = parse(uploadReportHTML(report, { lead: "New commit abc on main." }));
    expect(el.querySelector(".plan-line")).toBeNull();
    expect(el.querySelector("p")?.textContent).toBe("New commit abc on main.");
  });
});
