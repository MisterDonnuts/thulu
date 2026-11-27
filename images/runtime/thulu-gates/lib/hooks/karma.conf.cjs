// Karma config thulu-gates passes instead of the project's (`ng test --karma-config`, or
// `karma start`). It loads the project's config untouched and then: runs once without watching,
// leaves out quarantined specs with karma-jasmine's --grep, sends coverage to the gate's output
// directory instead of the repo, and adds a reporter that appends one JSON line per spec that
// starts or ends to $THULU_GATES_PROGRESS, so a run cut short by a hang still says which spec never
// finished. It lives outside the client repo; every path comes from the environment.

const { appendFileSync } = require("node:fs");
const { dirname, isAbsolute, join, resolve } = require("node:path");

const base = process.env.THULU_GATES_KARMA_CONFIG;
const progressFile = process.env.THULU_GATES_PROGRESS;
const grep = process.env.THULU_GATES_GREP || "";
const coverageDir = process.env.THULU_GATES_COVERAGE_DIR || "";

function write(line) {
  if (!progressFile) return;
  try {
    appendFileSync(progressFile, JSON.stringify(line) + "\n");
  } catch {
    // the gate reads what it can
  }
}

function ProgressReporter(baseReporterDecorator, emitter) {
  baseReporterDecorator(this);
  // The base decorator prints nothing for these; the project's own reporters do the talking.
  this.adapters = [];
  emitter.on("browser_info", (browser, info) => {
    if (info && info.thuluGates) write({ event: "start", name: info.thuluGates.name, browser: browser.name });
  });
  this.onSpecComplete = (browser, r) => {
    const name = [...(r.suite || []), r.description].join(" ");
    const status = r.skipped || r.disabled || r.pending ? "skip" : r.success ? "pass" : "fail";
    write({ event: "end", name, status, message: String((r.log || [])[0] || "").split("\n")[0], browser: browser.name });
  };
  this.onBrowserError = (browser, error) => {
    write({ event: "error", message: String((error && error.message) || error || "").split("\n")[0], browser: browser.name });
  };
}
ProgressReporter.$inject = ["baseReporterDecorator", "emitter"];

module.exports = function thuluGatesKarma(config) {
  let project = require(base);
  if (project && typeof project !== "function" && typeof project.default === "function") project = project.default;
  project(config);
  // Karma resolves a relative basePath against the config file, which is now this one.
  const basePath = config.basePath && isAbsolute(config.basePath) ? config.basePath : resolve(dirname(base), config.basePath || "");
  config.set({ basePath, singleRun: true, autoWatch: false, restartOnFileChange: false });
  const client = config.client || {};
  if (grep) client.args = [...(client.args || []), "--grep", grep];
  config.set({ client });
  config.set({ files: [...(config.files || []), { pattern: join(__dirname, "karma-client.js"), included: true, watched: false, served: true }] });
  config.set({ plugins: [...(config.plugins || ["karma-*"]), { "reporter:thulu-gates-progress": ["type", ProgressReporter] }] });
  config.set({ reporters: [...(config.reporters || ["progress"]), "thulu-gates-progress"] });
  if (coverageDir) {
    if (config.coverageReporter) config.set({ coverageReporter: { ...config.coverageReporter, dir: coverageDir, subdir: ".", reporters: [{ type: "lcovonly", subdir: ".", file: "lcov.info" }] } });
    if (config.coverageIstanbulReporter) config.set({ coverageIstanbulReporter: { ...config.coverageIstanbulReporter, dir: coverageDir, reports: ["lcovonly"] } });
  }
};
