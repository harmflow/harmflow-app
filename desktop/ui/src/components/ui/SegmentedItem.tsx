// What the two segmented controls share: an option, the name of the group, and the button that draws an option.
import type { ReactNode } from "react";
import { cx } from "./styleHelpers";

export interface SegmentOption<V extends string | number> {
  value: V;
  label: ReactNode;
  /** The option's accessible name, when its label is too short to say it (a weekday's "Mon"). */
  ariaLabel?: string;
  disabled?: boolean;
}

/** What names a segmented control's group: a label of its own, or the id of a caption elsewhere. */
export type SegmentedName = { "aria-label"?: string; "aria-labelledby"?: string };

export function SegmentedItem<V extends string | number>({ option, on, radio, onClick }: { option: SegmentOption<V>; on: boolean; radio: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      role={radio ? "radio" : undefined}
      aria-checked={radio ? on : undefined}
      aria-pressed={radio ? undefined : on}
      aria-label={option.ariaLabel}
      disabled={option.disabled}
      className={cx("segmented__item", on && "segmented__item--active")}
      onClick={onClick}
    >
      {option.label}
    </button>
  );
}
