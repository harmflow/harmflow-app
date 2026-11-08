// The window's question before it stops the daemon, and the stop it asks for. With executions running, the user waits
// for them — a draining stop, which the daemon ends once they have finished or its deadline has passed — or cancels
// them; with none, the daemon stops at once.
import type { useTranslation } from "react-i18next";
import { ApiError, api } from "../api/client";
import { native } from "../api/native";

type TFn = ReturnType<typeof useTranslation>["t"];

/** How the daemon was asked to stop: waiting for its executions, cancelling them, or with none running. */
export type StopChoice = "drain" | "force" | "stop";

/**
 * Asks whether to stop the daemon, with `running` executions, then asks the daemon for the stop chosen. Resolves to the
 * choice, or null when the user kept the daemon running; a refused request rejects.
 */
export async function askAndStopDaemon(translate: TFn, running: number): Promise<StopChoice | null> {
  // As in noticeText: the typed overloads, which walk every key of the resources, exceed TypeScript's instantiation
  // depth here, so the keys are checked by the locale tests instead.
  const t = translate as unknown as (key: string, options?: Record<string, unknown>) => string;
  let choice: StopChoice | null = null;
  if (running > 0) {
    const answer = await native.choose(
      t("settings.stopChoiceRunning", { count: running }),
      t("settings.stopConfirmTitle"),
      t("settings.stopWait", { count: running }),
      t("settings.stopCancelRunning", { count: running }),
      t("common.cancel"),
    );
    if (answer === "first") choice = "drain";
    else if (answer === "second") choice = "force";
  } else if (await native.confirm(t("settings.stopConfirm"), t("settings.stopConfirmTitle"), t("settings.stopDaemon"), t("common.cancel"))) {
    try {
      await api.stopDaemon(false);
      return "stop";
    } catch (err) {
      // The window's count was behind the daemon's: it refuses with the executions it runs, and the user chooses
      // between waiting for them and cancelling them as with a count that was current.
      const count = Number(err instanceof ApiError && err.code === "executions_running" ? err.params?.count : NaN);
      if (!(count > 0)) throw err;
      return askAndStopDaemon(translate, count);
    }
  }
  if (choice) await api.stopDaemon(choice === "force", choice === "drain");
  return choice;
}
