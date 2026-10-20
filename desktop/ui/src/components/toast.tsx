// Immediate feedback for saves and commands (desktop-ux.md, UX quality bar).
import { useSyncExternalStore } from "react";
import type { Tone } from "../design/tokens";
import { Icon } from "./Icon";
import { cx } from "./ui";

interface Toast {
  id: number;
  message: string;
  tone: Tone;
}

let toasts: Toast[] = [];
const listeners = new Set<() => void>();
let nextId = 1;

function emit() {
  listeners.forEach((l) => l());
}

export function toast(message: string, tone: Tone = "success", ttlMs = 3500) {
  const item = { id: nextId++, message, tone };
  toasts = [...toasts, item];
  emit();
  setTimeout(() => {
    toasts = toasts.filter((t) => t.id !== item.id);
    emit();
  }, ttlMs);
}

export function Toasts() {
  const items = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => toasts,
    () => toasts,
  );
  return (
    <div className="toasts" aria-live="polite">
      {items.map((t) => (
        <div key={t.id} className={cx("toast", `toast--${t.tone}`)}>
          <Icon name={t.tone === "danger" ? "x" : t.tone === "warning" ? "alert" : "check"} size={16} />
          <span>{t.message}</span>
        </div>
      ))}
    </div>
  );
}
