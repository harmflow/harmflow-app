import type { ReactNode } from "react";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

/**
 * A row of a list: a leading mark or icon, a title over a secondary line and an optional third one, then what sits
 * on its right — `aside` as it is drawn (a chip, a harness label), and `trailing` in the muted small text of a time.
 * With `onClick` it is a button; without, it shows the same row and does nothing. A `card` row has a border and a
 * sunken ground, as the Executions screen's aside draws them; a `tone` marks its left edge, as a notice's severity.
 */
export function ListRow({
  leading,
  icon,
  title,
  secondary,
  wrap,
  meta,
  aside,
  trailing,
  onClick,
  card,
  tone,
  alignTop,
}: {
  leading?: ReactNode;
  icon?: IconName;
  title: ReactNode;
  secondary?: ReactNode;
  /** The secondary line wraps, breaking a long path anywhere, where it is cut to one line otherwise. */
  wrap?: boolean;
  meta?: ReactNode;
  aside?: ReactNode;
  trailing?: ReactNode;
  onClick?: () => void;
  card?: boolean;
  tone?: "info" | "warning" | "error";
  alignTop?: boolean;
}) {
  const className = cx("list-row", card && "list-row--card", tone && `list-row--${tone}`, alignTop && "list-row--top");
  const content = (
    <>
      {icon ? (
        <span className="list-row__icon">
          <Icon name={icon} size={18} />
        </span>
      ) : (
        leading
      )}
      <span className="list-row__text">
        <span className="list-row__title">{title}</span>
        {secondary !== undefined ? <span className={cx("list-row__sub", wrap && "list-row__sub--wrap")}>{secondary}</span> : null}
        {meta !== undefined ? <span className="list-row__meta">{meta}</span> : null}
      </span>
      {aside}
      {trailing !== undefined ? <span className="list-row__trail">{trailing}</span> : null}
    </>
  );
  return onClick ? (
    <button type="button" className={className} onClick={onClick}>
      {content}
    </button>
  ) : (
    <div className={className}>{content}</div>
  );
}
