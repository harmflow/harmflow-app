// The repositories of a GitHub task, one row each.
import { useTranslation } from "react-i18next";
import { schemaDefault } from "../../lib/schema";
import { RowListEditor } from "../RowListEditor";
import { cleanRow, type Item } from "./formModel";

export function RepositoryList({ rows, onChange, errorPaths }: { rows: Item[]; onChange: (rows: Item[] | undefined) => void; errorPaths: Set<string> }) {
  const { t } = useTranslation();
  const base = "/spec/github/repositories";
  const cell = (key: string) => ({
    value: (row: Item) => (row[key] as string) ?? "",
    set: (row: Item, v: string) => cleanRow({ ...row, [key]: v }),
  });
  return (
    <RowListEditor<Item>
      rows={rows}
      onChange={onChange}
      empty={() => ({ name: "", localPath: "", baseBranch: "main" })}
      header
      columns={[
        {
          label: t("tasks.fields.repository"),
          placeholder: "owner/repo",
          width: "minmax(0, 1fr)",
          mono: true,
          error: (_, i) => errorPaths.has(`${base}/${i}/name`),
          ...cell("name"),
        },
        {
          label: t("tasks.fields.localPath"),
          placeholder: "${PROJECTS_ROOT}/repo",
          width: "minmax(0, 1.3fr)",
          mono: true,
          error: (_, i) => errorPaths.has(`${base}/${i}/localPath`),
          ...cell("localPath"),
        },
        {
          label: t("tasks.fields.baseBranch"),
          placeholder: String(schemaDefault(["spec", "github", "repositories", 0, "baseBranch"]) ?? "main"),
          width: "130px",
          mono: true,
          ...cell("baseBranch"),
        },
      ]}
      addLabel={t("tasks.fields.addRepository")}
      removeLabel={t("common.remove")}
    />
  );
}
