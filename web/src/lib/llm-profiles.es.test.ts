import { describe, expect, it, vi } from "vitest";

vi.mock("virtual:octopus-messages", async () => {
  const { loadMessages } = await import("../../i18n.vite.mjs");
  return { locale: "es", sourceLocale: "en", locales: ["en", "es"], messages: loadMessages("es") };
});

const { keyBadge, keyNoteText, refusedText } = await import("./llm-profiles");

describe("key check notes in a Spanish page", () => {
  const base = { id: "p1", name: "p1", engine: "claude" };

  it("words a host that did not answer", () => {
    const p = {
      ...base,
      key_state: "unreachable" as const,
      key_reason: "no_answer" as const,
      key_host: "api.anthropic.com",
    };
    expect(keyNoteText(p)).toBe(
      "No se pudo contactar a api.anthropic.com. Probá Volver a verificar más tarde.",
    );
    expect(keyBadge(p)?.label).toBe("Sin respuesta");
  });

  it("words a rejected key and a refused run", () => {
    expect(
      keyNoteText({ ...base, key_state: "invalid", key_reason: "rejected", key_status: 401 }),
    ).toBe("El proveedor rechazó la clave (HTTP 401).");
    expect(refusedText({ ...base, refused_status: 403 })).toBe(
      "El proveedor rechazó una llamada de una corrida con esta clave (HTTP 403).",
    );
  });
});
