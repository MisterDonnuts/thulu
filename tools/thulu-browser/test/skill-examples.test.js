import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { compileSteps } from "../evidence/library.js";
import { parseLibrary, parseTake } from "../evidence/schema.js";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)),
  "../../../internal/builtin/system-skills/octopus-browser-evidence/examples/evidence");
const skip = existsSync(root) ? false : "the skill's examples are not in this checkout";
const json = (...p) => JSON.parse(readFileSync(path.join(root, ...p), "utf8"));

test("the example takes compile against the example library", { skip }, () => {
  const flows = Object.fromEntries(readdirSync(path.join(root, "flows"))
    .map((f) => [path.basename(f, ".json"), json("flows", f)]));
  const library = parseLibrary({ targets: json("targets.json"), flows });
  const takes = readdirSync(path.join(root, "takes"));
  assert.ok(takes.length > 0);
  for (const f of takes) {
    const steps = compileSteps(parseTake(json("takes", f)).steps, library);
    assert.ok(steps.every((s) => s.kind !== "use"), `${f} still has flows after compiling`);
    assert.ok(JSON.stringify(steps).indexOf('"@') === -1, `${f} still has @targets after compiling`);
  }
});
