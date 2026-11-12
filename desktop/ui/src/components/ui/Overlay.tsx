import { useEffect, type ReactNode } from "react";
import { matches } from "../../lib/shortcuts";

/** What every overlay shares: a modal dialog that closes on the close shortcut, and on a click on itself, outside its card. */
export function Overlay({ className, label, onClose, children }: { className: string; label?: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => matches(e, "close") && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className={className} role="dialog" aria-modal="true" aria-label={label} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      {children}
    </div>
  );
}
