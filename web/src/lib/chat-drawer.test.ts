// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { DEV_MODE_AUTHOR, viewerFor } from "./activity";
import {
  actionStateText,
  agentGaps,
  agentOptionLabel,
  canActOnBoards,
  ChatDrafts,
  chatTimeline,
  isMentionBrief,
  previewText,
  replyTools,
  type ChatAction,
  type ChatAgent,
} from "./chat-drawer";

const agent = (over: Partial<ChatAgent> = {}): ChatAgent => ({
  type: "reviewer",
  name: "reviewer",
  prompt_ready: true,
  llm_ready: true,
  mcp: [],
  ...over,
});

describe("agentGaps / agentOptionLabel", () => {
  it("names a ready agent plainly", () => {
    expect(agentGaps(agent())).toEqual([]);
    expect(agentOptionLabel(agent())).toBe("reviewer");
  });

  it("marks what an agent lacks", () => {
    expect(agentGaps(agent({ prompt_ready: false }))).toEqual(["prompt"]);
    expect(agentOptionLabel(agent({ prompt_ready: false }))).toBe(
      t("chat.new.option_lacks_prompt", { agent: "reviewer" }),
    );
    expect(agentOptionLabel(agent({ llm_ready: false }))).toBe(
      t("chat.new.option_lacks_llm", { agent: "reviewer" }),
    );
    expect(agentOptionLabel(agent({ prompt_ready: false, llm_ready: false }))).toBe(
      t("chat.new.option_lacks_both", { agent: "reviewer" }),
    );
  });
});

describe("canActOnBoards", () => {
  it("needs octopus-api with write, or a tool that changes tasks", () => {
    expect(canActOnBoards(["octopus-api@write"])).toBe(true);
    expect(canActOnBoards(["thulu-github@write", "octopus-api@admin"])).toBe(true);
    expect(canActOnBoards(["octopus-api@read+task_update"])).toBe(true);
    expect(canActOnBoards(["octopus-api@task_list+task_create"])).toBe(true);
  });

  it("refuses read-only and missing grants", () => {
    expect(canActOnBoards(["octopus-api"])).toBe(false);
    expect(canActOnBoards(["octopus-api@read"])).toBe(false);
    expect(canActOnBoards(["octopus-api@task_list"])).toBe(false);
    expect(canActOnBoards(["thulu-github@write"])).toBe(false);
    expect(canActOnBoards([])).toBe(false);
    expect(canActOnBoards(null)).toBe(false);
  });
});

describe("previewText", () => {
  it("drops the markdown and keeps the words", () => {
    expect(previewText("Moví a **In progress**:\n- Exportar\n- Redondeo")).toBe(
      "Moví a In progress: Exportar Redondeo",
    );
  });

  it("keeps code and links as text", () => {
    expect(previewText("Corré `npm test` y mirá [el PR](https://x.test/1)")).toBe(
      "Corré npm test y mirá el PR",
    );
  });

  it("returns nothing for an empty message", () => {
    expect(previewText("  ")).toBe("");
  });
});

const action = (over: Partial<ChatAction> = {}): ChatAction => ({
  id: "a1",
  chat_id: "c1",
  agent: "assistant",
  method: "DELETE",
  path: "/tasks/t-1",
  summary: "Borrar «Borrador»",
  status: "confirmed",
  decided_by: "ana@x.test",
  created_at: "2026-10-16T10:00:00Z",
  ...over,
});

