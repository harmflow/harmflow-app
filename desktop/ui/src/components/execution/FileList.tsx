import { useTranslation } from "react-i18next";
import { Icon } from "../Icon";
import { EmptyState } from "../ui";

export function FileList({ files }: { files: string[] }) {
  const { t } = useTranslation();
  if (files.length === 0) return <EmptyState icon="file" title={t("executions.detail.noFiles")} />;
  return (
    <ul className="file-list">
      {files.map((f) => (
        <li key={f} className="mono">
          <Icon name="file" size={14} /> {f}
        </li>
      ))}
    </ul>
  );
}
