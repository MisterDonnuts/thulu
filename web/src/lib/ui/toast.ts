import { esc } from "./dom";
import { icoSvg } from "./icon";

export type ToastKind = "ok" | "err" | "warn" | "info";

const TOAST_ICONS: Record<ToastKind, string> = {
  ok: "i-check",
  err: "i-alert",
  warn: "i-warn",
  info: "i-info",
};

export function toastClasses(kind: ToastKind): string {
  return kind === "info" ? "toast" : `toast ${kind}`;
}

export interface ToastAction {
  label: string;
  run: () => void;
  signal?: AbortSignal;
}

export interface ToastLink {
  href: string;
  label: string;
}

export function makeToast(defaultMs = 3000) {
  return function toast(
    msg: string,
    kind: ToastKind = "info",
    ms = defaultMs,
    link?: ToastLink | null,
    action?: ToastAction,
  ): void {
    const stack = document.getElementById("toast-stack");
    if (!stack) return;
    const el = document.createElement("div");
    el.className = toastClasses(kind);
    if (kind === "err") el.setAttribute("role", "alert");
    const icon = TOAST_ICONS[kind];
    const a = link ? ` <a class="toast-link" href="${esc(link.href)}">${esc(link.label)}</a>` : "";
    el.innerHTML = `${icoSvg(icon)}<span>${esc(msg)}${a}</span>`;
    let closed = false;
    const close = () => {
      if (closed) return;
      closed = true;
      el.classList.add("fade");
      setTimeout(() => el.remove(), 220);
    };
    if (action) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "toast-action";
      btn.textContent = action.label;
      btn.addEventListener("click", () => {
        close();
        action.run();
      });
      el.appendChild(btn);
      action.signal?.addEventListener("abort", close);
    }
    stack.appendChild(el);
    setTimeout(close, ms);
  };
}
