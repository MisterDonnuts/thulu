import { describe, expect, it, vi } from "vitest";

vi.mock("virtual:octopus-messages", async () => {
  const { loadMessages } = await import("../../i18n.vite.mjs");
  return { locale: "es", sourceLocale: "en", locales: ["en", "es"], messages: loadMessages("es") };
});

const { levelLabel, modeNote, workerReasonText } = await import("./token-optimizer");
const { categoryLabel } = await import("./usage-breakdown");

describe("Large reads in Spanish", () => {
  it("names the levels", () => {
    expect(levelLabel("suggest")).toBe("Sugerir");
    expect(levelLabel("enforce")).toBe("Obligar");
    expect(categoryLabel("unattributed")).toBe("Sin atribuir");
  });

  it("says when enforce runs as suggest", () => {
    expect(modeNote("enforce", "codex").text).toBe(
      "Este agente no corre en Claude Code, así que Obligar funciona como Sugerir.",
    );
  });

  it("says why an engine has no worker", () => {
    expect(workerReasonText("no_default_profile")).toBe(
      "no hay credencial predeterminada para este engine",
    );
  });
});
