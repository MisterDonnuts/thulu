import { randomUUID } from "node:crypto";

import { t } from "../i18n.js";

export class ExpiringStore {
  #items = new Map();
  #ttlMs;
  #max;
  #onEvict;

  constructor({ ttlMs, max, onEvict = () => {} }) {
    this.#ttlMs = ttlMs;
    this.#max = max;
    this.#onEvict = onEvict;
    setInterval(() => this.sweep(), 60_000).unref();
  }

  put(value, id = randomUUID().slice(0, 12)) {
    this.#evict(id);
    this.#items.set(id, { value, expires: Date.now() + this.#ttlMs });
    while (this.#items.size > this.#max) this.#evict(this.#items.keys().next().value);
    return id;
  }

  get(id) {
    const hit = this.#items.get(id);
    if (!hit) return undefined;
    hit.expires = Date.now() + this.#ttlMs;
    return hit.value;
  }

  require(id, what) {
    const v = this.get(id);
    if (v === undefined) throw new Error(t("errors.not_found_or_expired", { what, id }));
    return v;
  }

  #evict(id) {
    const hit = this.#items.get(id);
    if (!hit) return;
    this.#items.delete(id);
    Promise.resolve(this.#onEvict(hit.value)).catch(() => {});
  }

  sweep(now = Date.now()) {
    for (const [id, hit] of this.#items) if (hit.expires < now) this.#evict(id);
  }

  clear() {
    for (const id of [...this.#items.keys()]) this.#evict(id);
  }
}
