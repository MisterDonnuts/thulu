import assert from "node:assert/strict";
import { test } from "node:test";

import { withLocale } from "../i18n.js";
import { NETWORK_PROFILES, describeProfile, emulation, throttled, withEmulation } from "../lib/throttle.js";

test("the network profiles are DevTools' presets in ms and bytes per second", () => {
  assert.deepEqual(NETWORK_PROFILES["slow-4g"], { offline: false, latency: 562.5, downloadThroughput: 180_000,
    uploadThroughput: 84_375 });
  assert.deepEqual(NETWORK_PROFILES["fast-4g"], { offline: false, latency: 165, downloadThroughput: 1_012_500,
    uploadThroughput: 168_750 });
  assert.deepEqual(NETWORK_PROFILES["3g"], { offline: false, latency: 2_000, downloadThroughput: 50_000,
    uploadThroughput: 50_000 });
  assert.equal(NETWORK_PROFILES.offline.offline, true);
  assert.equal(NETWORK_PROFILES.none.downloadThroughput, -1, "-1 turns throttling off");
});

test("a profile keeps only what slows the page or changes its look, and refuses what it does not know", () => {
  assert.deepEqual(emulation({}), {});
  assert.deepEqual(emulation({ network: "none", cpu_throttle: 1 }), {}, "neutral values drop out");
  assert.deepEqual(emulation({ network: "slow-4g", cpu_throttle: 4, color_scheme: "dark" }),
    { network: "slow-4g", cpu_throttle: 4, color_scheme: "dark" });
  assert.throws(() => emulation({ network: "5g" }), /unknown network profile "5g".*slow-4g/);
  assert.throws(() => emulation({ cpu_throttle: 0.5 }), /from 1 to 20/);
  assert.throws(() => emulation({ cpu_throttle: 50 }), /from 1 to 20/);
  assert.throws(() => emulation({ color_scheme: "sepia" }), /light or dark/);
  assert.equal(throttled({ color_scheme: "dark" }), false, "a color scheme alone does not slow anything");
  assert.equal(throttled({ cpu_throttle: 4 }), true);
});

test("a replay override replaces, clears or keeps each field of the take", () => {
  const take = { title: "t", network: "3g", cpu_throttle: 4, color_scheme: "dark" };
  assert.equal(withEmulation(take, {}), take, "no override, same take");
  assert.deepEqual(withEmulation(take, { network: "slow-4g" }), { ...take, network: "slow-4g" });
  assert.deepEqual(withEmulation(take, { network: "none", cpu_throttle: 1 }), { title: "t", color_scheme: "dark" });
  assert.deepEqual(withEmulation({ title: "t" }, { color_scheme: "light" }), { title: "t", color_scheme: "light" });
  assert.throws(() => withEmulation(take, { network: "lte" }), /unknown network profile/);
});

test("the profile reads in words, in the session's language", () => {
  assert.equal(withLocale("en", () => describeProfile({ network: "slow-4g", cpu_throttle: 4, color_scheme: "dark" })),
    "slow-4g · CPU 4x · dark");
  assert.equal(withLocale("es", () => describeProfile({}, { viewport: { width: 390, height: 844 } })),
    "390×844 · sin throttling");
});
