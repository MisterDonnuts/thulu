import { describe, expect, it } from "vitest";
import type { LiveBlock, LiveEvent } from "./liveview";
import {
  liveToolIcon,
  liveToolPreview,
  normalizeLiveEvent,
  prettyEvent,
  sessionModeLabel,
  toolCardHTML,
  toolInputJSON,
  toolResultHTML,
  toolResultText,
} from "./liveview";

const blocks = (ev: LiveEvent | null): LiveBlock[] => {
  const content = ev?.message?.content;
  return Array.isArray(content) ? content : [];
};

describe("normalizeLiveEvent", () => {
  it("passes claude-code events through untouched", () => {
    const ev = { type: "assistant", message: { content: [] } };
    expect(normalizeLiveEvent(ev)).toBe(ev);
  });

  it("drops events with nothing to show", () => {
    expect(normalizeLiveEvent({ type: "turn.started" })).toBeNull();
    expect(normalizeLiveEvent(null)).toBeNull();
    expect(normalizeLiveEvent({ type: "item.completed", item: { type: "unheard_of" } })).toBeNull();
  });

  it("turns the adapter's session line into a session event, for either engine", () => {
    expect(normalizeLiveEvent({ type: "octopus.session", mode: "rehydrated" })).toEqual({
      type: "session",
      mode: "rehydrated",
    });
    expect(normalizeLiveEvent({ type: "octopus.session" })).toEqual({ type: "session", mode: "" });
  });

  it("turns a codex thread start into a session-init system event", () => {
    expect(normalizeLiveEvent({ type: "thread.started", model: "o3", thread_id: "t1" })).toEqual({
      type: "system",
      subtype: "init",
      model: "o3",
      session_id: "t1",
    });
  });

  it("treats a completed codex turn as the run's result", () => {
    expect(normalizeLiveEvent({ type: "turn.completed", usage: { in: 1 } })).toEqual({
      type: "result",
      is_error: false,
      num_turns: 1,
      stop_reason: "",
      usage: { in: 1 },
    });
  });

  it("marks a failed turn as an error result", () => {
    const out = normalizeLiveEvent({ type: "turn.failed" });
    expect(out?.is_error).toBe(true);
    expect(out?.stop_reason).toBe("error");
  });

  it("maps a codex message onto an assistant text block", () => {
    expect(
      normalizeLiveEvent({ type: "item.completed", item: { type: "agent_message", text: "hi" } }),
    ).toEqual({ type: "assistant", message: { content: [{ type: "text", text: "hi" }] } });
  });

  it("maps reasoning onto a thinking block, summary included", () => {
    const out = normalizeLiveEvent({
      type: "item.completed",
      item: { type: "reasoning", summary: "because" },
    });
    expect(blocks(out)[0]).toEqual({ type: "thinking", thinking: "because" });
  });

  it("maps a shell command onto a tool_use named shell", () => {
    const out = normalizeLiveEvent({
      type: "item.completed",
      item: { type: "command_execution", command: "ls -la" },
    });
    expect(blocks(out)[0]).toEqual({
      type: "tool_use",
      name: "shell",
      input: { command: "ls -la" },
    });
  });

  it("maps a function call, falling back to its item type for a name", () => {
    const out = normalizeLiveEvent({
      type: "item.completed",
      item: { type: "function_call", arguments: { a: 1 } },
    });
    expect(blocks(out)[0].name).toBe("function_call");
    expect(blocks(out)[0].input).toEqual({ a: 1 });
  });

  it("renders a non-fatal codex error as a warning, not a failed run", () => {
    expect(
      normalizeLiveEvent({ type: "item.completed", item: { type: "error", message: "odd model" } }),
    ).toEqual({ type: "warning", text: "odd model" });
  });
});

describe("normalizeLiveEvent for OpenCode", () => {
  const sid = { sessionID: "ses_1" };

  it("shows text, reasoning and tool calls as the assistant's", () => {
    expect(blocks(normalizeLiveEvent({ type: "text", ...sid, part: { text: "hola" } }))).toEqual([
      { type: "text", text: "hola" },
    ]);
    expect(
      blocks(normalizeLiveEvent({ type: "reasoning", ...sid, part: { text: "pienso" } })),
    ).toEqual([{ type: "thinking", thinking: "pienso" }]);
    const tool = normalizeLiveEvent({
      type: "tool_use",
      ...sid,
      part: {
        tool: "bash",
        callID: "call_1",
        state: { status: "completed", input: { command: "ls" } },
      },
    });
    expect(blocks(tool)).toEqual([
      { type: "tool_use", id: "call_1", name: "bash", input: { command: "ls" } },
    ]);
  });

  it("ends the turn on the step that does not call tools", () => {
    expect(
      normalizeLiveEvent({ type: "step_finish", ...sid, part: { reason: "tool-calls" } }),
    ).toBeNull();
    expect(normalizeLiveEvent({ type: "step_finish", ...sid, part: { reason: "stop" } })).toEqual({
      type: "result",
      is_error: false,
      num_turns: 1,
      stop_reason: "stop",
    });
    expect(
      normalizeLiveEvent({ type: "error", ...sid, error: { name: "APIError" } }),
    ).toMatchObject({
      type: "result",
      is_error: true,
    });
  });

  it("leaves lines without a session id alone", () => {
    expect(
      normalizeLiveEvent(JSON.parse('{"type":"error","message":"codex error"}') as LiveEvent),
    ).toBeNull();
    expect(normalizeLiveEvent({ type: "step_start", ...sid })).toBeNull();
  });
});

