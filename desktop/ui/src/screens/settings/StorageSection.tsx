import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import { native } from "../../api/native";
import type { CleanResult } from "../../api/types";
import { toast } from "../../components/toast";
import { Banner, Button, IconButton, KeyValue, NumberInput, Panel } from "../../components/ui";
import { formatBytes } from "../../lib/format";
import { useDraft, type DraftReport } from "../../lib/hooks";
import { useStore } from "../../state/store";
import { DIRECTORIES } from "./directories";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

export function StorageSection({ report }: { report: DraftReport }) {
  const { t, i18n } = useTranslation();
  const settings = useStore((s) => s.settings);
  const diagnostics = useStore((s) => s.diagnostics);
  const patch = usePatch();
  const [clean, setClean] = useState<CleanResult | null>(null);
  const [cleaning, setCleaning] = useState(false);

  const cleanWorkspaces = async () => {
    setCleaning(true);
    try {
      setClean(await api.cleanWorkspaces());
    } catch (err) {
      toast((err as Error).message, "danger");
    } finally {
      setCleaning(false);
    }
  };

  return (
    <>
      <Panel title={t("settings.subheads.logs")}>
        <SettingRow id="settings-retention" label={t("settings.logRetentionDays")} hint={t("settings.logRetentionHint")}>
          <RetentionField
            id="settings-retention"
            value={settings?.logRetentionDays}
            onSave={(days) => void patch({ logRetentionDays: days })}
            onDraft={report}
          />
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.workspaces")}>
        <SettingRow
          label={
            diagnostics?.diskUsageBytes !== undefined ? (
              <>
                {t("settings.diskUsage")}: <strong>{formatBytes(diagnostics.diskUsageBytes, i18n.language)}</strong>
              </>
            ) : (
              t("settings.diskUsage")
            )
          }
          hint={t("settings.cleanWorkspacesHint")}
        >
          <Button icon="trash" disabled={cleaning} onClick={() => void cleanWorkspaces()}>
            {t("settings.cleanWorkspaces")}
          </Button>
        </SettingRow>
        {clean ? (
          <Banner tone="success" icon="check">
            {t("settings.cleanResult", { removed: clean.removed.length, kept: clean.kept.length })}
            <ul className="clean-list mono">
              {clean.removed.map((p) => (
                <li key={p}>− {p}</li>
              ))}
              {clean.kept.map((k) => (
                <li key={k.path} className="muted">
                  = {k.path} — {t("settings.keptReason", { reason: k.reason })}
                </li>
              ))}
            </ul>
          </Banner>
        ) : null}
      </Panel>
      <Panel title={t("settings.subheads.directories")}>
        {diagnostics ? (
          <KeyValue
            items={DIRECTORIES.filter((k) => diagnostics[k])
              .map((k) => [
                t(`settings.dirs.${k}`),
                <span className="dir-row">
                  <span className="mono">{diagnostics[k]}</span>
                  {native.available ? <IconButton icon="external" label={t("common.openInFinder")} onClick={() => void native.openPath(diagnostics[k]!)} /> : null}
                </span>,
              ])}
          />
        ) : null}
      </Panel>
    </>
  );
}

function RetentionField({ id, value, onSave, onDraft }: { id: string; value?: number; onSave: (days: number) => void; onDraft: DraftReport }) {
  const { t } = useTranslation();
  const [days, setDays] = useState<number | undefined>(value);
  useEffect(() => setDays(value), [value]);
  useDraft(onDraft, "retention", days !== value);
  return (
    <div className="inline-form">
      <NumberInput id={id} value={days} onValue={setDays} min={1} max={3650} />
      <Button size="sm" disabled={!days || days === value} onClick={() => days && onSave(days)}>
        {t("common.apply")}
      </Button>
    </div>
  );
}
