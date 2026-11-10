import { useTranslation } from "react-i18next";
import { api } from "../../api/client";
import type { SettingsPatch } from "../../api/types";
import { toast } from "../../components/toast";
import { actions } from "../../state/store";

/** Saves a change to the settings and reads them again; a toast says so, unless quiet. Undefined when it failed. */
export function usePatch() {
  const { t } = useTranslation();
  return async (p: SettingsPatch, quiet = false) => {
    try {
      const next = await api.updateSettings(p);
      await actions.refreshSettings();
      if (!quiet) toast(t("settings.saved"));
      return next;
    } catch (err) {
      toast((err as Error).message, "danger");
      return undefined;
    }
  };
}
