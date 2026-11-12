import type { TextareaHTMLAttributes } from "react";
import { cx } from "./styleHelpers";

export function TextArea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cx("input", "input--area", props.className)} />;
}
