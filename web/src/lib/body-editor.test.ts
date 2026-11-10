import { describe, expect, it } from "vitest";
import {
  UPLOAD_FAILED,
  attachmentMarkdown,
  attachmentPath,
  embeddedIn,
  fmtBytes,
  missingBriefSections,
  pastedName,
  replacePlaceholder,
  spliceText,
  uploadPlaceholder,
  withBriefSections,
} from "./body-editor";

describe("attachment urls", () => {
  it("stays root-relative so the body travels between hosts", () => {
    expect(attachmentPath("abc")).toBe("/task-attachments/abc");
    expect(attachmentPath("abc")).not.toMatch(/^https?:/);
  });

  it("embeds an image and links a document", () => {
    expect(attachmentMarkdown({ id: "1", filename: "shot.png", mime_type: "image/png" })).toBe(
      "![shot.png](/task-attachments/1)",
    );
    expect(
      attachmentMarkdown({ id: "2", filename: "spec.pdf", mime_type: "application/pdf" }),
    ).toBe("[spec.pdf](/task-attachments/2)");
  });

  it("embeds video and audio too", () => {
    expect(attachmentMarkdown({ id: "3", filename: "clip.mp4", mime_type: "video/mp4" })).toMatch(
      /^!\[/,
    );
  });

  it("knows whether the body already shows a file", () => {
    expect(embeddedIn("see ![x](/task-attachments/9)", "9")).toBe(true);
    expect(embeddedIn("see ![x](/task-attachments/9)", "8")).toBe(false);
    expect(embeddedIn("", "9")).toBe(false);
  });
});

describe("pastedName", () => {
  it("keeps the name a dropped file already has", () => {
    const f = new File(["x"], "notes.txt", { type: "text/plain" });
    expect(pastedName(f)).toBe("notes.txt");
  });

  it("names a clipboard blob from its mime type", () => {
    expect(pastedName(new Blob(["x"], { type: "image/webp" }), 1700)).toBe("paste-1700.webp");
  });

  it("drops the parameters off a mime type", () => {
    expect(pastedName(new Blob(["x"], { type: "text/html;charset=utf-8" }), 1)).toBe(
      "paste-1.html",
    );
  });

  it("assumes png when the blob says nothing", () => {
    expect(pastedName(new Blob(["x"]), 1)).toBe("paste-1.png");
  });
});

describe("placeholders", () => {
  it("carries the filename and a stamp so two pastes do not collide", () => {
    expect(uploadPlaceholder("a.png", 1)).not.toBe(uploadPlaceholder("a.png", 2));
    expect(uploadPlaceholder("a.png", 1)).toContain("a.png");
  });

  it("swaps in the real markdown and pulls the caret with it", () => {
    const ph = uploadPlaceholder("a.png", 1);
    const value = `before ${ph} after`;
    const caret = `before ${ph}`.length;
    const out = replacePlaceholder(value, caret, ph, "![a.png](/task-attachments/7)");
    expect(out.value).toBe("before ![a.png](/task-attachments/7) after");
    expect(out.value.slice(0, out.caret)).toBe("before ![a.png](/task-attachments/7)");
  });

  it("leaves something visible when the upload fails", () => {
    expect(UPLOAD_FAILED).toContain("upload failed");
  });
});

describe("spliceText", () => {
  it("inserts at a collapsed caret", () => {
    expect(spliceText("ab", 1, 1, "X")).toEqual({ value: "aXb", caret: 2 });
  });

  it("replaces a selection and leaves the caret after it", () => {
    expect(spliceText("hello world", 0, 5, "bye")).toEqual({ value: "bye world", caret: 3 });
  });

  it("appends at the end", () => {
    expect(spliceText("ab", 2, 2, "!")).toEqual({ value: "ab!", caret: 3 });
  });
});

describe("fmtBytes", () => {
  it("switches units at 1024", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(1023)).toBe("1023 B");
    expect(fmtBytes(1024)).toBe("1.0 kB");
    expect(fmtBytes(1024 * 1024 - 1)).toBe("1024.0 kB");
    expect(fmtBytes(1024 * 1024)).toBe("1.0 MB");
    expect(fmtBytes(5 * 1024 * 1024 + 512 * 1024)).toBe("5.5 MB");
  });
});

describe("brief sections", () => {
  it("lists the sections a body lacks, by their address", () => {
    expect(missingBriefSections("")).toEqual([
      "Goal",
      "Context",
      "Definition of Done",
      "Out of Scope",
    ]);
    expect(missingBriefSections("## goal\nship it\n\n## Definition of done\n- [ ] x")).toEqual([
      "Context",
      "Out of Scope",
    ]);
  });

  it("adds the missing ones after the text, keeping what is there", () => {
    const out = withBriefSections("Fix the login.\n\n## Context\nIt broke on Monday.\n");
    expect(out).toBe(
      "Fix the login.\n\n## Context\nIt broke on Monday.\n\n## Goal\n\n## Definition of Done\n\n- [ ]\n\n## Out of Scope\n",
    );
  });

  it("starts an empty body with the four sections", () => {
    expect(withBriefSections("")).toBe(
      "## Goal\n\n## Context\n\n## Definition of Done\n\n- [ ]\n\n## Out of Scope\n",
    );
  });

  it("leaves a complete body alone", () => {
    const full = "## Goal\n\n## Context\n\n## Definition of Done\n\n## Out of Scope\n";
    expect(withBriefSections(full)).toBe(full);
  });
});
