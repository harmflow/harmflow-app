import type { Tone } from "../../design/theme";
import { cx } from "./styleHelpers";

/** An indeterminate activity line: work is happening, with no invented percentage. */
export function ScanBar({ active, tone = "cyan" }: { active: boolean; tone?: Tone }) {
  return (
    <div className={cx("scanbar", active && "scanbar--active", `scanbar--${tone}`)} aria-hidden="true">
      <span />
    </div>
  );
}
