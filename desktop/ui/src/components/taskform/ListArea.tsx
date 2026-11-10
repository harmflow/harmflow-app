// A list of a custom permission profile, one item per line.
import { useEffect, useState } from "react";
import { linesToList, listToLines } from "../../lib/taskForm";
import { TextArea } from "../ui";

export function ListArea({ value, onValue }: { value: unknown; onValue: (v: string[] | undefined) => void }) {
  const [text, setText] = useState(listToLines(value));
  useEffect(() => setText(listToLines(value)), [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={3}
      className="mono"
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        const list = linesToList(e.target.value);
        onValue(list.length ? list : undefined);
      }}
    />
  );
}
