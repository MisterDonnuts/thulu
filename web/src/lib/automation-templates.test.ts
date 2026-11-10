import { describe, expect, it } from "vitest";

import { localizeSpec, TEMPLATE_SOURCES, TEMPLATES } from "./automation-templates";

const catalogs = import.meta.glob<Record<string, unknown>>(
  "../i18n/locales/*/lib/automation_templates.json",
  { eager: true, import: "default" },
);
const locales = Object.keys(catalogs).map((path) => path.split("/")[3]);

function catalogValue(locale: string, key: string): string {
  let node: unknown = catalogs[`../i18n/locales/${locale}/lib/automation_templates.json`];
  for (const part of key.replace(/^lib\.automation_templates\./, "").split(".")) {
    node = node && typeof node === "object" ? (node as Record<string, unknown>)[part] : undefined;
  }
  return typeof node === "string" ? node : "";
}

describe("template specs", () => {
  it("name every phrase they translate", () => {
    for (const src of TEMPLATE_SOURCES) {
      for (const phrase of Object.keys(src.texts)) {
        expect(src.spec, `phrase not in the spec: ${phrase}`).toContain(phrase);
      }
    }
  });

  it("render the source language unchanged", () => {
    TEMPLATE_SOURCES.forEach((src, i) => expect(TEMPLATES[i].spec).toBe(src.spec));
  });

  it("swap each phrase for its catalog message", () => {
    const out = localizeSpec("name: my automation\n", {
      "my automation": "lib.automation_templates.blank.spec.name",
    });
    expect(out).toBe(`name: ${catalogValue("en", "lib.automation_templates.blank.spec.name")}\n`);
  });

  it("keep every translation safe to drop into YAML", () => {
    expect(locales).toEqual(expect.arrayContaining(["en", "es"]));
    for (const locale of locales) {
      for (const src of TEMPLATE_SOURCES) {
        for (const key of Object.values(src.texts)) {
          const v = catalogValue(locale, key);
          expect(v, `${locale} ${key} is missing`).not.toBe("");
          expect(v, `${locale} ${key}`).not.toMatch(/["\\\n]/);
          if (/\.(name|description)$/.test(key)) {
            expect(v, `${locale} ${key}`).not.toMatch(/: | #|:$|^[^\p{L}]/u);
          }
        }
      }
    }
  });
});
