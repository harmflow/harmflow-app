// The parameters the cited skills and agents declare, below the task's own fields: one group per skill or agent that
// declares any, each parameter a field of its type with its description and its default.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Catalog, ParameterDeclaration } from "../../api/types";
import { getIn, linesToList, listToLines, type Doc, type Path } from "../../lib/taskForm";
import { Field, FieldGroup, NumberInput, TextArea, TextInput, Toggle } from "../ui";
import type { SetFn } from "./formModel";

/** The kinds of item that declare parameters, in the order the form groups them, and their map under spec.parameters. */
const DECLARING_KINDS = [
  { kind: "skill", key: "skills", cited: ["spec", "skills"] },
  { kind: "agent", key: "agents", cited: ["spec", "agents"] },
] as const;

/** One cited skill or agent that declares parameters. */
export interface DeclaredGroup {
  kind: "skill" | "agent";
  key: "skills" | "agents";
  name: string;
  declared: ParameterDeclaration[];
}

/** The cited skills and agents that declare parameters, in the form's order; empty when none does. */
export function declaredGroups(doc: Doc, catalog?: Catalog): DeclaredGroup[] {
  return DECLARING_KINDS.flatMap(({ kind, key, cited }) =>
    (getIn<string[]>(doc, [...cited]) ?? []).flatMap((name) => {
      const entry = (kind === "skill" ? catalog?.skills : catalog?.agents)?.find((e) => e.name === name);
      const declared = entry?.parameters ?? [];
      return declared.length > 0 ? [{ kind, key, name, declared }] : [];
    }),
  );
}

/**
 * The fields of each group. A value is written under spec.parameters.<skills|agents>.<name>; a cleared field is
 * removed, so the default applies.
 */
export function DeclaredParameters({
  groups,
  doc,
  set,
  errorPaths,
}: {
  groups: DeclaredGroup[];
  doc: Doc;
  set: SetFn;
  errorPaths: Set<string>;
}) {
  const { t } = useTranslation();
  return (
    <>
      {groups.map(({ kind, key, name, declared }) => (
        <div key={`${kind}:${name}`}>
          <h4 className="subhead">{t(`tasks.fields.parametersOf.${kind}`, { name })}</h4>
          {declared.map((d) => {
            const path: Path = ["spec", "parameters", key, name, d.name];
            const error = errorPaths.has("/" + path.join("/")) || errorPaths.has(`/spec/parameters/${key}/${name}`);
            return (
              <ParameterField key={d.name} declaration={d} value={getIn(doc, path)} onChange={(v) => set(path, v)} error={error} />
            );
          })}
        </div>
      ))}
    </>
  );
}

/** One declared parameter as a field of its type; an object is a YAML field, written in YAML's JSON form. */
function ParameterField({
  declaration: d,
  value,
  onChange,
  error,
}: {
  declaration: ParameterDeclaration;
  value: unknown;
  onChange: (v: unknown) => void;
  error: boolean;
}) {
  const { t } = useTranslation();
  const typeLabel = t(`tasks.paramTypes.${d.type}` as never, { defaultValue: d.type }) as string;
  const label = `${d.name}${d.required ? " *" : ""}`;
  const defaultText = d.default === undefined ? undefined : t("common.defaultValue", { value: shown(d.default) });
  const hint = [d.description, typeLabel, defaultText].filter(Boolean).join(" · ");
  const errorText = error ? t("common.error") : undefined;
  switch (d.type) {
    case "boolean":
      // A group, not a field: the toggle is a label of its own.
      return (
        <FieldGroup label={label} hint={errorText ?? hint}>
          <Toggle
            checked={typeof value === "boolean" ? value : d.default === true}
            onChange={(v) => onChange(v === d.default ? undefined : v)}
            label={d.name}
          />
        </FieldGroup>
      );
    case "number":
      return (
        <Field label={label} hint={hint} error={errorText}>
          <NumberInput
            value={typeof value === "number" ? value : undefined}
            placeholder={d.default === undefined ? undefined : shown(d.default)}
            onValue={(v) => onChange(v)}
          />
        </Field>
      );
    case "text_list":
      return (
        <Field label={label} hint={hint} error={errorText}>
          <TextArea rows={3} value={listToLines(value)} onChange={(e) => onChange(linesToList(e.target.value))} />
        </Field>
      );
    case "object":
      return <ObjectField label={label} hint={hint} error={errorText} value={value} onChange={onChange} />;
    default:
      return (
        <Field label={label} hint={hint} error={errorText}>
          <TextInput
            className={d.type === "repository" ? "mono" : undefined}
            placeholder={d.type === "repository" ? "owner/name" : d.default === undefined ? undefined : shown(d.default)}
            value={typeof value === "string" ? value : ""}
            onChange={(e) => onChange(e.target.value)}
          />
        </Field>
      );
  }
}

/** An object parameter's YAML field: its text is kept while it does not parse, and its value written once it does. */
function ObjectField({
  label,
  hint,
  error,
  value,
  onChange,
}: {
  label: string;
  hint: string;
  error?: string;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(() => (value === undefined ? "" : JSON.stringify(value, null, 2)));
  const [unreadable, setUnreadable] = useState(false);
  useEffect(() => {
    // A value changed elsewhere, as in the YAML editor, replaces the text unless the text already reads as it.
    setText((current) => {
      try {
        if (current.trim() !== "" && JSON.stringify(JSON.parse(current)) === JSON.stringify(value)) return current;
      } catch {
        // the text does not parse yet: the value it would write has not changed
        return current;
      }
      return value === undefined ? "" : JSON.stringify(value, null, 2);
    });
  }, [value]);
  const edit = (next: string) => {
    setText(next);
    if (next.trim() === "") {
      setUnreadable(false);
      onChange(undefined);
      return;
    }
    try {
      onChange(JSON.parse(next));
      setUnreadable(false);
    } catch {
      setUnreadable(true);
    }
  };
  return (
    <Field label={label} hint={hint} error={unreadable ? t("tasks.fields.parameterUnreadable") : error}>
      <TextArea className="mono" rows={5} value={text} onChange={(e) => edit(e.target.value)} />
    </Field>
  );
}

/** A default or a value as the form shows it. */
function shown(v: unknown): string {
  if (Array.isArray(v)) return v.join(", ");
  if (typeof v === "object" && v !== null) return JSON.stringify(v);
  return String(v);
}
