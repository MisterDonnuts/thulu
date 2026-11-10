// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { dirty } from "./reactive";
import { taskCardEl } from "./ui/task-card";

afterEach(() => {
  document.body.innerHTML = "";
});

describe("dirty.isDragging", () => {
  it("is false with nothing picked up", () => {
    document.body.innerHTML = `<div class="task-card"></div><div class="col"></div>`;
    expect(dirty.isDragging()).toBe(false);
  });

  it("sees a board card the pointer picked up", () => {
    document.body.innerHTML = `<div class="task-card task-card-dragging"></div>`;
    expect(dirty.isDragging()).toBe(true);
  });

  it("sees a card built with the dragging flag", () => {
    document.body.appendChild(taskCardEl({ id: "t1", flags: ["dragging"] }));
    expect(dirty.isDragging()).toBe(true);
  });

  it("does not take a ghost or a lifted card for a drag", () => {
    document.body.innerHTML = `<div class="task-card task-card-ghost"></div><div class="task-card task-card-lifted"></div>`;
    expect(dirty.isDragging()).toBe(false);
  });

  it("still sees a column or a subtask row being dragged", () => {
    document.body.innerHTML = `<div class="col dragging ce-dragging"></div>`;
    expect(dirty.isDragging()).toBe(true);
    document.body.innerHTML = `<div class="st-row dragging"></div>`;
    expect(dirty.isDragging()).toBe(true);
  });
});
