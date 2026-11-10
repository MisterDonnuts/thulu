// Every caller must use its own BrowserContext on this shared browser, so cookies and storage never
// cross.

import { chromium } from "playwright";

let browserPromise = null;

export function getBrowser() {
  if (!browserPromise) {
    browserPromise = chromium.launch({
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    });
  }
  return browserPromise;
}

export async function closeBrowser() {
  if (!browserPromise) return;
  const browser = await browserPromise;
  browserPromise = null;
  await browser.close();
}
