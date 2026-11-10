import { useState } from "react";
import { useTranslation } from "react-i18next";
import { native } from "../../api/native";
import { toast } from "../../components/toast";
import { Button, Chip, Panel, Toggle } from "../../components/ui";
import { askAndStopDaemon } from "../../lib/daemonStop";
import { errorMessage } from "../../lib/errorMessage";
import { formatDuration } from "../../lib/format";
import { useNow } from "../../lib/hooks";
import { actions, useStore } from "../../state/store";
import { SettingRow } from "./SettingRow";
import { usePatch } from "./usePatch";

export function DaemonSection() {
  const { t, i18n } = useTranslation();
  const settings = useStore((s) => s.settings);
  const health = useStore((s) => s.health);
  const running = useStore((s) => s.stats?.running ?? 0);
  const connection = useStore((s) => s.connection);
  const now = useNow(1000);
  const patch = usePatch();
  const [starting, setStarting] = useState(false);
  const daemonUp = connection === "connected";

  const setAutostart = async (enabled: boolean) => {
    const next = await patch({ autostart: enabled });
    // The tray process starts at login with the daemon, so notifications work with no window open.
    if (next) await native.setLoginItem(enabled && next.trayEnabled).catch(() => undefined);
  };

  const stopDaemon = async () => {
    try {
      const choice = await askAndStopDaemon(t, running);
      if (choice === "drain") {
        toast(t("settings.draining"), "warning");
        void actions.refreshHealth(); // the topbar shows the drain at once
      } else if (choice) toast(t("settings.stopped"), "warning");
    } catch (err) {
      toast((err as Error).message, "danger");
    }
  };

  const startDaemon = async () => {
    setStarting(true);
    try {
      await native.startDaemon();
      await actions.refreshAll();
    } catch (err) {
      toast(errorMessage(err), "danger");
    } finally {
      setStarting(false);
    }
  };

  return (
    <>
      <Panel title={t("settings.subheads.status")}>
        <SettingRow
          label={t("settings.runningNow")}
          hint={
            health ? (
              <span className="mono">
                {t("overview.pid", { pid: health.pid })} · {t("overview.uptime", { time: formatDuration(now - Date.parse(health.startedAt), i18n.language) })}
              </span>
            ) : undefined
          }
        >
          <Chip tone={daemonUp ? "success" : "danger"} icon={daemonUp ? "check" : "x"}>
            {daemonUp ? t("common.yes") : t("overview.notRunning")}
          </Chip>
          {daemonUp ? (
            <Button variant="danger-outline" icon="power" onClick={() => void stopDaemon()}>
              {t("settings.stopDaemon")}
            </Button>
          ) : native.available ? (
            <Button variant="primary" icon="power" disabled={starting} onClick={() => void startDaemon()}>
              {starting ? t("settings.starting") : t("settings.startDaemon")}
            </Button>
          ) : null}
        </SettingRow>
      </Panel>
      <Panel title={t("settings.subheads.startup")}>
        <SettingRow id="settings-autostart" label={t("settings.autostart")} hint={t("settings.autostartHint")}>
          <Toggle
            id="settings-autostart"
            checked={settings?.autostart.enabled ?? false}
            onChange={(v) => void setAutostart(v)}
            disabled={!settings}
          />
        </SettingRow>
      </Panel>
    </>
  );
}
