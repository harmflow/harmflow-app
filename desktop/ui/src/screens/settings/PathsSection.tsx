import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { RowListEditor } from "../../components/RowListEditor";
import { Button, Panel } from "../../components/ui";
import { useDraft, type DraftReport } from "../../lib/hooks";
import { useStore } from "../../state/store";
import { usePatch } from "./usePatch";

export function PathsSection({ report }: { report: DraftReport }) {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();
  return (
    <>
      <Panel title={t("settings.subheads.variables")}>
        <PathVariables value={settings?.pathVariables ?? {}} onSave={(v) => void patch({ pathVariables: v })} onDraft={report} />
      </Panel>
      <Panel title={t("settings.subheads.allowedFolders")}>
        <AllowedFolders value={settings?.allowedFolders ?? []} onSave={(v) => void patch({ allowedFolders: v })} onDraft={report} />
      </Panel>
    </>
  );
}

function PathVariables({ value, onSave, onDraft }: { value: Record<string, string>; onSave: (v: Record<string, string>) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<Array<[string, string]>>(Object.entries(value));
  useEffect(() => setRows(Object.entries(value)), [JSON.stringify(value)]);
  const changed = JSON.stringify(Object.fromEntries(rows.filter(([k]) => k.trim()))) !== JSON.stringify(value);
  useDraft(onDraft, "pathVariables", changed);
  return (
    <RowListEditor<[string, string]>
      rows={rows}
      onChange={setRows}
      empty={() => ["", ""]}
      hint={t("settings.variablesHint")}
      columns={[
        {
          label: t("settings.variableName"),
          placeholder: t("settings.variableName"),
          width: "minmax(0, 0.8fr)",
          mono: true,
          value: ([k]) => k,
          set: ([, v], k) => [k.toUpperCase().replace(/[^A-Z0-9_]/g, "_"), v],
        },
        {
          label: t("settings.variableValue"),
          placeholder: t("settings.variableValue"),
          width: "minmax(0, 1.4fr)",
          mono: true,
          value: ([, v]) => v,
          set: ([k], v) => [k, v],
        },
      ]}
      addLabel={t("settings.addVariable")}
      removeLabel={t("common.remove")}
      actions={
        <Button size="sm" variant="primary" disabled={!changed} onClick={() => onSave(Object.fromEntries(rows.filter(([k]) => k.trim())))}>
          {t("common.save")}
        </Button>
      }
    />
  );
}

// The folders, beside the home folder and the mounted volumes, where a task's paths may lead.
// The daemon refuses one where no harness may write, and a change that would leave a saved task outside them.
function AllowedFolders({ value, onSave, onDraft }: { value: string[]; onSave: (v: string[]) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<string[]>(value);
  useEffect(() => setRows(value), [JSON.stringify(value)]);
  const kept = rows.map((r) => r.trim()).filter(Boolean);
  const changed = JSON.stringify(kept) !== JSON.stringify(value);
  useDraft(onDraft, "allowedFolders", changed);
  return (
    <RowListEditor<string>
      rows={rows}
      onChange={setRows}
      empty={() => ""}
      hint={t("settings.allowedFoldersHint")}
      columns={[
        {
          label: t("settings.allowedFolder"),
          placeholder: t("settings.allowedFolderPlaceholder"),
          width: "minmax(0, 1fr)",
          mono: true,
          value: (f) => f,
          set: (_, f) => f,
        },
      ]}
      addLabel={t("settings.addAllowedFolder")}
      removeLabel={t("common.remove")}
      actions={
        <Button size="sm" variant="primary" disabled={!changed} onClick={() => onSave(kept)}>
          {t("common.save")}
        </Button>
      }
    />
  );
}
