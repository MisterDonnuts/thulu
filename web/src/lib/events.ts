// One EventSource per tab: HTTP/1.1 allows 6 connections per host across tabs and each SSE stream
// holds one.
// SSE only notifies; nothing is replayed across a reconnect, so consumers resync over REST in
// onReconnect.

import { t } from "../i18n";
import { ApiError, api } from "./api";
import { apiBase } from "./auth";

export interface Frame {
  topic: string;
  type: string;
  data?: unknown;
}

export const FRAME_END = "end";
export const FRAME_ERROR = "error";
export const FRAME_TRUNCATED = "truncated";
export const FRAME_WAITING = "waiting";

// A reconnect restarts the server source, which replays a log run from its start; consumers must
// clear on reset.
// Dispatched synchronously before the new EventSource exists, so no new frame can precede it.
export const FRAME_RESET = "reset";

export type ChannelStatus = "live" | "connecting" | "down";

type Handler = (f: Frame) => void;

const RETRY_MIN = 1000;
const RETRY_MAX = 30000;
const PUSH_RETRIES = 3;
const PUSH_RETRY_BASE_MS = 500;

function parseJSON(raw: unknown): Record<string, unknown> | null {
  if (typeof raw !== "string") return null;
  try {
    const v: unknown = JSON.parse(raw);
    return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

interface TopicHistory {
  frames: Frame[];
  chars: number;
  truncated: boolean;
}

// Mirrors the server's per-source replay caps.
const HISTORY_MAX_FRAMES = 20000;
const HISTORY_MAX_CHARS = 8 * 1024 * 1024;

export class EventChannel {
  private es: EventSource | null = null;
  private streamId = "";
  private handlers = new Map<string, Set<Handler>>();
  // The server replays a source once per connection and the tab shares one, so a second panel on a
  // run
  // would get nothing (forever, for a finished run) without this buffer.
  private history = new Map<string, TopicHistory>();
  private noReplay = new Set<string>();
  private serverTopics = new Set<string>();
  private reconnectCbs = new Set<() => void>();
  private statusCbs = new Set<(s: ChannelStatus) => void>();
  private status: ChannelStatus = "down";
  private retryDelay = RETRY_MIN;
  private retryTimer = 0;
  private reconnecting = false;

  // The replay flag is taken from the first subscriber of a topic.
  // Handlers are a Set: the same function reference twice is one subscriber.
  subscribe(topic: string, handler: Handler, opts?: { replay?: boolean }): () => void {
    let set = this.handlers.get(topic);
    const isNew = !set;
    if (!set) {
      set = new Set();
      this.handlers.set(topic, set);
      if (opts?.replay === false) this.noReplay.add(topic);
    }
    set.add(handler);

    if (!this.es) this.connect();
    else if (isNew) void this.push("subscribe", [topic]);
    else this.replayTo(topic, handler);

    let live = true;
    return () => {
      if (!live) return;
      live = false;
      this.drop(topic, handler);
    };
  }

  onReconnect(cb: () => void): () => void {
    this.reconnectCbs.add(cb);
    return () => this.reconnectCbs.delete(cb);
  }

  onStatus(cb: (s: ChannelStatus) => void): () => void {
    this.statusCbs.add(cb);
    cb(this.status);
    return () => this.statusCbs.delete(cb);
  }

  // Microtask so the caller finishes wiring first; network frames arrive as tasks, so none can
  // overtake the replay.
  private replayTo(topic: string, handler: Handler) {
    const hist = this.history.get(topic);
    if (!hist) return;
    queueMicrotask(() => {
      if (!this.handlers.get(topic)?.has(handler)) return;
      try {
        if (hist.truncated) handler({ topic, type: FRAME_TRUNCATED });
        for (const f of hist.frames) handler(f);
      } catch (err) {
        console.error(t("lib.events.log.replay_failed", { topic }), err);
      }
    });
  }

  private retain(frame: Frame) {
    if (this.noReplay.has(frame.topic)) return;
    let hist = this.history.get(frame.topic);
    if (!hist) {
      hist = { frames: [], chars: 0, truncated: false };
      this.history.set(frame.topic, hist);
    }
    const size = typeof frame.data === "string" ? frame.data.length : 0;
    hist.frames.push(frame);
    hist.chars += size;
    while (
      hist.frames.length > HISTORY_MAX_FRAMES ||
      (hist.chars > HISTORY_MAX_CHARS && hist.frames.length > 1)
    ) {
      const dropped = hist.frames.shift();
      hist.chars -= typeof dropped?.data === "string" ? dropped.data.length : 0;
      hist.truncated = true;
    }
  }

  private drop(topic: string, handler: Handler) {
    const set = this.handlers.get(topic);
    if (!set) return;
    set.delete(handler);
    if (set.size > 0) return;
    this.handlers.delete(topic);
    this.history.delete(topic);
    this.noReplay.delete(topic);
    if (this.handlers.size === 0) {
      this.disconnect();
      return;
    }
    void this.push("unsubscribe", [topic]);
  }

  private connect() {
    if (this.es || this.handlers.size === 0) return;
    window.clearTimeout(this.retryTimer);
    this.setStatus("connecting");

    const topics = [...this.handlers.keys()];
    if (this.reconnecting) {
      this.history.clear();
      for (const topic of topics) this.dispatch({ topic, type: FRAME_RESET });
    }
    this.reconnecting = true;
    const qs = topics.length ? `?topics=${encodeURIComponent(topics.join(","))}` : "";
    const es = new EventSource(`${apiBase()}/events${qs}`, { withCredentials: true });
    this.es = es;
    this.serverTopics = new Set(topics);

    es.addEventListener("hello", (e) => this.onHello(e));
    es.addEventListener("msg", (e) => this.onFrame(e));
    es.addEventListener("lag", (e) => this.onLag(e));
    es.onerror = () => this.onError(es);
  }

  private disconnect() {
    window.clearTimeout(this.retryTimer);
    this.history.clear();
    this.es?.close();
    this.es = null;
    this.streamId = "";
    this.serverTopics.clear();
    this.setStatus("down");
  }

  private onHello(e: MessageEvent) {
    const payload = parseJSON(e.data);
    const streamId = str(payload?.stream_id);
    if (!streamId) return;
    this.streamId = streamId;
    this.retryDelay = RETRY_MIN;
    // An error in hello means the query-string subscribe failed as a batch: the server holds no
    // topics.
    if (str(payload?.error)) this.serverTopics.clear();
    this.setStatus("live");

    const missing = [...this.handlers.keys()].filter((topic) => !this.serverTopics.has(topic));
    const done = missing.length ? this.push("subscribe", missing) : Promise.resolve();
    void done.then(() => {
      // Resync only after subscriptions are in place, so a REST refetch can't race the first pushed
      // event.
      for (const cb of this.reconnectCbs) cb();
    });
  }

  private onFrame(e: MessageEvent) {
    const raw = parseJSON(e.data);
    const topic = str(raw?.topic);
    const type = str(raw?.type);
    if (!topic || !type) return;
    this.dispatch({ topic, type, data: raw?.data });
  }

  private dispatch(frame: Frame) {
    const set = this.handlers.get(frame.topic);
    if (!set) return;
    if (frame.type !== FRAME_RESET) this.retain(frame);
    for (const h of [...set]) {
      try {
        h(frame);
      } catch (err) {
        console.error(t("lib.events.log.handler_failed", { topic: frame.topic }), err);
      }
    }
  }

  private onLag(e: MessageEvent) {
    console.warn(t("lib.events.log.dropped_frames", { data: String(e.data) }));
    for (const cb of this.reconnectCbs) cb();
  }

  private onError(es: EventSource) {
    if (es !== this.es) return;
    if (es.readyState === EventSource.CONNECTING) {
      this.setStatus("connecting");
      return;
    }
    this.es = null;
    this.streamId = "";
    this.serverTopics.clear();
    this.setStatus("down");
    if (this.handlers.size === 0) return;
    window.clearTimeout(this.retryTimer);
    this.retryTimer = window.setTimeout(() => this.connect(), this.retryDelay);
    this.retryDelay = Math.min(this.retryDelay * 2, RETRY_MAX);
  }

  // Uses the api client, not raw fetch, so an expired session is refreshed; otherwise a tab left
  // open
  // overnight looks live while subscribed to nothing.
  private async push(
    action: "subscribe" | "unsubscribe",
    topics: string[],
    attempt = 0,
  ): Promise<void> {
    if (!this.streamId || topics.length === 0) return;
    const id = this.streamId;
    try {
      await api.control(`/events/${id}/${action}`, { topics });
      if (id !== this.streamId) return; // reconnected mid-flight; that pass owns serverTopics
      for (const topic of topics) {
        if (action === "subscribe") this.serverTopics.add(topic);
        else this.serverTopics.delete(topic);
      }
      return;
    } catch (err) {
      // 404: the server lost the stream (restart, eviction); reconnecting re-subscribes everything.
      if (err instanceof ApiError && err.status === 404) {
        if (id === this.streamId) this.reconnectNow();
        return;
      }
    }
    if (attempt >= PUSH_RETRIES || id !== this.streamId) return;
    window.setTimeout(
      () => {
        // Retry only what is still wanted: a stale intent could unsubscribe a live topic or leave a
        // pod tail running.
        const still = topics.filter((topic) =>
          action === "subscribe" ? this.handlers.has(topic) : !this.handlers.has(topic),
        );
        if (id === this.streamId) void this.push(action, still, attempt + 1);
      },
      PUSH_RETRY_BASE_MS * 2 ** attempt,
    );
  }

  private reconnectNow() {
    this.es?.close();
    this.es = null;
    this.streamId = "";
    this.serverTopics.clear();
    if (this.handlers.size > 0) this.connect();
  }

  private setStatus(s: ChannelStatus) {
    if (this.status === s) return;
    this.status = s;
    for (const cb of this.statusCbs) cb(s);
  }
}

export const events = new EventChannel();

export function logsTopic(runID: string): string {
  return `logs:${runID}`;
}

export const CHAT_TOPIC = "chat";

export const TASKS_TOPIC = "tasks";
