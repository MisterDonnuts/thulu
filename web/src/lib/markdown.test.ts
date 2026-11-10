// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { md, mdInline } from "./markdown";

function dangerous(html: string): string[] {
  const box = document.createElement("div");
  box.innerHTML = html;
  const found: string[] = [];
  for (const el of box.querySelectorAll("*")) {
    if (el.tagName === "SCRIPT" || el.tagName === "IFRAME" || el.tagName === "OBJECT")
      found.push(el.tagName);
    for (const a of el.attributes) {
      if (/^on/i.test(a.name)) found.push(`${el.tagName}[${a.name}]`);
      if (/^(href|src)$/i.test(a.name) && /^\s*(javascript|data|vbscript):/i.test(a.value))
        found.push(`${el.tagName}[${a.name}=${a.value}]`);
    }
  }
  return found;
}

describe("md() against hostile input", () => {
  it.each([
    '![x" onerror="alert(1)](https://a/b.png)',
    "![x' onerror='alert(1)](https://a/b.png)",
    '![x](https://a/b.png" onerror="alert(1))',
    '[click](https://a/" onmouseover="alert(1))',
    "[click](javascript:alert(1))",
    "[click](JaVaScRiPt:alert(1))",
    "[click]( javascript:alert(1))",
    "[click](data:text/html,<script>alert(1)</script>)",
    "[click](vbscript:msgbox(1))",
    "![x](javascript:alert(1))",
    "![x.mp4](javascript:alert(1))",
    "![x.pdf](javascript:alert(1))",
    "![x](data:image/svg+xml,<svg onload=alert(1)>)",
    "<img src=x onerror=alert(1)>",
    "<script>alert(1)</script>",
    "# <img src=x onerror=alert(1)>",
    "- <svg onload=alert(1)>",
    "> <iframe src=javascript:alert(1)>",
    "| a | b |\n|---|---|\n| <img src=x onerror=alert(1)> | ok |",
    "```\n<script>alert(1)</script>\n```",
    "`<img src=x onerror=alert(1)>`",
    'see https://a.example/"onmouseover="alert(1)',
    '![`" onerror="alert(1)`](https://a/b.png)',
  ])("renders %j without anything that executes", (src) => {
    expect(dangerous(md(src))).toEqual([]);
  });

  it("escapes the quote instead of dropping the image", () => {
    const html = md('![x" onerror="alert(1)](https://a/b.png)');
    const img = new DOMParser().parseFromString(html, "text/html").querySelector("img");
    expect(img?.getAttribute("alt")).toBe('x" onerror="alert(1)');
    expect(img?.getAttribute("src")).toBe("https://a/b.png");
  });

  it("keeps the text of a link whose scheme is refused", () => {
    const html = md("[click me](javascript:alert(1))");
    expect(html).toContain("click me");
    expect(html).not.toContain("<a");
  });

  it("refuses protocol-relative links to another host", () => {
    expect(md("[x](//evil.example.com)")).not.toContain("<a");
    expect(md("[x](/\\evil.example.com)")).not.toContain("<a");
  });

  it("escapes an attachment's filename used as the link text", () => {
    const html = mdInline("![](/task-attachments/f)", {
      attachment: () => ({ mime: "application/pdf", filename: "<img src=x onerror=alert(1)>" }),
    });
    expect(html).not.toContain("<img");
  });

  it("hardens every link that opens a new tab", () => {
    const a = new DOMParser()
      .parseFromString(md("[x](https://example.com)"), "text/html")
      .querySelector("a");
    expect(a?.getAttribute("rel")).toBe("noopener noreferrer");
  });
});

describe("md() keeps what it is meant to render", () => {
  it("renders a quote as a blockquote, without its >", () => {
    const html = md("Dice:\n\n> redondea antes\n> de prorratear\n\nfin");
    expect(html).toContain("<blockquote>");
    expect(html).toContain("<p>redondea antes</p>");
    expect(html).not.toContain("&gt;");
  });

  it("renders the full vocabulary", () => {
    const html = md(
      [
        "## Plan",
        "- [ ] first",
        "- [x] second",
        "1. one",
        "**bold** and *italic* and `code`",
        "[link](https://example.com/?a=1&b=2) and https://auto.example.com",
        "![shot.png](/task-attachments/abc)",
        "| l | c | r |",
        "|:--|:-:|--:|",
        "| 1 | 2 | 3 |",
        "```go",
        "x := 1",
        "```",
      ].join("\n"),
      { checklist: true },
    );
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.querySelector("h2")?.textContent).toBe("Plan");
    const boxes = doc.querySelectorAll<HTMLInputElement>("input.md-task-check");
    expect(boxes).toHaveLength(2);
    expect(boxes[0].dataset.sec).toBe("plan");
    expect(boxes[1].dataset.idx).toBe("2");
    expect(boxes[1].checked).toBe(true);
    expect(boxes[0].disabled).toBe(false);
    expect(doc.querySelector("ol li")?.textContent).toBe("one");
    expect(doc.querySelector("strong")?.textContent).toBe("bold");
    expect(doc.querySelector("em")?.textContent).toBe("italic");
    const links = [...doc.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(links).toEqual(["https://example.com/?a=1&b=2", "https://auto.example.com"]);
    expect(doc.querySelector("img")?.getAttribute("src")).toBe("/task-attachments/abc");
    const cells = [...doc.querySelectorAll("td")].map((td) => td.getAttribute("style"));
    expect(cells).toEqual(["text-align:left", "text-align:center", "text-align:right"]);
    expect(doc.querySelector("pre code.lang-go")?.textContent).toBe("x := 1");
  });

  it("renders a video player for a recording", () => {
    const html = md("![clip.mp4](/task-attachments/xyz)");
    expect(html).toContain("<video");
    expect(html).toContain("controls");
  });
});

describe("md: folded blocks", () => {
  it("takes the summary from the line under <details>", () => {
    const html = md("<details>\n<summary>Context</summary>\n\nbody\n</details>");
    expect(html).toContain('<details class="md-details"><summary>Context</summary>');
    expect(html).not.toContain("&lt;summary&gt;");
    expect(md("text\n\n<summary>loose</summary>")).toContain("&lt;summary&gt;");
  });

  it("rebuilds a report's <details> with its summary", () => {
    const html = md(
      "<details><summary>Chapters</summary>\n\n- `0:01.0` Open the cart\n\n</details>",
    );
    expect(html).toContain('<details class="md-details"><summary>Chapters</summary>');
    expect(html).toContain("<li><code>0:01.0</code> Open the cart</li>");
    expect(html).toContain("</details>");
  });

  it("closes a block the source left open and ignores a stray close", () => {
    expect(md("<details>\n\ntext")).toMatch(
      /<details class="md-details"><summary>Details<\/summary>[\s\S]*<\/details>$/,
    );
    expect(md("</details>")).toContain("&lt;/details&gt;");
  });

  it("keeps other HTML escaped, inside the summary too", () => {
    const html = md("<details><summary><img src=x onerror=alert(1)></summary>");
    expect(html).not.toContain("<img");
    expect(md("<details open>")).toContain("&lt;details open&gt;");
  });
});
