import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import { native } from "../../api/native";
import { toast } from "../../components/toast";
import { Button, Panel } from "../../components/ui";
import { useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";

export function SecuritySection() {
  const { t } = useTranslation();
  const diagnostics = useStore((s) => s.diagnostics);

  const rotate = async () => {
    if (!(await native.confirm(t("settings.rotateConfirm"), t("settings.rotateConfirmTitle"), t("common.confirm"), t("common.cancel")))) return;
    try {
      await api.rotateToken();
      toast(t("settings.rotated"));
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  return (
    <Panel title={t("settings.subheads.localApi")}>
      <SettingRow label={t("settings.tokenStore")}>{diagnostics ? t(`settings.tokenStores.${diagnostics.tokenStore}`) : "—"}</SettingRow>
      <SettingRow label={t("settings.apiToken")} hint={t("settings.apiTokenHint")}>
        <Button icon="key" onClick={() => void rotate()}>
          {t("settings.rotateToken")}
        </Button>
      </SettingRow>
    </Panel>
  );
}
