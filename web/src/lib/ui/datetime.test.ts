// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { t } from "../../i18n";
import { fieldHTML } from "./form";
import {
  bindDateInputs,
  dateErrorId,
  dateInputError,
  dateInputHTML,
  dateInputsValid,
  dateInvalidMessage,
  datePlaceholder,
  formatDateValue,
  parseDateText,
  readDateInput,
  writeDateInput,
} from "./datetime";

// Intl puts a narrow no-break space before AM/PM in English.
const plain = (s: string) => s.replace(/[\u00a0\u202f]/g, " ");

describe("formatDateValue", () => {
  it("writes the moment the way each language does", () => {
    expect(formatDateValue("2026-09-17T17:04", "datetime", "es")).toBe("17/09/2026, 17:04");
    expect(plain(formatDateValue("2026-09-17T17:04", "datetime", "en"))).toBe(
      "09/17/2026, 05:04 PM",
    );
  });

  it("writes a day and a time of day alone", () => {
    expect(formatDateValue("2026-09-17", "date", "es")).toBe("17/09/2026");
    expect(formatDateValue("09:00", "time", "es")).toBe("09:00");
    expect(plain(formatDateValue("21:30", "time", "en"))).toBe("09:30 PM");
  });

  it("leaves an empty or malformed value empty", () => {
    expect(formatDateValue("", "datetime", "es")).toBe("");
    expect(formatDateValue("yesterday", "datetime", "es")).toBe("");
  });
});

describe("parseDateText", () => {
  it("reads day before month in Spanish, month before day in English", () => {
    expect(parseDateText("17/09/2026, 17:04", "datetime", "es")).toBe("2026-09-17T17:04");
    expect(parseDateText("09/17/2026, 05:04 PM", "datetime", "en")).toBe("2026-09-17T17:04");
    expect(parseDateText("3/4/2026 10:00", "datetime", "es")).toBe("2026-04-03T10:00");
    expect(parseDateText("3/4/2026 10:00", "datetime", "en")).toBe("2026-03-04T10:00");
  });

  it("takes loose typing: any separator, a short year, no time", () => {
    expect(parseDateText("17-9-26 9.5", "datetime", "es")).toBe("2026-09-17T09:05");
    expect(parseDateText("17/9/2026", "datetime", "es")).toBe("2026-09-17T00:00");
    expect(parseDateText("9/17/2026 12 am", "datetime", "en")).toBe("2026-09-17T00:00");
    expect(parseDateText("9/17/2026 12:30 p.m.", "datetime", "en")).toBe("2026-09-17T12:30");
    expect(parseDateText("9/17/2026 17:30", "datetime", "en")).toBe("2026-09-17T17:30");
  });

  it("reads a day and a time of day", () => {
    expect(parseDateText("17/09/2026", "date", "es")).toBe("2026-09-17");
    expect(parseDateText("9:00", "time", "es")).toBe("09:00");
    expect(parseDateText("9:00 PM", "time", "en")).toBe("21:00");
  });

  it("answers empty for an empty box and null for what is no date", () => {
    expect(parseDateText("  ", "datetime", "es")).toBe("");
    expect(parseDateText("31/02/2026", "datetime", "es")).toBeNull();
    expect(parseDateText("17/09/2026 25:00", "datetime", "es")).toBeNull();
    expect(parseDateText("17/09", "datetime", "es")).toBeNull();
    expect(parseDateText("13:00 PM", "time", "en")).toBeNull();
    expect(parseDateText("1/2/3/4/5/6", "datetime", "es")).toBeNull();
  });

  it("reads back what it writes, in both languages", () => {
    for (const loc of ["es", "en"]) {
      for (const v of ["2026-01-05T00:00", "2026-12-31T23:59", "2026-07-04T12:00"]) {
        expect(parseDateText(formatDateValue(v, "datetime", loc), "datetime", loc)).toBe(v);
      }
    }
  });
});

describe("datePlaceholder", () => {
  it("follows the language's order with the catalog's letters", () => {
    const d = t("ui.date.day"),
      m = t("ui.date.month"),
      y = t("ui.date.year");
    expect(datePlaceholder("date", "es")).toBe(`${d}/${m}/${y}`);
    expect(datePlaceholder("date", "en")).toBe(`${m}/${d}/${y}`);
    expect(datePlaceholder("time", "es")).toBe(`${t("ui.date.hour")}:${t("ui.date.minute")}`);
  });

  it("names both periods of a 12-hour clock, not the sample's", () => {
    const hm = `${t("ui.date.hour")}:${t("ui.date.minute")}`;
    expect(plain(datePlaceholder("time", "en"))).toBe(`${hm} AM/PM`);
    expect(plain(datePlaceholder("datetime", "en")).endsWith(`, ${hm} AM/PM`)).toBe(true);
    const dmy = `${t("ui.date.day")}/${t("ui.date.month")}/${t("ui.date.year")}`;
    expect(datePlaceholder("datetime", "es")).toBe(`${dmy}, ${hm}`);
  });
});

