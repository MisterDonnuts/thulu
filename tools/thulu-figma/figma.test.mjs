import { test } from "node:test";
import assert from "node:assert/strict";

import {
  colorHex,
  normalizeNodeId,
  parseFileRef,
  parseTeamRef,
  summarizeComments,
  summarizeLibrary,
  summarizeNode,
  summarizeTree,
} from "./figma.js";

test("a file reference is a bare key or any figma.com link to the file", () => {
  assert.deepEqual(parseFileRef("filekeyfilekey00"), { fileKey: "filekeyfilekey00", nodeId: "" });
  assert.deepEqual(parseFileRef("https://www.figma.com/design/filekeyfilekey00/App?node-id=12-34&t=x"), {
    fileKey: "filekeyfilekey00",
    nodeId: "12:34",
  });
  assert.deepEqual(parseFileRef("https://figma.com/file/filekeyfilekey00/Old-link"), {
    fileKey: "filekeyfilekey00",
    nodeId: "",
  });
  assert.equal(
    parseFileRef("https://www.figma.com/design/filekeyfilekey00/branch/branchbranch0000/App").fileKey,
    "branchbranch0000",
  );
  assert.equal(parseFileRef("https://www.figma.com/proto/filekeyfilekey00/Flow").fileKey, "filekeyfilekey00");
});

test("anything else is not a file reference", () => {
  for (const bad of [
    "",
    "short",
    "../../v1/me",
    "https://evil.example/design/filekeyfilekey00",
    "https://www.figma.com/files/team/123",
    "filekeyfilekey00?x=1",
  ]) {
    assert.equal(parseFileRef(bad), null, bad);
  }
});

test("node ids from links are rewritten to the API's form", () => {
  assert.equal(normalizeNodeId("1-2"), "1:2");
  assert.equal(normalizeNodeId("1:2"), "1:2");
  assert.equal(normalizeNodeId(" "), "");
});

test("a team reference is the digits or a link carrying them", () => {
  assert.equal(parseTeamRef("1234567"), "1234567");
  assert.equal(parseTeamRef("https://www.figma.com/files/team/1234567/Design"), "1234567");
  assert.equal(parseTeamRef("https://www.figma.com/files/9988/team/1234567"), "1234567");
  assert.equal(parseTeamRef("team-one"), null);
});

test("colors come out as hex, with alpha only when not opaque", () => {
  assert.equal(colorHex({ r: 1, g: 0, b: 0, a: 1 }), "#ff0000");
  assert.equal(colorHex({ r: 0, g: 0, b: 0, a: 1 }, 0.25), "#00000040");
});

const frame = {
  id: "1:2",
  name: "Card",
  type: "FRAME",
  absoluteBoundingBox: { x: 10, y: 20.04, width: 320, height: 200 },
  layoutMode: "VERTICAL",
  itemSpacing: 8,
  paddingTop: 16,
  paddingRight: 16,
  paddingBottom: 16,
  paddingLeft: 16,
  primaryAxisAlignItems: "MIN",
  counterAxisAlignItems: "CENTER",
  fills: [{ type: "SOLID", color: { r: 1, g: 1, b: 1, a: 1 } }, { type: "SOLID", visible: false, color: { r: 0, g: 0, b: 0 } }],
  cornerRadius: 12,
  effects: [{ type: "DROP_SHADOW", radius: 8, color: { r: 0, g: 0, b: 0, a: 0.25 }, offset: { x: 0, y: 4 } }],
  styles: { fill: "S:1" },
  exportSettings: [{ format: "PNG" }],
  children: [
    {
      id: "1:3",
      name: "Title",
      type: "TEXT",
      characters: "Hello",
      style: { fontFamily: "Inter", fontWeight: 600, fontSize: 18, lineHeightPx: 24.2, textAlignHorizontal: "LEFT" },
      fills: [{ type: "SOLID", color: { r: 0.1, g: 0.1, b: 0.1, a: 1 } }],
    },
    { id: "1:4", name: "Body", type: "FRAME", children: [{ id: "1:5", name: "x", type: "RECTANGLE" }] },
  ],
};

test("a node keeps what building it takes and drops the rest", () => {
  const out = summarizeNode(frame, { depth: 1, styles: { "S:1": { name: "Surface/Default" } } });
  assert.deepEqual(out.box, [10, 20, 320, 200]);
  assert.deepEqual(out.layout, { mode: "vertical", gap: 8, padding: [16, 16, 16, 16], main: "min", cross: "center" });
  assert.deepEqual(out.fills, ["#ffffff"], "hidden paints are left out");
  assert.equal(out.radius, 12);
  assert.deepEqual(out.effects, [{ type: "drop_shadow", radius: 8, color: "#00000040", offset: [0, 4] }]);
  assert.deepEqual(out.styles, { fill: "Surface/Default" }, "style ids resolve to names");
  assert.equal(out.exportSettings, undefined);
  const [title, body] = out.children;
  assert.equal(title.text, "Hello");
  assert.deepEqual(title.text_style, { font: "Inter", weight: 600, size: 18, line_height: 24.2 });
  assert.equal(body.children, undefined);
  assert.equal(body.child_count, 1);
});

test("the node budget cuts the answer and counts what it left out", () => {
  const budget = { left: 2, omitted: 0 };
  const out = summarizeNode(frame, { depth: 3, budget });
  assert.equal(out.children.length, 1);
  assert.equal(budget.omitted, 2, "Body and its rectangle");
});

test("the outline lists pages and frames by id, name and type only", () => {
  const doc = { children: [{ id: "0:1", name: "Page 1", type: "CANVAS", children: [frame] }] };
  const { pages, omitted_nodes } = summarizeTree(doc, { maxNodes: 3 });
  assert.equal(pages[0].name, "Page 1");
  const card = pages[0].children[0];
  assert.deepEqual(Object.keys(card).sort(), ["children", "id", "name", "type"]);
  assert.equal(omitted_nodes, 2, "Body and its rectangle, past the three-node cap");
});

test("the library groups styles by type and caps components", () => {
  const lib = summarizeLibrary(
    {
      components: { "1:9": { name: "Button/Primary", description: "" }, "1:10": { name: "Input" } },
      styles: { "S:1": { name: "Surface/Default", styleType: "FILL" }, "S:2": { name: "H1", styleType: "TEXT" } },
    },
    1,
  );
  assert.equal(lib.components.length, 1);
  assert.equal(lib.components_total, 2);
  assert.deepEqual(lib.styles, { fill: [{ id: "S:1", name: "Surface/Default" }], text: [{ id: "S:2", name: "H1" }] });
});

test("comments become threads; resolved ones stay out unless asked for", () => {
  const list = [
    { id: "1", message: "Spacing is off", user: { handle: "ana" }, created_at: "2026-10-01T10:00:00Z", client_meta: { node_id: "1:2" } },
    { id: "2", parent_id: "1", message: "Fixed", user: { handle: "bo" }, created_at: "2026-10-02T10:00:00Z" },
    { id: "3", message: "Old", user: { handle: "ana" }, created_at: "2026-09-01T10:00:00Z", resolved_at: "2026-09-02T00:00:00Z" },
  ];
  const open = summarizeComments(list);
  assert.equal(open.threads.length, 1);
  assert.equal(open.resolved_hidden, 1);
  assert.deepEqual(open.threads[0].anchor, { node: "1:2" });
  assert.deepEqual(open.threads[0].replies.map((r) => r.message), ["Fixed"]);
  const all = summarizeComments(list, { includeResolved: true });
  assert.deepEqual(all.threads.map((th) => th.id), ["1", "3"]);
});
