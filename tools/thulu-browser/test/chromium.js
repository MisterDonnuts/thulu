// Probe by launching: chromium.executablePath() is the full browser, but the image ships only the
// headless shell.
// Any other launch error is a broken install and fails instead of skipping.

import { chromium } from "playwright";

export async function chromiumMissing() {
  try {
    await (await chromium.launch()).close();
    return false;
  } catch (err) {
    if (err.message.includes("Executable doesn't exist")) return "Playwright Chromium is not installed";
    throw err;
  }
}
