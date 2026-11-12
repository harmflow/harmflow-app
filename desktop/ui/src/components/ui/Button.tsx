import type { ButtonHTMLAttributes } from "react";
import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

/** `link` draws a text-styled action: no padding, border, or background, the link color, and an underline on hover. */
type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-outline" | "link";

export function Button({
  variant = "secondary",
  icon,
  size = "md",
  className,
  children,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; icon?: IconName; size?: "sm" | "md" | "lg" }) {
  return (
    <button type="button" className={cx("btn", `btn--${variant}`, `btn--${size}`, className)} {...rest}>
      {icon ? <Icon name={icon} size={size === "sm" ? 14 : size === "lg" ? 20 : 16} /> : null}
      {children ? <span>{children}</span> : null}
    </button>
  );
}
