import { readFile } from "node:fs/promises";

import { getBrowser } from "../lib/browser.js";
import { clock } from "./report.js";

const MAX_CELLS = 12;
const SHEET_WIDTH = 1200;

export function pickChapters(chapters) {
  if (chapters.length <= MAX_CELLS) return chapters;
  const step = (chapters.length - 1) / (MAX_CELLS - 1);
  return Array.from({ length: MAX_CELLS }, (_, i) => chapters[Math.round(i * step)]);
}

const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

/**
 * @param {{ title: string, cells: { frames: string[], label: string, at: number }[] }} input
 *        frames are JPEG paths; two per cell for a before/after sheet.
 * @returns {Promise<Buffer>} JPEG
 */
export async function contactSheet({ title, cells }) {
  const pairs = cells.some((c) => c.frames.length > 1);
  const cols = pairs ? 2 : 3;
  const figures = await Promise.all(cells.map(async (c) => {
    const imgs = await Promise.all(c.frames.filter(Boolean).map(async (f) =>
      `<img src="data:image/jpeg;base64,${(await readFile(f)).toString("base64")}">`));
    return `<figure><div class="imgs">${imgs.join("")}</div>
      <figcaption><b>${clock(c.at)}</b> ${esc(c.label)}</figcaption></figure>`;
  }));
  // i18n-ignore markup and CSS; the visible text is the title and the captions
  const html = `<!doctype html><meta charset="utf-8"><style>
    *{box-sizing:border-box;margin:0}
    body{background:#0B1220;color:#EAF2FF;font:13px/1.35 ui-sans-serif,-apple-system,"Segoe UI",Roboto,sans-serif;width:${SHEET_WIDTH}px}
    #sheet{padding:16px}h1{font-size:16px;margin-bottom:12px}
    .grid{display:grid;grid-template-columns:repeat(${cols},1fr);gap:12px}
    figure{background:#121A2B;border-radius:8px;overflow:hidden}
    .imgs{display:flex;gap:2px}.imgs img{flex:1;min-width:0;display:block;width:100%}
    figcaption{padding:6px 8px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
    b{color:#00C2FF;font-variant-numeric:tabular-nums;margin-right:4px}
  </style><div id="sheet"><h1>${esc(title)}</h1><div class="grid">${figures.join("")}</div></div>`;

  const browser = await getBrowser();
  const context = await browser.newContext({ viewport: { width: SHEET_WIDTH, height: 800 } });
  try {
    const page = await context.newPage();
    await page.setContent(html, { waitUntil: "load" });
    return await page.locator("#sheet").screenshot({ type: "jpeg", quality: 72 });
  } finally {
    await context.close();
  }
}
