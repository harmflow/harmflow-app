import { useId, type ReactNode } from "react";

/**
 * A field whose control is a group, such as a row of checkboxes. Its caption names the group, which sits in a
 * container of `className`, and labels none of its controls: a `<label>` would name, and toggle, the first of them.
 */
export function FieldGroup({ label, hint, className, children }: { label: ReactNode; hint?: ReactNode; className?: string; children: ReactNode }) {
  const id = useId();
  return (
    <div className="field">
      <span className="field__label" id={id}>
        {label}
      </span>
      <div className={className} role="group" aria-labelledby={id}>
        {children}
      </div>
      {hint ? <span className="field__hint">{hint}</span> : null}
    </div>
  );
}
