import type { ReactNode } from "react";
import { Icon, type IconName } from "../Icon";

export function EmptyState({ icon = "sparkle", title, hint, action }: { icon?: IconName; title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="empty">
      <Icon name={icon} size={26} className="empty__icon" />
      <p className="empty__title">{title}</p>
      {hint ? <p className="empty__hint">{hint}</p> : null}
      {action}
    </div>
  );
}
