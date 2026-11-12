import type { ReactNode } from "react";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

export function Panel({
  title,
  icon,
  actions,
  className,
  children,
  bodyClassName,
}: {
  title?: ReactNode;
  icon?: IconName;
  actions?: ReactNode;
  className?: string;
  bodyClassName?: string;
  children: ReactNode;
}) {
  return (
    <section className={cx("panel", className)}>
      {title ? (
        <header className="panel__header">
          <h2 className="panel__title">
            {icon ? <Icon name={icon} size={18} className="panel__icon" /> : null}
            {title}
          </h2>
          {actions ? <div className="panel__actions">{actions}</div> : null}
        </header>
      ) : null}
      <div className={cx("panel__body", bodyClassName)}>{children}</div>
    </section>
  );
}
