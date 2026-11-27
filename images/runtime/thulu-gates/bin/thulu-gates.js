#!/usr/bin/env node
import { handOver } from "../lib/handover.js";

// A runner's job names the copy it shipped in $THULU_GATES_HOME. Another copy found first on PATH
// (one a person installed by hand, which no update reaches) hands the run to it, so a job always
// runs the gates of the runner that started it.
const handed = handOver(process.argv.slice(2), import.meta.url);
if (handed !== null) {
  process.exitCode = handed;
} else {
  const { main } = await import("../lib/cli.js");
  process.exitCode = await main(process.argv.slice(2));
}
