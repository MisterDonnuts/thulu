// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

import { ApiError } from "./api";
import { wireMdEditor } from "./mdeditor";
import { mdEditorHTML } from "./ui/catalog";

describe("wireMdEditor", () => {
  it("hands the save error to the page, so it can say why", async () => {
    document.body.innerHTML = `<textarea id="ta"></textarea><button id="save"></button><button id="back"></button>`;
    const err = new ApiError(400, "description is longer than 1024 characters");
    const onSaveError = vi.fn();
    wireMdEditor({
      ta: document.getElementById("ta") as HTMLTextAreaElement,
      saveBtn: document.getElementById("save") as HTMLButtonElement,
      backBtn: document.getElementById("back") as HTMLElement,
      isCreate: true,
      saveLabel: "Save",
      getOriginal: () => "",
      setOriginal: vi.fn(),
      save: () => Promise.reject(err),
      onSaveError,
      onSaved: vi.fn(),
      discardMessage: "",
      onBack: vi.fn(() => Promise.resolve()),
    });
    (document.getElementById("save") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(onSaveError).toHaveBeenCalledWith(err));
    expect((document.getElementById("save") as HTMLButtonElement).disabled).toBe(false);
  });

  it("gives the save button back the markup it had when no label is passed", async () => {
    document.body.innerHTML = `<textarea id="ta"></textarea><button id="save">Save <kbd aria-hidden="true">⌘S</kbd></button><button id="back"></button>`;
    const save = document.getElementById("save") as HTMLButtonElement;
    const before = save.innerHTML;
    let release: () => void = () => undefined;
    const onSaved = vi.fn();
    wireMdEditor({
      ta: document.getElementById("ta") as HTMLTextAreaElement,
      saveBtn: save,
      backBtn: document.getElementById("back") as HTMLElement,
      isCreate: true,
      getOriginal: () => "",
      setOriginal: vi.fn(),
      save: () => new Promise<void>((r) => (release = r)),
      onSaveError: vi.fn(),
      onSaved,
      discardMessage: "",
      onBack: vi.fn(() => Promise.resolve()),
    });
    save.click();
    expect(save.querySelector("kbd")).toBeNull();
    release();
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(save.innerHTML).toBe(before);
  });

  it("says the file has unsaved changes in the editor's save state, until it saves", async () => {
    document.body.innerHTML = mdEditorHTML({
      path: "prompts/main.md",
      pathId: "p",
      textId: "ta",
      value: "old",
      back: { id: "back" },
      stateId: "st",
      save: { id: "save", disabled: true },
    });
    const ta = document.getElementById("ta") as HTMLTextAreaElement;
    const state = document.getElementById("st") as HTMLElement;
    let original = "old";
    const onSaved = vi.fn();
    wireMdEditor({
      ta,
      saveBtn: document.getElementById("save") as HTMLButtonElement,
      stateEl: state,
      backBtn: document.getElementById("back") as HTMLElement,
      isCreate: false,
      getOriginal: () => original,
      setOriginal: (v) => (original = v),
      save: () => Promise.resolve(),
      onSaveError: vi.fn(),
      onSaved,
      discardMessage: "",
      onBack: vi.fn(() => Promise.resolve()),
    });
    expect(state.hidden).toBe(true);
    ta.value = "new";
    ta.dispatchEvent(new Event("input"));
    expect(state.hidden).toBe(false);
    expect(state.dataset.dirty).toBe("true");
    expect(state.querySelector(".dot-warn")).not.toBeNull();
    (document.getElementById("save") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(onSaved).toHaveBeenCalledWith("new"));
    expect(state.hidden).toBe(true);
    expect(state.dataset.dirty).toBe("false");
  });
});