describe("actionStateText", () => {
  it("says you when this tab decided, the person otherwise", () => {
    expect(actionStateText(action(), true)).toBe(t("chat.actions.confirmed_by_you"));
    expect(actionStateText(action(), false)).toBe(
      t("chat.actions.confirmed_by", { person: "ana@x.test" }),
    );
    expect(actionStateText(action({ status: "canceled" }), true)).toBe(
      t("chat.actions.canceled_by_you"),
    );
    expect(actionStateText(action({ status: "canceled", decided_by: "" }), false)).toBe(
      t("chat.actions.canceled_expired"),
    );
  });

  it("says you when decided_by signs the viewer, the dev-mode operator included", () => {
    const ana = { name: "Ana", email: "ana@x.test" };
    expect(actionStateText(action(), false, ana)).toBe(t("chat.actions.confirmed_by_you"));
    const dev = viewerFor(null, true);
    expect(actionStateText(action({ decided_by: DEV_MODE_AUTHOR }), false, dev)).toBe(
      t("chat.actions.confirmed_by_you"),
    );
    expect(
      actionStateText(action({ status: "canceled", decided_by: DEV_MODE_AUTHOR }), false, dev),
    ).toBe(t("chat.actions.canceled_by_you"));
    expect(actionStateText(action({ decided_by: "Beto" }), false, ana)).toBe(
      t("chat.actions.confirmed_by", { person: "Beto" }),
    );
  });

  it("explains a 403 as a role that falls short", () => {
    const failed = action({ status: "failed", result_code: 403, result_error: "forbidden" });
    expect(actionStateText(failed, true)).toBe(t("chat.actions.forbidden_task"));
    expect(actionStateText({ ...failed, path: "/tasks/t-1/comments/c-9" }, true)).toBe(
      t("chat.actions.forbidden"),
    );
  });

  it("keeps the status and the error for any other failure", () => {
    const failed = action({ status: "failed", result_code: 409, result_error: "busy" });
    expect(actionStateText(failed, true)).toBe(
      t("chat.actions.failed_with_error", { status: 409, error: "busy" }),
    );
  });
});

describe("message metadata", () => {
  it("recognizes the brief of a thread mention", () => {
    expect(isMentionBrief({ role: "user", metadata: { source: "task_mention" } })).toBe(true);
    expect(isMentionBrief({ role: "assistant", metadata: { source: "task_mention" } })).toBe(false);
    expect(isMentionBrief({ role: "user" })).toBe(false);
  });

  it("reads the tools a reply used", () => {
    expect(
      replyTools({
        role: "assistant",
        metadata: {
          tools: '[{"name":"Read","hint":"src/a.ts"},{"name":"Bash"}]',
          tools_omitted: "3",
        },
      }),
    ).toEqual({
      calls: [
        { name: "Read", hint: "src/a.ts" },
        { name: "Bash", hint: undefined },
      ],
      omitted: 3,
    });
    expect(replyTools({ role: "assistant" })).toEqual({ calls: [], omitted: 0 });
    expect(replyTools({ role: "assistant", metadata: { tools: "not json" } })).toEqual({
      calls: [],
      omitted: 0,
    });
  });
});

describe("ChatDrafts", () => {
  it("gives each chat back only its own text", () => {
    const d = new ChatDrafts();
    d.stash("a", "half a question");
    expect(d.take("b")).toBe("");
    expect(d.take("a")).toBe("half a question");
    expect(d.take("a")).toBe("");
  });

  it("forgets a blank composer", () => {
    const d = new ChatDrafts();
    d.stash("a", "something");
    d.stash("a", "   ");
    expect(d.take("a")).toBe("");
  });

  it("restores a failed message unless the chat holds newer text", () => {
    const d = new ChatDrafts();
    d.restore("a", "failed send");
    expect(d.take("a")).toBe("failed send");
    d.stash("b", "typed after");
    d.restore("b", "failed send");
    expect(d.take("b")).toBe("typed after");
  });
});

describe("chatTimeline", () => {
  it("puts each held action where the agent asked for it", () => {
    const msgs = [
      { id: "m1", created_at: "2026-10-17T10:00:00Z" },
      { id: "m2", created_at: "2026-10-17T10:02:00Z" },
      { id: "m3", created_at: "2026-10-17T10:05:00Z" },
    ];
    const actions = [
      { id: "a1", created_at: "2026-10-17T10:02:00Z" },
      { id: "a2", created_at: "2026-10-17T10:03:00Z" },
    ];
    const order = chatTimeline(msgs, actions).map((i) => ("msg" in i ? i.msg.id : i.action.id));
    expect(order).toEqual(["m1", "m2", "a1", "a2", "m3"]);
  });

  it("keeps a chat with no actions as its messages", () => {
    const msgs = [{ created_at: "2026-10-17T10:00:00Z" }];
    expect(chatTimeline(msgs, [])).toEqual([{ msg: msgs[0] }]);
  });
});
