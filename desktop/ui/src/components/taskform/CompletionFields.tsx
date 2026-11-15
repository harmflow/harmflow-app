// What an execution of a task that runs a harness does once it ends completed: wait for the schedule, or run the task
// again at once, up to the run-again limit, which only Run again uses.
import { useTranslation } from "react-i18next";
import { schemaDefault, schemaEnum, schemaRange } from "../../lib/schema";
import { getIn, type Doc, type Path } from "../../lib/taskForm";
import { Field, NumberInput, Select } from "../ui";
import { OPTION_FIELDS, type SetFn } from "./formModel";

const LIMIT: Path = ["spec", "completion", "runAgainLimit"];
const OPTION_KEY = "completion" satisfies keyof typeof OPTION_FIELDS;

export function CompletionFields({ doc, set, err }: { doc: Doc; set: SetFn; err: (path: Path) => string | undefined }) {
  const { t } = useTranslation();
  const path = OPTION_FIELDS[OPTION_KEY];
  // A task that names no policy has the schema's default, which the select shows as chosen.
  const policy = getIn<string>(doc, path) ?? schemaDefault<string>(path) ?? "";
  const limitDefault = schemaDefault<number>(LIMIT);
  const range = schemaRange(LIMIT);
  return (
    <div className="grid-2">
      <Field label={t("tasks.fields.completion")} error={err(path)}>
        <Select value={policy} onChange={(e) => set(path, e.target.value)}>
          {schemaEnum(path).map((v) => (
            <option key={v} value={v}>
              {t(`tasks.options.${OPTION_KEY}.${v}` as never, { defaultValue: v }) as string}
            </option>
          ))}
        </Select>
      </Field>
      <Field label={t("tasks.fields.runAgainLimit")} hint={t("tasks.fields.runAgainLimitHint")} error={err(LIMIT)}>
        <NumberInput
          value={getIn<number>(doc, LIMIT)}
          onValue={(v) => set(LIMIT, v)}
          min={range.min}
          max={range.max}
          step={1}
          disabled={policy !== "run_again"}
          placeholder={limitDefault !== undefined ? t("common.defaultValue", { value: limitDefault }) : undefined}
        />
      </Field>
    </div>
  );
}
