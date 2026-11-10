import { describe, expect, it } from "vitest";
import {
  HOOK_TEMPLATE,
  orphansHTML,
  parseStageRef,
  stageEditorHTML,
  stageSetsHTML,
  type StageFiles,
} from "./stage-files";

const files: StageFiles = {
  stage_dir: ".octopus/stages",
  sets: [
    {
      kind: "prompts",
      title: "Stage prompts",
      description: "Injected when an agent launches.",
      stages: [
        { stage: "build", label: "Build", path: "prompts/build.md", exists: true },
        { stage: "review", label: "Review", path: "prompts/review.md", exists: false },
      ],
    },
  ],
};

describe("stageSetsHTML", () => {
  it("follows the catalog's stage order, not the payload's", () => {
    const html = stageSetsHTML(files, ["review", "build"]);
    expect(html.indexOf("Review")).toBeLessThan(html.indexOf("Build"));
  });
  it("skips a stage the project has no entry for", () => {
    const html = stageSetsHTML(files, ["build", "ship"]);
    expect(html).not.toContain("ship");
    expect(html.match(/data-sf-stage=/g)?.length).toBe(1);
  });
  it("gives an existing file edit and delete, a missing one only create", () => {
    const html = stageSetsHTML(files, ["build", "review"]);
    expect(html).toContain('data-sf-del="prompts|build"');
    expect(html).toContain('data-sf-edit="prompts|review"');
    expect(html).not.toContain('data-sf-del="prompts|review"');
  });
  it("shows the path for a file that exists and 'Not set' for one that does not", () => {
    const html = stageSetsHTML(files, ["build", "review"]);
    expect(html).toContain("prompts/build.md");
    expect(html).toContain("Not set");
    expect(html).toContain('<span class="file-status file-status-mono" data-tone="ok">');
    expect(html).toContain(
      '<span class="file-status"><span class="file-status-text">Not set</span>',
    );
  });
  it("draws each stage as a file row, the one that exists with the ok tone", () => {
    const html = stageSetsHTML(files, ["build", "review"]);
    expect(html.match(/class="row row-boxed file-row"/g)?.length).toBe(2);
    expect(html).toContain('data-tone="ok"');
    expect(html.match(/data-tone=/g)?.length).toBe(1);
  });
  it("names every action in words beside its glyph", () => {
    const html = stageSetsHTML(files, ["build", "review"]);
    expect(html).not.toContain("icon-btn");
    expect(html).toContain(">Edit<");
    expect(html).toContain(">Create<");
  });
  it("adds nothing when there are no orphans", () => {
    expect(stageSetsHTML(files, ["build"])).not.toContain("Orphaned");
  });
});

describe("orphansHTML", () => {
  it("is empty for none", () => {
    expect(orphansHTML([])).toBe("");
  });
  it("names them and offers the clean-up", () => {
    const html = orphansHTML(["prompts/old.md", "hooks/gone.sh"]);
    expect(html).toContain("prompts/old.md, hooks/gone.sh");
    expect(html).toContain('id="sf-prune"');
  });
  it("escapes a filename", () => {
    expect(orphansHTML(['<img src=x onerror="y">'])).not.toContain("<img");
  });
});

describe("stageEditorHTML", () => {
  const entry = { stage: "build", label: "Build", path: "prompts/build.md", exists: true };

  it("starts with Save disabled on an existing file, enabled on a new one", () => {
    const save = (html: string) => /<button id="sf-save"[^>]*>/.exec(html)?.[0] ?? "";
    expect(save(stageEditorHTML(entry, "x"))).toMatch(/ disabled[ >]/);
    expect(stageEditorHTML({ ...entry, exists: false }, "")).not.toContain("disabled");
  });
  it("marks a file that does not exist yet", () => {
    expect(stageEditorHTML({ ...entry, exists: false }, "")).toContain("(new)");
    expect(stageEditorHTML(entry, "")).not.toContain("(new)");
  });
  it("draws Save as the primary button with its shortcut, and Back with its word", () => {
    const html = stageEditorHTML(entry, "");
    expect(html).toMatch(/class="btn btn-primary[^"]*"/);
    expect(html).toContain('aria-keyshortcuts="Meta+S Control+S"');
    expect(html).toMatch(/<button[^>]* id="sf-back"[^>]*>.*Back.*?<\/button>/);
  });
  it("names the textarea after the path above it", () => {
    const html = stageEditorHTML(entry, "");
    expect(html).toContain('id="sf-path"');
    expect(html).toMatch(/<textarea id="sm-text"[^>]* aria-labelledby="sf-path">/);
  });
  it("is a boxed file editor with a quiet save state", () => {
    const html = stageEditorHTML(entry, "");
    expect(html).toMatch(/^<div class="md-editor md-editor-box">/);
    expect(html).toMatch(/<span id="sf-state" class="save-state"[^>]* hidden>/);
  });
  it("escapes the file's content into the textarea", () => {
    const html = stageEditorHTML(entry, "</textarea><script>alert(1)</script>");
    expect(html).not.toContain("</textarea><script>");
  });
});

describe("parseStageRef", () => {
  it("splits the pair", () => {
    expect(parseStageRef("hooks|build")).toEqual({ kind: "hooks", stage: "build" });
  });
  it("gives empty strings rather than undefined for a malformed ref", () => {
    expect(parseStageRef("hooks")).toEqual({ kind: "hooks", stage: "" });
    expect(parseStageRef("")).toEqual({ kind: "", stage: "" });
  });
});

describe("HOOK_TEMPLATE", () => {
  it("states the interpreter, the working directory and the failure mode", () => {
    expect(HOOK_TEMPLATE).toContain("#!/usr/bin/env bash");
    expect(HOOK_TEMPLATE).toContain("/workspace/projects/");
    expect(HOOK_TEMPLATE).toContain("set -e");
  });
});
