// The agents or skills a task names, as chips, with the catalog's entries to add from.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { CatalogEntry } from "../../api/types";
import { controlKey } from "../../lib/shortcuts";
import { Chip, Field } from "../ui";

export function NamePicker({
  label,
  entries,
  value,
  onChange,
}: {
  label: string;
  entries: CatalogEntry[];
  value: string[];
  onChange: (v: string[] | undefined) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const listId = useMemo(() => `names-${Math.random().toString(36).slice(2)}`, []);
  const add = (name: string) => {
    const n = name.trim();
    if (!n || value.includes(n)) return;
    onChange([...value, n]);
    setDraft("");
  };
  return (
    <Field label={label}>
      <div className="name-picker">
        <div className="name-picker__chips">
          {value.map((name) => {
            const entry = entries.find((e) => e.name === name);
            return (
              <Chip
                key={name}
                tone={entry ? "violet" : "warning"}
                title={entry?.description}
                onRemove={() => onChange(value.filter((v) => v !== name))}
                removeLabel={t("common.remove")}
              >
                <span className="mono">{name}</span>
                <span className="name-picker__source">
                  {entry ? (entry.source === "builtin" ? t("tasks.fields.builtin") : t("tasks.fields.user")) : t("tasks.fields.notFound")}
                </span>
              </Chip>
            );
          })}
        </div>
        <input
          className="input mono"
          list={listId}
          value={draft}
          placeholder={t("tasks.fields.addName")}
          onChange={(e) => {
            const v = e.target.value;
            if (entries.some((x) => x.name === v)) add(v);
            else setDraft(v);
          }}
          onKeyDown={(e) => {
            if (controlKey(e, "commit")) {
              e.preventDefault();
              add(draft);
            }
          }}
          onBlur={() => add(draft)}
        />
        <datalist id={listId}>
          {entries
            .filter((e) => !value.includes(e.name))
            .map((e) => (
              <option key={e.name} value={e.name}>
                {e.source === "builtin" ? t("tasks.fields.builtin") : t("tasks.fields.user")} · {e.harnesses.join(", ")}
              </option>
            ))}
        </datalist>
      </div>
    </Field>
  );
}
