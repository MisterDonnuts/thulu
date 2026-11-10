import { describe, it, expect } from "vitest";
import { extOf, mediaKind, attachmentIDFromURL, countKinds } from "./media";
import { mdInline } from "./markdown";

describe("extOf", () => {
  it("reads the extension of a name and of a URL path", () => {
    expect(extOf("clip.MP4")).toBe("mp4");
    expect(extOf("/a/b/report.pdf?v=2")).toBe("pdf");
    expect(extOf("no-extension")).toBe("");
    expect(extOf(".hidden")).toBe("");
  });
});

describe("mediaKind", () => {
  it("trusts the MIME type first", () => {
    expect(mediaKind("video/mp4", "whatever")).toBe("video");
    expect(mediaKind("image/png")).toBe("image");
    expect(mediaKind("audio/mpeg")).toBe("audio");
  });

  it("falls back to the filename when the URL carries no extension", () => {
    expect(mediaKind("", "screen-recording.webm", "/task-attachments/abc123")).toBe("video");
    expect(mediaKind("", "shot.png", "/task-attachments/abc123")).toBe("image");
    expect(mediaKind("", "report.pdf", "/task-attachments/abc123")).toBe("file");
  });

  it("returns file when nothing identifies it", () => {
    expect(mediaKind("", "", "")).toBe("file");
  });
});

describe("attachmentIDFromURL", () => {
  it("pulls the id out of an attachment URL", () => {
    expect(attachmentIDFromURL("http://host:8585/task-attachments/abc-123")).toBe("abc-123");
    expect(attachmentIDFromURL("https://example.com/other.png")).toBe("");
  });
});

describe("mdInline media embeds", () => {
  it("gives a video a player instead of a broken image", () => {
    const html = mdInline("![clip.mp4](/task-attachments/xyz)");
    expect(html).toContain("<video");
    expect(html).toContain('src="/task-attachments/xyz"');
    expect(html).not.toContain("<img");
  });

  it("still renders an image as an image", () => {
    expect(mdInline("![shot.png](/task-attachments/xyz)")).toContain("<img");
  });

  it("does not autolink the URL inside a player's src", () => {
    const html = mdInline("![clip.mp4](https://host/task-attachments/xyz)");
    expect(html).not.toContain("<a href");
  });
});

describe("mediaBase", () => {
  it("puts the reader's own origin in front of a stored attachment path", () => {
    const html = mdInline("![shot.png](/task-attachments/abc)", {
      mediaBase: "http://localhost:8585",
    });
    expect(html).toContain('src="http://localhost:8585/task-attachments/abc"');
  });

  it("leaves other URLs alone", () => {
    const html = mdInline("![x](https://example.com/a.png)", { mediaBase: "http://host:8585" });
    expect(html).toContain('src="https://example.com/a.png"');
  });

  it("resolves a link to a non-media attachment too", () => {
    const html = mdInline("[report.pdf](/task-attachments/def)", { mediaBase: "http://h:8585" });
    expect(html).toContain('href="http://h:8585/task-attachments/def"');
  });
});

describe("attachment URLs an author hardcoded", () => {
  it("swaps the written origin for the reader's own", () => {
    const html = mdInline("![shot.png](http://localhost:8585/task-attachments/abc)", {
      mediaBase: "https://board.example.com",
    });
    expect(html).toContain('src="https://board.example.com/task-attachments/abc"');
    expect(html).not.toContain("localhost:8585");
  });

  it("keeps the query string", () => {
    const html = mdInline("[f](http://h:8585/task-attachments/abc?download=1)", {
      mediaBase: "https://board.example.com",
    });
    expect(html).toContain('href="https://board.example.com/task-attachments/abc?download=1"');
  });
});

describe("mdInline with the attachment record at hand", () => {
  const atts: Record<string, { mime?: string; filename?: string }> = {
    png: { mime: "image/png", filename: "matriz.png" },
    pdf: { mime: "application/pdf", filename: "informe.pdf" },
  };
  const opts = { attachment: (id: string) => atts[id] };

  it("renders an image whose alt text has no extension", () => {
    expect(mdInline("![la matriz guardada](/task-attachments/png)", opts)).toContain("<img");
  });

  it("gives a PDF a link instead of a broken image", () => {
    const html = mdInline("![informe](/task-attachments/pdf)", opts);
    expect(html).toContain('<a class="md-file"');
    expect(html).toContain("informe");
    expect(html).not.toContain("<img");
  });

  it("links a PDF by its extension when the record is missing", () => {
    expect(mdInline("![informe.pdf](/task-attachments/zzz)")).toContain('<a class="md-file"');
  });

  it("still guesses image for an embed with no clue at all", () => {
    expect(mdInline("![](/task-attachments/zzz)")).toContain("<img");
  });
});

describe("countKinds", () => {
  it("splits files into videos, images and the rest", () => {
    expect(
      countKinds([
        { mime_type: "video/webm", filename: "a.webm" },
        { filename: "b.png" },
        { mime_type: "image/jpeg", filename: "c" },
        { mime_type: "application/json", filename: "take.json" },
        {},
      ]),
    ).toEqual({ video: 1, image: 2, other: 2 });
  });
});
