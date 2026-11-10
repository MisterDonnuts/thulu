import assert from "node:assert/strict";
import { test } from "node:test";

import { t, withLocale } from "../i18n.js";

test("withLocale renders t() in the run's locale, across awaits", async () => {
  const plain = t("labels.mode_replay");
  const inSpanish = await withLocale("es-AR", async () => {
    await new Promise((r) => setTimeout(r, 1));
    return t("labels.mode_replay");
  });
  assert.equal(inSpanish, "reproducción");
  assert.equal(t("labels.mode_replay"), plain, "outside the call the server's locale stays");
  assert.equal(withLocale("", () => t("labels.mode_replay")), plain);
  assert.equal(withLocale("xx", () => t("labels.mode_replay")), "replay", "an unknown locale reads English");
});
