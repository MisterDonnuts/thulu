import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import {
  applyVerb,
  badgeOf,
  BundleError,
  choicesOf,
  choosable,
  copyActionText,
  exportAllFilename,
  exportFilename,
  filterEntries,
  itemKey,
  readBundleFile,
  resolutionsOf,
  resultIsEmpty,
  statusTone,
  tagLabel,
  tagsOf,
  updatesOf,
  writes,
  writesOf,
  withTypedNames,
  type PlanItem,
  type Plan,
  type WorkshopEntry,
} from "./workshop";

const entry = (over: Partial<WorkshopEntry>): WorkshopEntry => ({
  kind: "agent",
  name: "x",
  tags: [],
  revision: 1,
  status: "available",
  ...over,
});

const entries: WorkshopEntry[] = [
  entry({
    name: "changelog-writer",
    description: "Writes the CHANGELOG",
    tags: ["docs", "release"],
  }),
  entry({ name: "reviewer", description: "Reviews code", tags: ["review"] }),
  entry({ kind: "skill", name: "changelog-style", tags: ["docs"] }),
];

describe("filterEntries", () => {
  it("keeps one kind", () => {
    expect(filterEntries(entries, "skill", "").map((e) => e.name)).toEqual(["changelog-style"]);
  });
  it("matches every word against name, description and tags", () => {
    expect(filterEntries(entries, "agent", "write release").map((e) => e.name)).toEqual([
      "changelog-writer",
    ]);
    expect(filterEntries(entries, "agent", "write review")).toEqual([]);
  });
  it("matches a tag by the word the chip shows", () => {
    const word = tagLabel("release").split(" ")[0];
    expect(filterEntries(entries, "agent", word).map((e) => e.name)).toEqual(["changelog-writer"]);
  });
  it("requires every selected tag", () => {
    expect(filterEntries(entries, "agent", "", ["docs"]).map((e) => e.name)).toEqual([
      "changelog-writer",
    ]);
    expect(filterEntries(entries, "agent", "", ["docs", "review"])).toEqual([]);
  });
});

describe("tagsOf", () => {
  it("lists the tags of one kind, sorted and once", () => {
    expect(tagsOf(entries, "agent")).toEqual(["docs", "release", "review"]);
  });
});

describe("statusTone", () => {
  it("never leaves a status without a tone", () => {
    expect(statusTone("installed")).toBe("ok");
    expect(statusTone("update")).toBe("warn");
    expect(statusTone("available")).toBe("neutral");
  });
});

const plan: Plan = {
  source: "workshop",
  blocked: false,
  writes: 1,
  notices: [],
  items: [
    {
      kind: "agent",
      name: "a",
      action: "install",
      resolution: "install",
      options: ["install"],
      target: "a",
    },
    {
      kind: "skill",
      name: "s",
      action: "conflict",
      resolution: "keep",
      options: ["keep", "replace", "copy"],
      target: "s",
    },
  ],
};

describe("plan helpers", () => {
  it("only sends back the choices the person could make", () => {
    expect(choosable(plan).map(itemKey)).toEqual(["skill/s"]);
    expect(resolutionsOf(plan)).toEqual({ "skill/s": "keep" });
  });
  it("counts writes the way the daemon does", () => {
    expect(writesOf(plan)).toBe(1);
    const copy = {
      ...plan,
      items: plan.items.map((it) => ({ ...it, resolution: it.options.at(-1) ?? it.resolution })),
    };
    expect(writesOf(copy)).toBe(2);
  });
});

describe("readBundleFile", () => {
  it("parses JSON", async () => {
    const f = new File(['{"format":"octopus-workshop"}'], "b.json");
    await expect(readBundleFile(f)).resolves.toEqual({ format: "octopus-workshop" });
  });
  it("refuses what is not JSON and what is too large", async () => {
    await expect(readBundleFile(new File(["{"], "b.json"))).rejects.toBeInstanceOf(BundleError);
    await expect(readBundleFile(new File(["{}"], "b.json"), 1)).rejects.toMatchObject({
      code: "too_large",
    });
  });
});

describe("exportFilename", () => {
  it("names the download after the item", () => {
    expect(exportFilename("skill", "changelog-style")).toBe("octopus-skill-changelog-style.json");
  });
});

describe("skip", () => {
  it("writes nothing, like keep", () => {
    expect(writes("skip")).toBe(false);
    expect(writes("keep")).toBe(false);
    expect(writes("copy")).toBe(true);
    const skipped = {
      ...plan,
      items: plan.items.map((it) => ({ ...it, resolution: "skip" as const })),
    };
    expect(writesOf(skipped)).toBe(0);
  });
});

const item = (over: Partial<PlanItem>): PlanItem => ({
  kind: "agent",
  name: "a",
  action: "install",
  resolution: "install",
  options: ["install", "skip"],
  target: "a",
  ...over,
});
const planOf = (items: PlanItem[]): Plan => ({
  source: "workshop",
  blocked: false,
  writes: items.length,
  notices: [],
  items,
});

