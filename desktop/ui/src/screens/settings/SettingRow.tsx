import type { ReactNode } from "react";

/**
 * One setting in a panel: its label and hint on the left, its control on the right, or under the label where the row is
 * too narrow for both. With `id`, the label names the control of that id.
 */
export function SettingRow({ id, label, hint, children }: { id?: string; label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="setting-row">
      <div className="setting-row__text">
        {id ? (
          <label className="setting-row__label" htmlFor={id}>
            {label}
          </label>
        ) : (
          <span className="setting-row__label">{label}</span>
        )}
        {hint ? <span className="field__hint">{hint}</span> : null}
      </div>
      <div className="setting-row__control">{children}</div>
    </div>
  );
}
