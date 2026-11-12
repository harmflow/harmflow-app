import type { InputHTMLAttributes } from "react";
import { cx } from "./styleHelpers";

/** A text field: `mono` sets it in the monospaced face, `invalid` draws it as a field in error. */
export function TextInput({ mono, invalid, ...props }: InputHTMLAttributes<HTMLInputElement> & { mono?: boolean; invalid?: boolean }) {
  return <input type="text" {...props} className={cx("input", mono && "mono", invalid && "input--error", props.className)} />;
}
