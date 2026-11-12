import { SegmentedItem, type SegmentedName, type SegmentOption } from "./SegmentedItem";

/** A single choice drawn as a row of buttons: a radio group, each option a radio checked while chosen. */
export function SegmentedControl<V extends string | number>({
  value,
  options,
  onChange,
  ...name
}: { value: V; options: SegmentOption<V>[]; onChange: (v: V) => void } & SegmentedName) {
  return (
    <div className="segmented" role="radiogroup" {...name}>
      {options.map((o) => (
        <SegmentedItem key={o.value} option={o} on={o.value === value} radio onClick={() => onChange(o.value)} />
      ))}
    </div>
  );
}
