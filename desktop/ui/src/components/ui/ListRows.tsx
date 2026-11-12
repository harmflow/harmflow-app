import type { ReactNode } from "react";
import { cx } from "./styleHelpers";

/** A column of list rows, each in its `<li>`; `loose` spaces cards apart. */
export function ListRows({ loose, className, children }: { loose?: boolean; className?: string; children: ReactNode }) {
  return <ul className={cx("list-rows", loose && "list-rows--loose", className)}>{children}</ul>;
}
