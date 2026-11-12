import type { SelectHTMLAttributes } from "react";
import { Icon } from "../Icon";
import { cx } from "./styleHelpers";

export function Select({ children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="select">
      <select {...props} className={cx("input", "select__control", props.className)}>
        {children}
      </select>
      <Icon name="chevronDown" size={14} className="select__chevron" />
    </span>
  );
}
