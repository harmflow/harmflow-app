import type { ReactNode } from "react";
import { cx } from "./styleHelpers";

/** A switch. Without a label of its own, a `<label htmlFor={id}>` elsewhere names it, as a settings row's does. */
export function Toggle({
  id,
  checked,
  onChange,
  label,
  disabled,
}: {
  id?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={cx("toggle", disabled && "toggle--disabled")}>
      <input id={id} type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle__track" aria-hidden="true">
        <span className="toggle__thumb" />
      </span>
      {label ? <span className="toggle__label">{label}</span> : null}
    </label>
  );
}
