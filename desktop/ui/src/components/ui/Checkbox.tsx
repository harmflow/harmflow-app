import type { ReactNode } from "react";
import { Icon } from "../Icon";
import { cx } from "./styleHelpers";

/** A checkbox. `compact` draws it at the size of a toolbar's small monospace text, as the event timeline's tools are. */
export function Checkbox({
  checked,
  onChange,
  label,
  disabled,
  compact,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: ReactNode;
  disabled?: boolean;
  compact?: boolean;
}) {
  return (
    <label className={cx("checkbox", compact && "checkbox--compact", disabled && "checkbox--disabled")}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="checkbox__box" aria-hidden="true">
        <Icon name="check" size={compact ? 10 : 12} />
      </span>
      <span>{label}</span>
    </label>
  );
}