describe("applyVerb", () => {
  it("installs when every write is new, copies included", () => {
    expect(
      applyVerb(
        planOf([
          item({}),
          item({ kind: "skill", name: "s", resolution: "copy", target: "s-2" }),
          item({ name: "b", resolution: "keep" }),
        ]),
      ),
    ).toBe("install");
  });
  it("updates when every write is an update", () => {
    expect(
      applyVerb(
        planOf([item({ action: "update", resolution: "update" }), item({ resolution: "skip" })]),
      ),
    ).toBe("update");
  });
  it("applies when the writes mix", () => {
    expect(
      applyVerb(planOf([item({}), item({ name: "b", action: "conflict", resolution: "replace" })])),
    ).toBe("apply");
    expect(
      applyVerb(planOf([item({}), item({ name: "b", action: "update", resolution: "update" })])),
    ).toBe("apply");
  });
});

describe("choicesOf", () => {
  it("sends the names copies take, and nothing for the rest", () => {
    const p = planOf([
      item({}),
      item({
        kind: "skill",
        name: "octopus-x",
        action: "reserved",
        resolution: "copy",
        options: ["skip", "copy"],
        target: "x",
      }),
    ]);
    expect(choicesOf(p)).toEqual({
      resolutions: { "agent/a": "install", "skill/octopus-x": "copy" },
      names: { "skill/octopus-x": "x" },
    });
  });
});

describe("resultIsEmpty", () => {
  const none = { installed: [], updated: [], replaced: [], copied: [], kept: [], skipped: [] };
  it("ignores what was kept", () => {
    expect(resultIsEmpty({ ...none, kept: [{ kind: "skill", name: "s" }] })).toBe(true);
  });
  it("counts what was left out", () => {
    expect(resultIsEmpty({ ...none, skipped: [{ kind: "agent", name: "a" }] })).toBe(false);
  });
});

describe("updatesOf", () => {
  it("names the installed items of one kind that have an update", () => {
    const list = [
      entry({ name: "a", status: "update" }),
      entry({ name: "b", status: "installed" }),
      entry({ kind: "skill", name: "s", status: "update" }),
    ];
    expect([...updatesOf(list, "agent")]).toEqual(["a"]);
    expect([...updatesOf(list, "skill")]).toEqual(["s"]);
  });
});

describe("exportAllFilename", () => {
  it("names the download after the kind", () => {
    expect(exportAllFilename("agent")).toBe("octopus-agents.json");
    expect(exportAllFilename("skill")).toBe("octopus-skills.json");
  });
});

describe("tagLabel", () => {
  it("words the catalog's tags and leaves any other as written", () => {
    expect(tagLabel("docs")).toBe(t("workshop.tags.docs"));
    expect(tagLabel("team-only")).toBe("team-only");
  });
});

describe("withTypedNames", () => {
  const copy = item({
    kind: "skill",
    name: "checklist",
    action: "conflict",
    resolution: "copy",
    options: ["keep", "replace", "copy"],
    target: "checklist-2",
  });

  it("sends the name typed in the input over the plan's", () => {
    expect(withTypedNames(planOf([copy]), { "skill/checklist": " team-checklist " })).toEqual({
      resolutions: { "skill/checklist": "copy" },
      names: { "skill/checklist": "team-checklist" },
    });
  });

  it("is null when nothing new was typed", () => {
    expect(withTypedNames(planOf([copy]), { "skill/checklist": "checklist-2" })).toBeNull();
    expect(withTypedNames(planOf([copy]), { "skill/checklist": "  " })).toBeNull();
    expect(withTypedNames(planOf([copy]), { "agent/a": "b" })).toBeNull();
  });
});

describe("copyActionText", () => {
  it("names the copy a skill is held as, and stays out of every other item", () => {
    expect(copyActionText(item({ action: "same", as_copy: true, target: "style-2" }))).toBe(
      t("workshop.plan.action.copy_same", { target: "style-2" }),
    );
    expect(copyActionText(item({ action: "conflict", as_copy: true, target: "style-2" }))).toBe(
      t("workshop.plan.action.copy_conflict", { target: "style-2" }),
    );
    expect(t("workshop.plan.action.copy_same", { target: "style-2" })).toContain("style-2");
    expect(copyActionText(item({ action: "conflict" }))).toBeNull();
  });
});

describe("badgeOf", () => {
  it("says the workspace's own stays when it keeps something different", () => {
    expect(badgeOf(item({ action: "conflict", resolution: "keep" }))).toBe("keep_yours");
    expect(badgeOf(item({ action: "update", resolution: "keep" }))).toBe("keep_yours");
    expect(badgeOf(item({ action: "same", resolution: "keep" }))).toBe("keep");
    expect(badgeOf(item({ action: "conflict", resolution: "replace" }))).toBe("replace");
  });
});
