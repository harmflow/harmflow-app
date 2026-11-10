// An editable list of rows of text fields: a remove button on each row, and an add button below.
import type { ReactNode } from "react";
import { Button, IconButton, TextInput } from "./ui";

/** One column of a RowListEditor: a text field in each row. */
export type RowListColumn<R> = {
  /** The column's header, and the accessible name of its field in each row. */
  label: string;
  placeholder?: string;
  /** The column's track in the row's grid, such as `minmax(0, 1fr)` or `130px`. */
  width: string;
  mono?: boolean;
  value: (row: R) => string;
  /** The row with this column set to what the user typed. */
  set: (row: R, value: string) => R;
  /** Whether the field of row `index` is marked as an error. */
  error?: (row: R, index: number) => boolean;
};

/** The width of the column of remove buttons, an IconButton's. */
const REMOVE_WIDTH = "32px";

export function RowListEditor<R>({
  rows,
  columns,
  empty,
  onChange,
  addLabel,
  removeLabel,
  header,
  hint,
  actions,
}: {
  rows: R[];
  columns: RowListColumn<R>[];
  /** A new, empty row, which the add button appends. */
  empty: () => R;
  onChange: (rows: R[]) => void;
  addLabel: string;
  removeLabel: string;
  /** Draws a header row of the columns' labels above the rows. */
  header?: boolean;
  /** A line above the rows. */
  hint?: ReactNode;
  /** Buttons beside the add button, such as a Save. */
  actions?: ReactNode;
}) {
  const layout = { gridTemplateColumns: [...columns.map((c) => c.width), REMOVE_WIDTH].join(" ") };
  const add = (
    <Button size="sm" icon="plus" onClick={() => onChange([...rows, empty()])}>
      {addLabel}
    </Button>
  );
  return (
    <div className="row-list">
      {hint ? <p className="field__hint">{hint}</p> : null}
      {header ? (
        <div className="row-list__row row-list__head" style={layout}>
          {columns.map((c) => (
            <span key={c.label}>{c.label}</span>
          ))}
          <span />
        </div>
      ) : null}
      {rows.map((row, i) => (
        <div className="row-list__row" style={layout} key={i}>
          {columns.map((c) => (
            <TextInput
              key={c.label}
              mono={c.mono}
              invalid={c.error?.(row, i)}
              aria-label={c.label}
              placeholder={c.placeholder}
              value={c.value(row)}
              onChange={(e) => onChange(rows.map((r, j) => (j === i ? c.set(r, e.target.value) : r)))}
            />
          ))}
          <IconButton icon="trash" label={removeLabel} onClick={() => onChange(rows.filter((_, j) => j !== i))} />
        </div>
      ))}
      {actions ? (
        <div className="inline-actions">
          {add}
          {actions}
        </div>
      ) : (
        add
      )}
    </div>
  );
}
