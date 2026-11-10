// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { sanitizeMarkdownHTML } from "./sanitize";

describe("sanitizeMarkdownHTML", () => {
  it.each([
    ['<img src=x onerror="alert(1)">', "onerror"],
    ["<script>alert(1)</script>", "<script"],
    ["<iframe src=https://a></iframe>", "<iframe"],
    ["<svg><a href=javascript:alert(1)>x</a></svg>", "javascript"],
    ['<a href="javascript:alert(1)">x</a>', "javascript"],
    ['<a href="//evil.example.com">x</a>', "evil.example.com"],
    ['<img src="/api/tasks/1">', "/api/tasks"],
    ['<img src="data:image/svg+xml,x">', "data:"],
    ['<td style="background:url(https://track.example)">x</td>', "track.example"],
    ['<input type="image" src="https://a/b.png">', "image"],
    ['<form action="https://a"><button>x</button></form>', "<form"],
    ['<p data-other="1">x</p>', "data-other"],
  ])("strips %s", (html, needle) => {
    expect(sanitizeMarkdownHTML(html)).not.toContain(needle);
  });

  it("forces rel=noopener on links that open a new tab", () => {
    const out = sanitizeMarkdownHTML('<a href="https://a" target="_self" rel="opener">x</a>');
    expect(out).toContain('target="_blank"');
    expect(out).toContain('rel="noopener noreferrer"');
  });

  it("keeps what the renderer produces", () => {
    const html =
      '<ul><li class="md-task done"><input type="checkbox" class="md-task-check" ' +
      'data-sec="plan" data-idx="1" checked disabled> <span class="md-task-text">a</span></li></ul>' +
      '<table class="md-table"><tbody><tr><td style="text-align:center">1</td></tr></tbody></table>' +
      '<video class="md-media" src="/task-attachments/v" controls preload="metadata" playsinline></video>';
    const out = sanitizeMarkdownHTML(html);
    for (const needle of [
      'type="checkbox"',
      'data-sec="plan"',
      'data-idx="1"',
      "checked",
      "disabled",
      'style="text-align:center"',
      'src="/task-attachments/v"',
      "controls",
      "playsinline",
    ])
      expect(out).toContain(needle);
  });
});
