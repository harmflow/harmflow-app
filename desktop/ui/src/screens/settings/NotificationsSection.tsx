import { useTranslation } from "react-i18next";
import { native } from "../../api/native";
import type { Settings } from "../../api/types";
import { Button, Panel, Select } from "../../components/ui";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

export function NotificationsSection() {
  const { t } = useTranslation();
  const settings = useStore((s) => s.settings);
  const patch = usePatch();
  return (
    <Panel title={t("settings.subheads.notices")}>
      <SettingRow id="settings-notification-level" label={t("settings.notificationLevel")} hint={t("settings.notificationLevelHint")}>
        <Select
          id="settings-notification-level"
          value={settings?.notificationLevel ?? "all"}
          onChange={(e) => void patch({ notificationLevel: e.target.value as Settings["notificationLevel"] })}
        >
          {(["all", "failures_and_attention", "none"] as const).map((l) => (
            <option key={l} value={l}>
              {t(`settings.notificationLevels.${l}`)}
            </option>
          ))}
        </Select>
      </SettingRow>
      {native.available ? (
        <SettingRow label={t("settings.notificationPermissionLabel")} hint={t("settings.notificationPermissionHint")}>
          <Button size="sm" icon="bell" onClick={() => void native.ensureNotificationPermission()}>
            {t("settings.notificationPermission")}
          </Button>
        </SettingRow>
      ) : null}
    </Panel>
  );
}
