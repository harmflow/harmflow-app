import type { ReactNode } from "react";
import { cx } from "./styleHelpers";

export function Field({
  label,
  hint,
  error,
  children,
  inline,
}: {
  label: ReactNode;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
  inline?: boolean;
}) {
  return (
    <label className={cx("field", inline && "field--inline", error && "field--error")}>
      <span className="field__label">{label}</span>
      {children}
      {error ? <span className="field__error">{error}</span> : hint ? <span className="field__hint">{hint}</span> : null}
    </label>
  );
}
