// The field that edits a group's name in the task list.
import { useRef, useState } from "react";
import { TextInput } from "../components/ui";
import { controlKey } from "../lib/shortcuts";

/** A group's name being edited: Enter or leaving the field keeps it, Escape keeps the name it had. */
export function GroupName({ name, label, onDone }: { name: string; label: string; onDone: (name: string) => void }) {
  const [value, setValue] = useState(name);
  const done = useRef(false);
  const finish = (v: string) => {
    if (done.current) return;
    done.current = true;
    onDone(v);
  };
  return (
    <TextInput
      className="task-group__rename"
      aria-label={label}
      autoFocus
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (controlKey(e, "commit")) finish(value);
        if (controlKey(e, "revert")) finish(name);
      }}
      onBlur={() => finish(value)}
    />
  );
}
