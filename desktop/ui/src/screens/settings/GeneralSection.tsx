import { useTranslation } from "react-i18next";
import { native } from "../../api/native";
import type { Settings } from "../../api/types";
import { Panel, Select, Toggle } from "../../components/ui";
import { applyLanguage } from "../../i18n";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

export function GeneralSection() {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();

  const setLanguage = async (language: Settings["language"]) => {
    applyLanguage(language);
    await patch({ language }, true);
  };

  const setTray = async (enabled: boolean) => {
    const next = await patch({ trayEnabled: enabled });
    if (next) {
      await native.setTrayEnabled(enabled);
      if (next.autostart.enabled) await native.setLoginItem(enabled).catch(() => undefined);
    }
  };

  const setKeepTrayOnQuit = async (keep: boolean) => {
    if (await patch({ keepTrayOnQuit: keep })) await native.setKeepTrayOnQuit(keep);
  };

  return (
    <>
      <Panel title={t("settings.subheads.language")}>
        <SettingRow id="settings-language" label={t("settings.language")} hint={t("settings.languageHint")}>
          <Select
            id="settings-language"
            value={settings?.language ?? "system"}
            onChange={(e) => void setLanguage(e.target.value as Settings["language"])}
          >
            {(["system", "pt-BR", "en-US", "es-ES"] as const).map((l) => (
              <option key={l} value={l}>
                {t(`settings.languages.${l}`)}
              </option>
            ))}
          </Select>
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.menuBar")}>
        <SettingRow id="settings-tray" label={t("settings.tray")} hint={t("settings.trayHint")}>
          <Toggle id="settings-tray" checked={settings?.trayEnabled ?? true} onChange={(v) => void setTray(v)} />
        </SettingRow>
        {/* It depends on the tray: while the tray is off, ⌘Q quits. */}
        <SettingRow id="settings-keep-tray" label={t("settings.keepTrayOnQuit")} hint={t("settings.keepTrayOnQuitHint")}>
          <Toggle
            id="settings-keep-tray"
            checked={settings?.keepTrayOnQuit ?? false}
            onChange={(v) => void setKeepTrayOnQuit(v)}
            disabled={!settings?.trayEnabled}
          />
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.executions")}>
        <SettingRow
          id="settings-restriction-warnings"
          label={t("settings.showRestrictionWarnings")}
          hint={t("settings.showRestrictionWarningsHint")}
        >
          <Toggle
            id="settings-restriction-warnings"
            checked={settings?.showRestrictionWarnings ?? true}
            onChange={(v) => void patch({ showRestrictionWarnings: v })}
          />
        </SettingRow>
      </Panel>
    </>
  );
}
