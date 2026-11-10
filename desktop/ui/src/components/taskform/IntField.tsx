// A whole-number field of the task document, with the schema's range and default.
import { useTranslation } from "react-i18next";
import { schemaDefault, schemaRange } from "../../lib/schema";
import { getIn, type Doc, type Path } from "../../lib/taskForm";
import { Field, NumberInput } from "../ui";
import type { SetFn } from "./formModel";

export function IntField({ path, label, doc, set }: { path: Path; label: string; doc: Doc; set: SetFn }) {
  const { t } = useTranslation();
  const d = schemaDefault<number>(path);
  const range = schemaRange(path);
  return (
    <Field label={label}>
      <NumberInput
        value={getIn<number>(doc, path)}
        onValue={(v) => set(path, v)}
        min={range.min}
        max={range.max}
        placeholder={d !== undefined ? t("common.defaultValue", { value: d }) : undefined}
      />
    </Field>
  );
}
