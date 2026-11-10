// A select of one of the form's closed sets, whose empty option names the default.
import { useTranslation } from "react-i18next";
import { schemaEnum } from "../../lib/schema";
import { getIn, type Doc, type Path } from "../../lib/taskForm";
import { Field, Select } from "../ui";
import { OPTION_FIELDS, type SetFn } from "./formModel";

export function EnumField({
  label,
  optionKey,
  doc,
  set,
  defLabel,
}: {
  label: string;
  optionKey: keyof typeof OPTION_FIELDS;
  doc: Doc;
  set: SetFn;
  defLabel: (path: Path, label?: (v: string) => string) => string;
}) {
  const { t } = useTranslation();
  const path = OPTION_FIELDS[optionKey];
  const optionLabel = (v: string) => t(`tasks.options.${optionKey}.${v}` as never, { defaultValue: v }) as string;
  return (
    <Field label={label}>
      <Select value={getIn<string>(doc, path) ?? ""} onChange={(e) => set(path, e.target.value || undefined)}>
        <option value="">{defLabel(path, optionLabel)}</option>
        {schemaEnum(path).map((v) => (
          <option key={v} value={v}>
            {optionLabel(v)}
          </option>
        ))}
      </Select>
    </Field>
  );
}
