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
