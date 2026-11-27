import assert from "node:assert/strict";
import { test } from "node:test";

import { compileSteps } from "../evidence/library.js";
import { TakeError, parseLibrary, parseTake } from "../evidence/schema.js";

const take = (steps, extra = {}) => ({ version: 1, title: "t", base_url: "http://app.test", steps, ...extra });

test("a take gets the default viewport and accepts a JSON string", () => {
  const t = parseTake(JSON.stringify(take([{ kind: "goto", url: "/" }])));
  assert.deepEqual(t.viewport, { width: 1440, height: 900 });
  assert.equal(t.steps[0].url, "/");
});

test("every problem in a take is reported at once", () => {
  assert.throws(
    () => parseTake(take([{ kind: "click" }, { kind: "fly" }, { kind: "fill", target: "#a" }])),
    (err) => {
      assert.ok(err instanceof TakeError);
      assert.match(err.message, /steps\.0/);
      assert.match(err.message, /steps\.1/);
      assert.match(err.message, /steps\.2/);
      return true;
    },
  );
});

test("a fill takes a value or a secret name, never both", () => {
  assert.ok(parseTake(take([{ kind: "fill", target: "#p", secret: "password" }])));
  assert.throws(() => parseTake(take([{ kind: "fill", target: "#p", value: "x", secret: "password" }])), TakeError);
});

test("a scroll needs a direction or a target", () => {
  assert.throws(() => parseTake(take([{ kind: "scroll" }])), /direction or target/);
  assert.ok(parseTake(take([{ kind: "scroll", direction: "bottom" }])));
});

test("a verify is either measured (check + target) or reported (observed)", () => {
  assert.ok(parseTake(take([{ kind: "verify", case: "TC-1", expected: "3", check: "count", target: "li" }])));
  assert.ok(parseTake(take([{ kind: "verify", case: "TC-1", expected: "ok", observed: "ok" }])));
  assert.throws(() => parseTake(take([{ kind: "verify", case: "TC-1", expected: "ok" }])), TakeError);
});

const library = parseLibrary({
  targets: { save: "role=button[name=\"Save\"]", email: "[data-testid=email]" },
  flows: {
    login: {
      params: ["user"],
      steps: [
        { kind: "fill", target: "@email", value: "{{user}}@example.com", label: "Sign in as {{user}}" },
        { kind: "fill", target: "#password", secret: "password" },
        { kind: "click", target: "@save" },
      ],
    },
    outer: { steps: [{ kind: "use", flow: "login", params: { user: "alice" } }, { kind: "beat", ms: 500 }] },
    loop: { steps: [{ kind: "use", flow: "loop" }] },
  },
});

test("flows expand in place with their params, and @names resolve", () => {
  const steps = compileSteps(parseTake(take([{ kind: "use", flow: "outer" }])).steps, library);
  assert.deepEqual(steps.map((s) => s.kind), ["fill", "fill", "click", "beat"]);
  assert.equal(steps[0].target, "[data-testid=email]");
  assert.equal(steps[0].value, "alice@example.com");
  assert.equal(steps[0].label, "Sign in as alice");
  assert.equal(steps[1].secret, "password");
  assert.equal(steps[2].target, "role=button[name=\"Save\"]");
});

test("@names resolve only where a selector goes", () => {
  const steps = compileSteps(parseTake(take([
    { kind: "note", text: "mail @email later" },
    { kind: "shot", shot: { name: "s", spotlight: [{ target: "@save", note: "@save stays" }], redact: ["@email"] } },
  ])).steps, library);
  assert.equal(steps[0].text, "mail @email later");
  assert.equal(steps[1].shot.spotlight[0].target, "role=button[name=\"Save\"]");
  assert.equal(steps[1].shot.spotlight[0].note, "@save stays");
  assert.deepEqual(steps[1].shot.redact, ["[data-testid=email]"]);
});

