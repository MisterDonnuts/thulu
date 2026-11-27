// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { t } from "../i18n";
import { fileIcon, fileRowInnerHTML, fileSize, filesNewestFirst } from "./files-view";
import { fmtDayClock } from "./time";

describe("the Files tab", () => {
  it("picks the icon by kind: video plays, image is a picture, the rest a page", () => {
    expect(fileIcon({ mime_type: "video/mp4", filename: "a.mp4" })).toBe("i-play");
    expect(fileIcon({ mime_type: "image/png", filename: "a.png" })).toBe("i-camera");
    expect(fileIcon({ mime_type: "text/markdown", filename: "a.md" })).toBe("i-file");
  });

  it("says sizes in whole kilobytes, never under 1, and megabytes past a million bytes", () => {
    expect(fileSize(120)).toBe(t("lib.files_view.size.kb", { n: 1 }));
    expect(fileSize(3_400)).toBe(t("lib.files_view.size.kb", { n: 3 }));
    expect(fileSize(705_000)).toBe(t("lib.files_view.size.kb", { n: 705 }));
    expect(fileSize(1_240_000)).toBe(t("lib.files_view.size.mb", { n: "1.2" }));
  });

  it("lists the newest file first, and keeps the later of two files added together on top", () => {
    const files = [
      { id: "a", created_at: "2026-11-20T18:35:00Z" },
      { id: "b", created_at: "2026-11-20T23:22:00Z" },
      { id: "c", created_at: "2026-11-20T18:35:00Z" },
    ];
    expect(filesNewestFirst(files).map((f) => f.id)).toEqual(["b", "c", "a"]);
  });

  it("draws a row: icon, the name that opens the file, size, who added it and when", () => {
    const at = "2026-11-20T23:22:00Z";
    const box = document.createElement("div");
    box.innerHTML = fileRowInnerHTML(
      {
        id: "f1",
        filename: "abc-123 <evidence>.md",
        mime_type: "text/markdown",
        size: 3_000,
        created_at: at,
      },
      "thulu-evidence",
    );
    const name = box.querySelector<HTMLButtonElement>("button.tv-file-nm");
    expect(name?.dataset.fileOpen).toBe("f1");
    expect(name?.textContent).toBe("abc-123 <evidence>.md");
    expect(name?.title).toBe("abc-123 <evidence>.md");
    expect(box.querySelector(".tv-file-ic use")?.getAttribute("href")).toBe("#i-file");
    expect(box.querySelector(".tv-file-sz")?.textContent).toBe(fileSize(3_000));
    expect(box.querySelector(".tv-file-by")?.textContent).toBe(
      `thulu-evidence · ${fmtDayClock(at)}`,
    );
  });
});
