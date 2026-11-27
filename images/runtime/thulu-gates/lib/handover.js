import { spawnSync } from "node:child_process";
import { existsSync, realpathSync } from "node:fs";
import { constants } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// HOME_ENV names the copy of thulu-gates a runner shipped for the job; HANDED_ENV marks a run
// already handed over, so two copies that name each other never loop.
export const HOME_ENV = "THULU_GATES_HOME";
export const HANDED_ENV = "THULU_GATES_HANDED";

function real(p) {
  try {
    return realpathSync(p);
  } catch {
    return p;
  }
}

// handOver runs the job's copy when $THULU_GATES_HOME names one that is not this one, and returns
// its exit code; null means this copy runs. A home without the CLI in it is ignored: running the
// copy at hand beats failing the gates.
export function handOver(args, selfURL, { env = process.env, spawn = spawnSync, node = process.execPath } = {}) {
  const home = env[HOME_ENV];
  if (!home || env[HANDED_ENV]) return null;
  const self = dirname(dirname(fileURLToPath(selfURL)));
  const target = join(home, "bin", "thulu-gates.js");
  if (real(home) === real(self) || !existsSync(target)) return null;
  const r = spawn(node, [target, ...args], { stdio: "inherit", env: { ...env, [HANDED_ENV]: "1" } });
  if (r.error) return 1;
  if (r.signal) return 128 + (constants.signals[r.signal] ?? 1);
  return r.status ?? 1;
}
