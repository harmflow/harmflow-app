import type { InputHTMLAttributes } from "react";
import { cx } from "./styleHelpers";

export function NumberInput({
  value,
  onValue,
  ...rest
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange"> & { value: number | undefined; onValue: (v: number | undefined) => void }) {
  return (
    <input
      type="number"
      {...rest}
      className={cx("input", "input--number", rest.className)}
      value={value ?? ""}
      onChange={(e) => {
        const raw = e.target.value;
        onValue(raw === "" ? undefined : Number(raw));
      }}
    />
  );
}
