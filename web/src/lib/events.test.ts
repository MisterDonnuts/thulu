import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { EventChannel, FRAME_END, FRAME_RESET, type Frame } from "./events";

type Listener = (e: { data: string }) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 2;

  readyState = FakeEventSource.CONNECTING;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, Listener[]>();

  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: Listener) {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  hello(streamID = "stream-1", error?: string) {
    this.readyState = FakeEventSource.OPEN;
    this.emit(
      "hello",
      JSON.stringify(error ? { stream_id: streamID, error } : { stream_id: streamID }),
    );
  }

  frame(f: Frame) {
    this.emit("msg", JSON.stringify(f));
  }

  private emit(type: string, data: string) {
    for (const fn of this.listeners.get(type) ?? []) fn({ data });
  }

  fail() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }
}

let posts: Array<{ url: string; topics: string[] }>;

function newChannel() {
  return new EventChannel();
}

const flush = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  FakeEventSource.instances = [];
  posts = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal("window", {
    setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms),
    clearTimeout: (id: number) => clearTimeout(id),
    location: { protocol: "http:", hostname: "octopus.test" },
  });
  vi.stubGlobal("fetch", (url: string, init?: { body?: string }) => {
    const body = init?.body ? (JSON.parse(init.body) as { topics: string[] }) : { topics: [] };
    posts.push({ url, topics: body.topics });
    return Promise.resolve({
      ok: true,
      status: 200,
      text: () => Promise.resolve("{}"),
      json: () => Promise.resolve({}),
    });
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const noop = () => undefined;

const last = () => FakeEventSource.instances[FakeEventSource.instances.length - 1];

describe("EventChannel", () => {
  it("opens exactly one connection for many topics", () => {
    const ch = newChannel();
    ch.subscribe("chat", noop);
    ch.subscribe("logs:a", noop);
    ch.subscribe("logs:b", noop);

    expect(FakeEventSource.instances).toHaveLength(1);
    expect(last().url).toContain("topics=chat");
  });

  it("subscribes the topics that were not in the opening query", async () => {
    const ch = newChannel();
    ch.subscribe("chat", noop);
    ch.subscribe("logs:a", noop);
    last().hello();
    await flush();

    expect(posts).toHaveLength(1);
    expect(posts[0].url).toContain("/events/stream-1/subscribe");
    expect(posts[0].topics).toEqual(["logs:a"]);
  });

  it("re-subscribes everything when the server rejected the opening batch", async () => {
    const ch = newChannel();
    ch.subscribe("chat", noop);
    last().hello("stream-1", "unknown topic");
    await flush();

    expect(posts[0].topics).toEqual(["chat"]);
  });

  it("routes each frame to its topic's handlers only", () => {
    const ch = newChannel();
    const chat: Frame[] = [];
    const logs: Frame[] = [];
    ch.subscribe("chat", (f) => chat.push(f));
    ch.subscribe("logs:a", (f) => logs.push(f));
    last().hello();

    last().frame({ topic: "logs:a", type: "line", data: "hello" });
    last().frame({ topic: "chat", type: "message_added", data: { id: "m1" } });

    expect(logs.map((f) => f.data)).toEqual(["hello"]);
    expect(chat.map((f) => f.type)).toEqual(["message_added"]);
  });

  it("replays a topic's history to a handler that subscribes late", async () => {
    const ch = newChannel();
    ch.subscribe("logs:a", noop);
    last().hello();
    last().frame({ topic: "logs:a", type: "line", data: "one" });
    last().frame({ topic: "logs:a", type: "line", data: "two" });
    last().frame({ topic: "logs:a", type: FRAME_END });

    const late: Frame[] = [];
    ch.subscribe("logs:a", (f) => late.push(f));
    await flush();

    expect(late.map((f) => f.data ?? f.type)).toEqual(["one", "two", FRAME_END]);
    expect(posts).toHaveLength(0);
  });

  it("skips the history for topics that opted out", async () => {
    const ch = newChannel();
    ch.subscribe("chat", noop, { replay: false });
    last().hello();
    last().frame({ topic: "chat", type: "message_added", data: { id: "m1" } });

    const late: Frame[] = [];
    ch.subscribe("chat", (f) => late.push(f));
    await flush();

    expect(late).toEqual([]);
  });

  it("tells the server when a topic's last handler goes away", async () => {
    const ch = newChannel();
    ch.subscribe("chat", noop);
    const offA = ch.subscribe("logs:a", () => undefined);
    const offB = ch.subscribe("logs:a", () => undefined);
    last().hello();
    await flush();
    posts.length = 0;

    offA();
    expect(posts).toHaveLength(0);

    offB();
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toContain("/unsubscribe");
    expect(posts[0].topics).toEqual(["logs:a"]);
  });

  it("closes the connection once nothing is subscribed", () => {
    const ch = newChannel();
    const off = ch.subscribe("logs:a", noop);
    last().hello();
    expect(last().closed).toBe(false);

    off();
    expect(last().closed).toBe(true);
  });

  it("double-unsubscribe does not drop a fresh subscription", () => {
    const ch = newChannel();
    const off = ch.subscribe("logs:a", noop);
    last().hello();
    off();
    const seen: Frame[] = [];
    ch.subscribe("logs:a", (f) => seen.push(f));
    off();

    last().hello();
    last().frame({ topic: "logs:a", type: "line", data: "still here" });
    expect(seen.some((f) => f.data === "still here")).toBe(true);
  });

  it("emits a reset before reconnecting so consumers drop the old replay", async () => {
    const ch = newChannel();
    const seen: Frame[] = [];
    ch.subscribe("logs:a", (f) => seen.push(f));
    last().hello();
    last().frame({ topic: "logs:a", type: "line", data: "one" });

    last().fail();
    await new Promise((r) => setTimeout(r, 1100)); // RETRY_MIN backoff

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(seen.map((f) => f.type)).toEqual(["line", FRAME_RESET]);

    const late: Frame[] = [];
    ch.subscribe("logs:a", (f) => late.push(f));
    await flush();
    expect(late).toEqual([]);
  });

  it("runs reconnect callbacks after subscriptions are restored", async () => {
    const ch = newChannel();
    const order: string[] = [];
    ch.subscribe("chat", noop);
    ch.subscribe("logs:a", noop);
    ch.onReconnect(() => order.push("resync"));

    last().hello();
    await flush();
    await flush();

    expect(posts.map((p) => p.topics)).toEqual([["logs:a"]]);
    expect(order).toEqual(["resync"]);
  });

  it("reports connection status", () => {
    const ch = newChannel();
    const seen: string[] = [];
    ch.onStatus((s) => seen.push(s));
    expect(seen).toEqual(["down"]);

    ch.subscribe("chat", noop);
    last().hello();
    expect(seen).toEqual(["down", "connecting", "live"]);
  });
});

describe("EventChannel subscribe retries", () => {
  it("retries a failed subscribe and only for topics still wanted", async () => {
    let fail = true;
    vi.stubGlobal("fetch", (url: string, init?: { body?: string }) => {
      const body = init?.body ? (JSON.parse(init.body) as { topics: string[] }) : { topics: [] };
      posts.push({ url, topics: body.topics });
      if (fail) {
        fail = false;
        return Promise.reject(new Error("network down"));
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        text: () => Promise.resolve("{}"),
        json: () => Promise.resolve({}),
      });
    });

    const ch = newChannel();
    ch.subscribe("chat", noop);
    last().hello();
    const offB = ch.subscribe("logs:b", noop);
    const offC = ch.subscribe("logs:c", noop);
    await flush();
    offC();
    posts.length = 0;

    await new Promise((r) => setTimeout(r, 700)); // PUSH_RETRY_BASE_MS
    const retried = posts.filter((p) => p.url.includes("/subscribe")).flatMap((p) => p.topics);
    expect(retried).toContain("logs:b");
    expect(retried).not.toContain("logs:c");
    offB();
  });

  it("rebuilds the stream when the server no longer knows it", async () => {
    vi.stubGlobal("fetch", (url: string) => {
      posts.push({ url, topics: [] });
      return Promise.resolve({
        ok: false,
        status: 404,
        text: () => Promise.resolve('{"error":"stream not found"}'),
        json: () => Promise.resolve({ error: "stream not found" }),
      });
    });

    const ch = newChannel();
    ch.subscribe("chat", noop);
    last().hello();
    ch.subscribe("logs:a", noop);
    await flush();

    expect(FakeEventSource.instances.length).toBeGreaterThan(1);
    expect(last().url).toContain("topics=");
  });
});
