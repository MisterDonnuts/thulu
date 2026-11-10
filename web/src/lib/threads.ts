export interface ThreadComment {
  id: string;
  parent_id?: string;
  source?: string;
  author?: string;
}

export interface Thread<C extends ThreadComment> {
  root: C;
  replies: C[];
}

export const FOLDED_REPLIES = 3;

export const RESOLVED_REPLIES = 1;

export function groupThreads<C extends ThreadComment>(comments: C[]): Array<Thread<C>> {
  const threads: Array<Thread<C>> = [];
  const at = new Map<string, number>();
  for (const c of comments) {
    const idx = c.parent_id ? at.get(c.parent_id) : undefined;
    if (idx !== undefined) {
      threads[idx].replies.push(c);
      continue;
    }
    at.set(c.id, threads.length);
    threads.push({ root: c, replies: [] });
  }
  return threads;
}

export function visibleReplies<C>(
  replies: C[],
  expanded: boolean,
  resolved = false,
): { shown: C[]; hidden: number } {
  const keep = resolved ? RESOLVED_REPLIES : FOLDED_REPLIES;
  if (expanded || replies.length <= keep) return { shown: replies, hidden: 0 };
  const hidden = replies.length - keep;
  return { shown: replies.slice(hidden), hidden };
}

// The daemon routes a reply with no @mention to this same agent; keep the rules in step.
export function lastAgent<C extends ThreadComment>(th: Thread<C>): string {
  const msgs = [th.root, ...th.replies];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].source === "agent") return msgs[i].author ?? "";
  }
  return "";
}

export interface MentionEvent {
  kind: string;
  ref_id?: string;
  actor?: string;
  payload?: { agent?: string; thread_id?: string } | null;
}

export interface InflightMention {
  agent: string;
  thread: string;
}

// mention_started and mention_finished both carry the run's instance id in ref_id.
export function inflightMentions(events: MentionEvent[]): InflightMention[] {
  const started = new Map<string, InflightMention>();
  const finished = new Set<string>();
  for (const e of events) {
    if (!e.ref_id) continue;
    if (e.kind === "mention_started") {
      const p = e.payload ?? {};
      const agent = p.agent ?? ((e.actor ?? "").replace(/^agent:/, "") || "agent");
      started.set(e.ref_id, { agent, thread: p.thread_id ?? "" });
    } else if (e.kind === "mention_finished") {
      finished.add(e.ref_id);
    }
  }
  return [...started].filter(([inst]) => !finished.has(inst)).map(([, m]) => m);
}
