import type { ButtonHTMLAttributes } from "react";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

export function IconButton({
  icon,
  label,
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return (
    <button type="button" className={cx("icon-btn", className)} aria-label={label} title={label} {...rest}>
      <Icon name={icon} size={18} />
    </button>
  );
}
