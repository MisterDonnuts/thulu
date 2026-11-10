// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { chatMd } from "./chat-markdown";

describe("chatMd", () => {
  it("renders a quoted reply as a quote", () => {
    const html = chatMd("> ¿redondea antes?\nDespués.");
    expect(html).toBe("<blockquote>\n<p>¿redondea antes?</p>\n</blockquote>\n<p>Después.</p>");
    expect(html).not.toContain("&gt;");
  });

  it("renders nothing for an empty message", () => {
    expect(chatMd("")).toBe("");
    expect(chatMd("  \n ")).toBe("");
  });

  it("keeps a typed line break inside a paragraph", () => {
    expect(chatMd("hola\nchau")).toBe("<p>hola<br>chau</p>");
  });

  it("leaves blank-line paragraphs as separate paragraphs", () => {
    expect(chatMd("uno\n\ndos")).toBe("<p>uno</p>\n<p>dos</p>");
  });

  it("does not put breaks inside lists, headings or code blocks", () => {
    const html = chatMd("## Plan\n- a\n- b\n```\nx\ny\n```\nfin");
    expect(html).not.toContain("<br>");
    expect(html).toContain("<h2>Plan</h2>");
    expect(html).toContain("<li>a</li>");
    expect(html).toContain("<pre><code>x\ny</code></pre>");
  });

  it("renders markdown the way md() does", () => {
    const html = chatMd("**bold** and `code`\n\n| a | b |\n|---|---|\n| 1 | 2 |");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<code>code</code>");
    expect(html).toContain('<table class="md-table">');
  });

  it("keeps the mark out of a URL that ends a line", () => {
    const html = chatMd("see https://example.com/x\nnext");
    expect(html).toContain('href="https://example.com/x"');
    expect(html).toContain("<br>next");
  });

  it("puts the daemon origin in front of attachment paths", () => {
    const html = chatMd("![shot.png](/task-attachments/abc123)", {
      mediaBase: "http://daemon:8585",
    });
    expect(html).toContain('src="http://daemon:8585/task-attachments/abc123"');
  });

  it("escapes raw HTML", () => {
    const box = document.createElement("div");
    box.innerHTML = chatMd("<img src=x onerror=alert(1)>\n<script>alert(1)</script>");
    expect(box.querySelector("script, img")).toBeNull();
    expect(box.textContent).toContain("<script>");
  });

  it("drops a mark character the author typed", () => {
    expect(chatMd("a\uE000b")).toBe("<p>ab</p>");
  });
});
