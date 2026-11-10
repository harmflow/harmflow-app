// The fields of a script task, with the text areas of its argument lists and its environment.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { schemaEnum } from "../../lib/schema";
import { envToLines, getIn, linesToArgs, linesToEnv, listToLines, programKind, setProgramKind, type Doc, type Path } from "../../lib/taskForm";
import { Checkbox, Field, FieldGroup, SegmentedControl, TextArea, TextInput } from "../ui";
import type { SetFn } from "./formModel";

/** A script task's program, in either form, its working directory, environment, and systems. */
export function ScriptFields({ doc, onChange, set, err }: { doc: Doc; onChange: (doc: Doc) => void; set: SetFn; err: (path: Path) => string | undefined }) {
  const { t } = useTranslation();
  const kind = programKind(doc);
  const systems = getIn<string[]>(doc, ["spec", "script", "systems"]) ?? [];
  return (
    <>
      <SegmentedControl
        aria-label={t("tasks.fields.programKind")}
        value={kind}
        options={[
          { value: "command", label: t("tasks.fields.programCommand") },
          { value: "inline", label: t("tasks.fields.programInline") },
        ]}
        onChange={(k) => onChange(setProgramKind(doc, k))}
      />
      {kind === "command" ? (
        <Field label={t("tasks.fields.command")} hint={t("tasks.fields.commandHint")} error={err(["spec", "script", "command"]) ?? err(["spec", "script", "command", 0])}>
          <ArgsArea rows={4} value={getIn(doc, ["spec", "script", "command"])} onValue={(v) => set(["spec", "script", "command"], v)} placeholder={"/usr/bin/python3\n/Users/me/jobs/audit.py\n--weekly"} />
        </Field>
      ) : (
        <>
          <Field label={t("tasks.fields.interpreter")} hint={t("tasks.fields.interpreterHint")} error={err(["spec", "script", "inline", "interpreter"])}>
            <ArgsArea rows={2} value={getIn(doc, ["spec", "script", "inline", "interpreter"])} onValue={(v) => set(["spec", "script", "inline", "interpreter"], v)} placeholder={"/bin/zsh"} />
          </Field>
          <Field label={t("tasks.fields.source")} error={err(["spec", "script", "inline", "source"])}>
            <TextArea
              className="mono input--prompt"
              rows={10}
              value={getIn<string>(doc, ["spec", "script", "inline", "source"]) ?? ""}
              onChange={(e) => set(["spec", "script", "inline", "source"], e.target.value)}
            />
          </Field>
        </>
      )}
      <div className="grid-2">
        <Field label={t("tasks.fields.workingDirectory")} hint={t("tasks.fields.workingDirectoryHint")} error={err(["spec", "script", "workingDirectory"])}>
          <TextInput
            mono
            value={getIn<string>(doc, ["spec", "script", "workingDirectory"]) ?? ""}
            placeholder="~"
            onChange={(e) => set(["spec", "script", "workingDirectory"], e.target.value)}
          />
        </Field>
        <FieldGroup label={t("tasks.fields.systems")} hint={t("tasks.fields.systemsHint")} className="check-row">
          {schemaEnum(["spec", "script", "systems"]).map((system) => (
            <Checkbox
              key={system}
              checked={systems.includes(system)}
              label={t(`tasks.options.systems.${system}` as never, { defaultValue: system }) as string}
              onChange={(on) => set(["spec", "script", "systems"], on ? [...systems, system] : systems.filter((s) => s !== system))}
            />
          ))}
        </FieldGroup>
      </div>
      <Field label={t("tasks.fields.environment")} hint={t("tasks.fields.environmentHint")} error={err(["spec", "script", "environment"])}>
        <EnvArea value={getIn(doc, ["spec", "script", "environment"])} onValue={(v) => set(["spec", "script", "environment"], v)} />
      </Field>
    </>
  );
}

/** An argument list, one argument per line, kept as typed: spaces inside an argument are its own. */
function ArgsArea({ value, onValue, rows, placeholder }: { value: unknown; onValue: (v: string[] | undefined) => void; rows: number; placeholder?: string }) {
  const [text, setText] = useState(listToLines(value));
  useEffect(() => {
    if (JSON.stringify(linesToArgs(text)) !== JSON.stringify(value ?? [])) setText(listToLines(value));
  }, [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={rows}
      className="mono"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const args = linesToArgs(e.target.value);
        onValue(args.length ? args : undefined);
      }}
    />
  );
}

function EnvArea({ value, onValue }: { value: unknown; onValue: (v: Record<string, string> | undefined) => void }) {
  const [text, setText] = useState(envToLines(value));
  useEffect(() => {
    if (JSON.stringify(linesToEnv(text)) !== JSON.stringify(value ?? {})) setText(envToLines(value));
  }, [JSON.stringify(value)]); // keep in sync with YAML edits
  return (
    <TextArea
      rows={3}
      className="mono"
      value={text}
      placeholder="TZ=America/Sao_Paulo"
      onChange={(e) => {
        setText(e.target.value);
        const env = linesToEnv(e.target.value);
        onValue(Object.keys(env).length ? env : undefined);
      }}
    />
  );
}
