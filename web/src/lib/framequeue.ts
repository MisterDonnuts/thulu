const FRAME_BUDGET_MS = 8;

export interface FrameQueue<T> {
  push(item: T): void;
  clear(): void;
  stop(): void;
}

export function frameQueue<T>(opts: {
  each: (item: T) => void;
  after?: () => void;
  budgetMs?: number;
}): FrameQueue<T> {
  const budget = opts.budgetMs ?? FRAME_BUDGET_MS;
  let items: T[] = [];
  let head = 0;
  let frame = 0;
  let stopped = false;

  const tick = () => {
    frame = 0;
    const start = performance.now();
    while (head < items.length) {
      const item = items[head++];
      try {
        opts.each(item);
      } catch (err) {
        console.error(err);
      }
      if (performance.now() - start >= budget) break;
    }
    if (head >= items.length) {
      items = [];
      head = 0;
    } else {
      frame = requestAnimationFrame(tick);
    }
    opts.after?.();
  };

  return {
    push(item) {
      if (stopped) return;
      items.push(item);
      if (!frame) frame = requestAnimationFrame(tick);
    },
    clear() {
      items = [];
      head = 0;
    },
    stop() {
      stopped = true;
      items = [];
      head = 0;
      if (frame) cancelAnimationFrame(frame);
      frame = 0;
    },
  };
}