test("a perf check names its metric and takes a number as the maximum, with no target or match", () => {
  const perf = (over) => take([{ kind: "verify", case: "TC-1", check: "perf", ...over }]);
  assert.equal(parseTake(perf({ metric: "lcp", expected: "2500" })).steps[0].metric, "lcp");
  assert.ok(parseTake(perf({ metric: "cls", expected: "0.1" })));
  assert.ok(parseTake(perf({ metric: "response", expected: "800" })));
  assert.throws(() => parseTake(perf({ expected: "2500" })), /needs a metric/);
  assert.throws(() => parseTake(perf({ metric: "lcp", expected: "fast" })), /a number/);
  assert.throws(() => parseTake(perf({ metric: "lcp", expected: "" })), /a number/);
  assert.throws(() => parseTake(perf({ metric: "lcp", expected: "-1" })), /a number/);
  assert.throws(() => parseTake(perf({ metric: "lcp", expected: "2500", match: "contains" })), /no match/);
  assert.throws(() => parseTake(perf({ metric: "lcp", expected: "2500", target: "#hero" })), /no target/);
  assert.throws(() => parseTake(perf({ metric: "tti", expected: "2500" })), TakeError);
  assert.throws(() => parseTake(take([{ kind: "verify", case: "TC-1", check: "text", target: "h1", expected: "x",
    metric: "lcp" }])), /only with check "perf"/);
});

test("a take carries its network, CPU and color scheme, and its ignore patterns", () => {
  const t = parseTake(take([{ kind: "goto", url: "/" }], {
    network: "slow-4g", cpu_throttle: 4, color_scheme: "dark", ignore_errors: ["analytics\\.example"],
  }));
  assert.equal(t.network, "slow-4g");
  assert.equal(t.cpu_throttle, 4);
  assert.equal(t.color_scheme, "dark");
  assert.deepEqual(t.ignore_errors, ["analytics\\.example"]);
  assert.throws(() => parseTake(take([{ kind: "goto", url: "/" }], { network: "5g" })), TakeError);
  assert.throws(() => parseTake(take([{ kind: "goto", url: "/" }], { cpu_throttle: 40 })), TakeError);
  assert.throws(() => parseTake(take([{ kind: "goto", url: "/" }], { color_scheme: "sepia" })), TakeError);
  assert.throws(() => parseTake(take([{ kind: "goto", url: "/" }], { ignore_errors: ["("] })), /regular expression/);
});

test("an a11y step takes its scope, tags and threshold, and @names resolve in its scope", () => {
  const parsed = parseTake(take([
    { kind: "a11y", case: "TC-3", include: ["@form"], exclude: ["#ads"], tags: ["wcag2aa"], fail_on: "critical", disable_rules: ["color-contrast"] },
    { kind: "a11y" },
  ]));
  const lib = parseLibrary({ targets: { form: "#checkout" } });
  const [scoped, plain] = compileSteps(parsed.steps, lib);
  assert.deepEqual(scoped.include, ["#checkout"]);
  assert.equal(scoped.fail_on, "critical");
  assert.equal(plain.include, undefined);
  assert.throws(() => parseTake(take([{ kind: "a11y", fail_on: "fatal" }])), TakeError);
  assert.throws(() => parseTake(take([{ kind: "a11y", target: "#x" }])), TakeError, "strict: an a11y step has no target");
});

test("cut_waits and a think note pass the strict schema", () => {
  const parsed = parseTake(take([{ kind: "note", text: "I expected the price here", level: "think" }], { cut_waits: false }));
  assert.equal(parsed.cut_waits, false);
  assert.equal(parsed.steps[0].level, "think");
  assert.throws(() => parseTake(take([{ kind: "goto", url: "/" }], { cut_waits: "no" })), TakeError);
});

test("compile problems are listed together: unknown flow, target, missing param, recursion", () => {
  const steps = parseTake(take([
    { kind: "use", flow: "nope" },
    { kind: "click", target: "@ghost" },
    { kind: "use", flow: "login" },
    { kind: "use", flow: "loop" },
  ])).steps;
  assert.throws(() => compileSteps(steps, library), (err) => {
    assert.ok(err instanceof TakeError);
    assert.match(err.message, /nope/);
    assert.match(err.message, /ghost/);
    assert.match(err.message, /user/);
    assert.match(err.message, /loop/);
    return true;
  });
});
