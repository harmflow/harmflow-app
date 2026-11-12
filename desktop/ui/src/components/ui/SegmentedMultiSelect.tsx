import { SegmentedItem, type SegmentedName, type SegmentOption } from "./SegmentedItem";

/** Several choices drawn as a row of toggle buttons, each pressed while chosen; a click adds or removes its value. */
export function SegmentedMultiSelect<V extends string | number>({
  values,
  options,
  onChange,
  ...name
}: { values: V[]; options: SegmentOption<V>[]; onChange: (values: V[]) => void } & SegmentedName) {
  return (
    <div className="segmented" role="group" {...name}>
      {options.map((o) => {
        const on = values.includes(o.value);
        return (
          <SegmentedItem
            key={o.value}
            option={o}
            on={on}
            radio={false}
            onClick={() => onChange(on ? values.filter((v) => v !== o.value) : [...values, o.value])}
          />
        );
      })}
    </div>
  );
}
