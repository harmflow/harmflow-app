import type { ReactNode } from "react";
import type { Tone } from "../../design/theme";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

/** A short label in a tone. Given `onRemove`, it ends in a remove button named `removeLabel`. */
export function Chip({
  tone = "neutral",
  icon,
  children,
  title,
  onRemove,
  removeLabel,
}: {
  tone?: Tone;
  icon?: IconName;
  children: ReactNode;
  title?: string;
  onRemove?: () => void;
  removeLabel?: string;
}) {
  return (
    <span className={cx("chip", `chip--${tone}`, onRemove && "chip--removable")} title={title}>
      {icon ? <Icon name={icon} size={12} /> : null}
      {children}
      {onRemove ? (
        <button type="button" className="chip__remove" aria-label={removeLabel} onClick={onRemove}>
          <Icon name="x" size={12} />
        </button>
      ) : null}
    </span>
  );
}
