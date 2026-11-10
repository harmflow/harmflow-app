import { useTranslation } from "react-i18next";
import type { Execution } from "../../api/types";
import type { Tone } from "../../design/theme";
import { formatDuration, formatTime, elapsedMs } from "../../lib/format";
import type { IconName } from "../Icon";
import { Chip, EmptyState } from "../ui";

type CommandStatus = Execution["commands"][number]["status"];
/** A command the harness declined never ran, so it reads apart from a failure. */
const COMMAND_TONES: Record<CommandStatus, Tone> = { running: "cyan", succeeded: "success", failed: "danger", declined: "warning" };
const COMMAND_ICONS: Record<CommandStatus, IconName> = { running: "dot", succeeded: "check", failed: "x", declined: "alert" };

export function CommandList({ commands, locale }: { commands: Execution["commands"]; locale: string }) {
  const { t } = useTranslation();
  if (commands.length === 0) return <EmptyState icon="prompt" title={t("executions.detail.noCommands")} />;
  return (
    <div className="table__scroll">
      <table className="table">
        <thead>
          <tr>
            <th>{t("history.columns.status")}</th>
            <th>{t("executions.detail.tabs.commands")}</th>
            <th>{t("executions.detail.exitCode")}</th>
            <th>{t("executions.detail.started")}</th>
            <th>{t("executions.detail.duration")}</th>
          </tr>
        </thead>
        <tbody>
          {commands.map((c) => (
            <tr key={c.id}>
              <td>
                <Chip tone={COMMAND_TONES[c.status]} icon={COMMAND_ICONS[c.status]}>
                  {t(`executions.detail.commandStatus.${c.status}`)}
                </Chip>
              </td>
              <td className="mono cell-wrap">{c.command}</td>
              <td className="mono">{c.exitCode ?? "—"}</td>
              <td className="mono">{formatTime(c.startedAt, locale)}</td>
              <td className="mono">{c.startedAt ? formatDuration(elapsedMs(c.startedAt, c.endedAt), locale) : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
