// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeEach, describe, expect, it } from "vitest";

// Vitest hands a CSS import over empty, so the stylesheet is read from disk.
const css = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "..", "src", "styles", "activity.css"),
  "utf8",
);

// Mirrors threadHTML() in pages/tasks.astro: the typing slot sits in the replies box, which
// carries is-empty while the thread has no replies.
function thread(typing) {
  document.body.innerHTML = `<div class="activity"><article class="act-thread">
    <div class="act-c">root</div>
    <div class="act-replies is-empty"><div class="typing-row thread-typing" data-thread-typing="r1">${typing}</div></div>
  </article></div>`;
  return document.querySelector(".act-replies");
}

describe("a thread with no replies", () => {
  beforeEach(() => {
    document.head.innerHTML = `<style>${css}</style>`;
  });

  it("hides its replies box while nobody is answering", () => {
    expect(getComputedStyle(thread("")).display).toBe("none");
  });

  it("shows the agent answering it", () => {
    const line = `<div class="act-c act-typing-line"><span class="typing-who"><strong>dev</strong> is replying…</span></div>`;
    expect(getComputedStyle(thread(line)).display).not.toBe("none");
  });
});