describe("the field in a page", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("labels its picker button and points it at the box", () => {
    document.body.innerHTML = dateInputHTML({ id: "f-from" });
    const btn = document.querySelector("[data-date-pick]");
    expect(btn?.getAttribute("aria-label")).toBe(t("ui.date.pick_datetime"));
    expect(btn?.getAttribute("aria-controls")).toBe("f-from");
    const native = document.querySelector(".date-input-native");
    expect(native?.getAttribute("type")).toBe("datetime-local");
    expect(native?.getAttribute("tabindex")).toBe("-1");
  });

  it("writes, reads and marks a text that is no date", () => {
    document.body.innerHTML = dateInputHTML({ id: "f-from" });
    bindDateInputs(document);
    writeDateInput("f-from", "2026-09-17T17:04");
    const text = document.getElementById("f-from") as HTMLInputElement;
    expect(text.value).toBe(formatDateValue("2026-09-17T17:04"));
    expect(readDateInput("f-from")).toBe("2026-09-17T17:04");
    expect((document.querySelector(".date-input-value") as HTMLInputElement).value).toBe(
      "2026-09-17T17:04",
    );

    text.value = "not a date";
    text.dispatchEvent(new Event("input", { bubbles: true }));
    expect(text.hasAttribute("aria-invalid")).toBe(false);
    expect(readDateInput("f-from")).toBe("");

    writeDateInput("f-from", "");
    expect(text.value).toBe("");
    expect(text.hasAttribute("aria-invalid")).toBe(false);
  });

  it("says under the box, and to a screen reader, why its text is no date", () => {
    document.body.innerHTML = fieldHTML({
      label: "From",
      for: "f-from",
      control: dateInputHTML({ id: "f-from" }),
    });
    bindDateInputs(document);
    const text = document.getElementById("f-from") as HTMLInputElement;
    const line = document.getElementById(dateErrorId("f-from")) as HTMLElement;
    expect(line.hidden).toBe(true);

    text.value = "nada";
    text.dispatchEvent(new Event("input", { bubbles: true }));
    text.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    expect(text.getAttribute("aria-invalid")).toBe("true");
    expect(line.hidden).toBe(false);
    expect(line.textContent).toBe(dateInvalidMessage("datetime"));
    expect(text.getAttribute("aria-describedby")?.split(" ")).toContain(line.id);
    expect(text.hasAttribute("title")).toBe(false);

    text.value = formatDateValue("2026-09-17T10:00");
    text.dispatchEvent(new Event("input", { bubbles: true }));
    expect(text.hasAttribute("aria-invalid")).toBe(false);
    expect(line.hidden).toBe(true);
    expect(text.hasAttribute("aria-describedby")).toBe(false);
  });

  it("lets a page tell a wrong date from an empty box before it acts", () => {
    document.body.innerHTML =
      fieldHTML({ label: "From", for: "f-from", control: dateInputHTML({ id: "f-from" }) }) +
      fieldHTML({ label: "To", for: "f-to", control: dateInputHTML({ id: "f-to" }) });
    bindDateInputs(document);
    expect(dateInputsValid(["f-from", "f-to"])).toBe(true);
    expect(dateInputError("f-from")).toBe("");

    const to = document.getElementById("f-to") as HTMLInputElement;
    to.value = "31/02/2026";
    expect(readDateInput(to)).toBe("");
    expect(dateInputError(to)).toBe(dateInvalidMessage("datetime"));
    expect(dateInputsValid(["f-from", "f-to"], { focus: true })).toBe(false);
    expect(document.activeElement).toBe(to);
    expect(document.getElementById(dateErrorId("f-to"))?.hidden).toBe(false);
  });

  it("says a time field holds no time in a time's words", () => {
    expect(dateInvalidMessage("time", "es")).toBe(
      t("ui.date.invalid_time", { example: datePlaceholder("time", "es") }),
    );
  });

  it("hands a pick from the browser's picker to the box as input and change", () => {
    document.body.innerHTML = dateInputHTML({ id: "ef-time", mode: "time", value: "09:00" });
    bindDateInputs(document);
    const text = document.getElementById("ef-time") as HTMLInputElement;
    const heard: string[] = [];
    text.addEventListener("input", () => heard.push("input"));
    text.addEventListener("change", () => heard.push(`change:${readDateInput(text)}`));
    const native = document.querySelector<HTMLInputElement>(".date-input-native");
    if (!native) throw new Error("no native picker");
    native.value = "18:30";
    native.dispatchEvent(new Event("change", { bubbles: true }));
    expect(heard).toEqual(["input", "change:18:30"]);
    expect(readDateInput("ef-time")).toBe("18:30");
  });
});
