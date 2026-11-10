// The menu a task's or a group's handle opens, with its move choices.
import { useEffect, useRef } from "react";
import { matches } from "../lib/shortcuts";

export type MenuChoice = { label: string; disabled?: boolean; onSelect: () => void };

/** A handle's move choices. It takes the focus when it opens; Escape or a press outside closes it. */
export function MoveMenu({ items, onClose }: { items: MenuChoice[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    ref.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.parentElement?.contains(e.target as Node)) onClose();
    };
    window.addEventListener("mousedown", onDown);
    return () => window.removeEventListener("mousedown", onDown);
  }, []);
  return (
    <div
      ref={ref}
      className="popover popover--start"
      role="menu"
      onKeyDown={(e) => {
        if (matches(e, "close")) {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      {items.map((item) => (
        <button
          key={item.label}
          type="button"
          role="menuitem"
          className="menu-item"
          disabled={item.disabled}
          onClick={() => {
            item.onSelect();
            onClose();
          }}
        >
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
}