describe("liveToolIcon", () => {
  it("groups the editing tools under one glyph", () => {
    expect(liveToolIcon("Edit")).toBe("edit");
    expect(liveToolIcon("MultiEdit")).toBe("edit");
  });

  it("falls back to the generic bolt", () => {
    expect(liveToolIcon("SomeMcpTool")).toBe("zap");
  });
});

describe("liveToolPreview", () => {
  it("shows the path a file tool touched", () => {
    expect(liveToolPreview("Read", { file_path: "/a/b.ts" })).toBe("/a/b.ts");
    expect(liveToolPreview("edit", { filePath: "/a/c.go" })).toBe("/a/c.go");
    expect(liveToolIcon("bash")).toBe("terminal");
  });

  it("collapses whitespace in a command and clips it", () => {
    expect(liveToolPreview("Bash", { command: "ls   -la\n  ." })).toBe("ls -la .");
    expect(liveToolPreview("Bash", { command: "x".repeat(200) })).toHaveLength(120);
  });

  it("counts todos", () => {
    expect(liveToolPreview("TodoWrite", { todos: [1] })).toBe("1 item");
    expect(liveToolPreview("TodoWrite", { todos: [1, 2] })).toBe("2 items");
  });

  it("falls back to clipped JSON for a tool it does not know", () => {
    expect(liveToolPreview("Mystery", { a: 1 })).toBe('{"a":1}');
    expect(liveToolPreview("Mystery", { a: "x".repeat(200) })).toMatch(/…$/);
  });

  it("says nothing when there is no input", () => {
    expect(liveToolPreview("Read", null)).toBe("");
  });
});

describe("tool cards", () => {
  it("escapes a hostile tool name and preview into the summary", () => {
    const html = toolCardHTML({ name: "<img>", input: { file_path: "</pre>" } });
    expect(html).not.toContain("<img>");
    expect(html).toContain("lv-tool-status pending");
  });

  it("names an unnamed tool", () => {
    expect(toolCardHTML({})).toContain(">tool<");
  });

  it("survives input it cannot stringify", () => {
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(() => toolInputJSON(cyclic)).not.toThrow();
  });

  it("reads a result out of every shape it arrives in", () => {
    expect(toolResultText("plain")).toBe("plain");
    expect(toolResultText([{ text: "a" }, { text: "b" }])).toBe("a\nb");
    expect(toolResultText({ n: 1 })).toContain('"n": 1');
    expect(toolResultText(null)).toBe("");
  });

  it("clips long output and says that it did", () => {
    const html = toolResultHTML("y".repeat(5000), false);
    expect(html).toContain("… (truncated)");
    expect(toolResultHTML("short", false)).not.toContain("truncated");
  });

  it("labels an error result", () => {
    expect(toolResultHTML("boom", true)).toContain("Result (error)");
  });

  it("shows the input and the result in code blocks, an error one in red", () => {
    expect(toolCardHTML({ name: "Read", input: { file_path: "a.go" } })).toContain(
      '<div class="code-block code-block-multi code-block-wrap code-block-h-sm"><pre class="code-block-text" tabindex="0">',
    );
    expect(toolResultHTML("ok", false)).toContain(
      'class="code-block code-block-multi code-block-wrap code-block-h-sm"',
    );
    expect(toolResultHTML("boom", true)).toContain("code-block-danger");
    expect(toolResultHTML("<b>", false)).toContain("&lt;b&gt;");
  });
});

describe("sessionModeLabel", () => {
  it("names each mode and stays quiet about one it does not know", () => {
    expect(sessionModeLabel("native")).toBe("Session resumed");
    expect(sessionModeLabel("rehydrated")).toBe("Session rebuilt from the history");
    expect(sessionModeLabel("fresh")).toBe("New session");
    expect(sessionModeLabel("teleported")).toBe("");
  });
});

describe("prettyEvent", () => {
  it("heads the session line with its mode instead of dumping it", () => {
    const html = prettyEvent({ type: "octopus.session", mode: "native" });
    expect(html).toContain("Session resumed");
    expect(html).not.toContain("ev-b");
  });

  it("heads a session init with the model", () => {
    const html = prettyEvent({ type: "system", subtype: "init", model: "sonnet" });
    expect(html).toContain("system · init · model sonnet");
  });

  it("classes a user message carrying a tool result as tool_result", () => {
    const html = prettyEvent({
      type: "user",
      message: { content: [{ type: "tool_result", content: "out" }] },
    });
    expect(html).toContain('class="ev tool_result"');
    expect(html).toContain("[tool_result]");
  });

  it("classes an assistant turn with a tool call as tool_use", () => {
    const html = prettyEvent({
      type: "assistant",
      message: { content: [{ type: "tool_use", name: "Bash", input: { command: "ls" } }] },
    });
    expect(html).toContain('class="ev tool_use"');
    expect(html).toContain("[tool_use: Bash]");
  });

  it("prints turns and cost on a result", () => {
    expect(prettyEvent({ type: "result", num_turns: 3, total_cost_usd: 0.1234 })).toContain(
      "turns 3 · $0.1234",
    );
  });

  it("dumps anything else as JSON", () => {
    expect(prettyEvent({ type: "weird", a: 1 })).toContain("&quot;a&quot;: 1");
  });

  it("escapes the body", () => {
    const html = prettyEvent({ type: "user", message: { content: "<script>x</script>" } });
    expect(html).not.toContain("<script>");
  });
});
