// First use: the scope of local operation, what was detected, autostart offered unchecked, a manual start of the
// daemon, a first task, then the live executions screen. The first task is created or imported on the Tasks screen,
// and first use comes back afterwards: to its last step once the task is saved, to the first-task step otherwise.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import { HarnessRow, ToolRow } from "../components/Harnesses";
import { TemplateButtons } from "../components/Templates";
import { Icon, Logo } from "../components/Icon";
import { toast } from "../components/toast";
import { Banner, Button, Checkbox, Chip, cx } from "../components/ui";
import { errorMessage } from "../lib/errorMessage";
import { actions, useStore } from "../state/store";

const TOTAL = 5;
/** The step that offers the first task, and the last one, whose button finishes onboarding. */
export const FIRST_TASK_STEP = 4;
export const LAST_STEP = TOTAL;

/** Where first use stands when it hands the first task over, so that it comes back with the autostart choice made. */
export type FirstUse = { step: number; autostart: boolean };

export function OnboardingScreen({
  onFinish,
  onCreateTask,
  onImport,
  resume,
}: {
  onFinish: () => void;
  onCreateTask: (template: string, at: FirstUse) => void;
  onImport: (at: FirstUse) => void;
  /** Where first use comes back to; from its first step when none. */
  resume?: FirstUse;
}) {
  const { t } = useTranslation();
  const [step, setStep] = useState(resume?.step ?? 1);
  const [autostart, setAutostart] = useState(resume?.autostart ?? false);
  const [starting, setStarting] = useState(false);
  const harnesses = useStore((s) => s.harnesses);
  const tools = useStore((s) => s.tools);
  const connection = useStore((s) => s.connection);
  const daemonUp = connection === "connected";

  const startDaemon = async () => {
    setStarting(true);
    try {
      await native.startDaemon();
      await actions.refreshAll();
    } catch (err) {
      toast(t("daemonDown.failed", { message: errorMessage(err) }), "danger");
    } finally {
      setStarting(false);
    }
  };

  // Only the last step's button records onboarding as completed: a first task picked or imported does not.
  const complete = async () => {
    try {
      if (daemonUp) {
        const settings = await api.updateSettings({ onboardingCompleted: true, ...(autostart ? { autostart: true } : {}) });
        if (autostart) await native.setLoginItem(settings.trayEnabled).catch(() => undefined);
        await actions.refreshSettings();
      }
    } catch (err) {
      toast((err as Error).message, "danger");
    }
    void native.ensureNotificationPermission();
    onFinish();
  };

  const startButton = !daemonUp ? (
    native.available ? (
      <Button variant="primary" icon="power" disabled={starting} onClick={() => void startDaemon()}>
        {starting ? t("daemonDown.starting") : t("onboarding.daemon.start")}
      </Button>
    ) : (
      <Banner tone="blue" icon="terminal">
        {t("daemonDown.browserHint")}
      </Banner>
    )
  ) : null;

  return (
    <div className="onboarding">
      <div className="onboarding__card">
        <header className="onboarding__header">
          <Logo size={44} />
          <div>
            <h1>{t("onboarding.title")}</h1>
            <span className="mono muted">{t("onboarding.step", { current: step, total: TOTAL })}</span>
          </div>
        </header>
        <div className="onboarding__progress" aria-hidden="true">
          {Array.from({ length: TOTAL }, (_, i) => (
            <span key={i} className={cx(i < step && "is-done")} />
          ))}
        </div>

        {step === 1 ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.scope.title")}</h2>
            <p>{t("onboarding.scope.body")}</p>
            <ul className="onboarding__points">
              {(["local", "permissions", "github", "api"] as const).map((k) => (
                <li key={k}>
                  <Icon name={k === "local" ? "home" : k === "permissions" ? "shield" : k === "github" ? "pr" : "key"} size={18} />
                  <span>{t(`onboarding.scope.points.${k}`)}</span>
                </li>
              ))}
            </ul>
          </section>
        ) : null}

        {step === 2 ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.detect.title")}</h2>
            <p>{t("onboarding.detect.body")}</p>
            {daemonUp ? (
              <>
                {harnesses.map((h) => (
                  <HarnessRow key={h.id} harness={h} />
                ))}
                {tools.map((tool) => (
                  <ToolRow key={tool.id} tool={tool} />
                ))}
              </>
            ) : (
              <>
                <p className="muted">{t("onboarding.daemon.notRunning")}</p>
                {startButton}
              </>
            )}
          </section>
        ) : null}

        {step === 3 ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.daemon.title")}</h2>
            <p>{t("onboarding.daemon.body")}</p>
            <div className="inline-actions">
              <Chip tone={daemonUp ? "success" : "danger"} icon={daemonUp ? "check" : "x"}>
                {daemonUp ? t("onboarding.daemon.running") : t("onboarding.daemon.notRunning")}
              </Chip>
              {startButton}
            </div>
            <Checkbox checked={autostart} onChange={setAutostart} label={t("onboarding.daemon.autostart")} />
            <p className="field__hint">{t("onboarding.daemon.autostartHint")}</p>
          </section>
        ) : null}

        {step === FIRST_TASK_STEP ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.firstTask.title")}</h2>
            <p>{t("onboarding.firstTask.body")}</p>
            <TemplateButtons disabled={!daemonUp} onPick={(id) => onCreateTask(id, { step, autostart })} />
            <Button icon="upload" disabled={!daemonUp} onClick={() => onImport({ step, autostart })}>
              {t("common.import")}
            </Button>
          </section>
        ) : null}

        {step === LAST_STEP ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.done.title")}</h2>
            <p>{t("onboarding.done.body")}</p>
          </section>
        ) : null}

        <footer className="onboarding__footer">
          {step > 1 ? (
            <Button variant="ghost" onClick={() => setStep(step - 1)}>
              {t("common.back")}
            </Button>
          ) : (
            <span />
          )}
          {step === FIRST_TASK_STEP ? (
            <Button variant="ghost" onClick={() => setStep(LAST_STEP)}>
              {t("onboarding.firstTask.skip")}
            </Button>
          ) : null}
          {step < TOTAL ? (
            step !== FIRST_TASK_STEP ? (
              <Button variant="primary" icon="arrowRight" onClick={() => setStep(step + 1)}>
                {t("common.next")}
              </Button>
            ) : null
          ) : (
            <Button variant="primary" icon="playFilled" onClick={() => void complete()}>
              {t("onboarding.done.go")}
            </Button>
          )}
        </footer>
      </div>
    </div>
  );
}

export function DaemonDownScreen() {
  const { t } = useTranslation();
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const start = async () => {
    setStarting(true);
    setError(null);
    try {
      await native.startDaemon();
      await actions.refreshAll();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setStarting(false);
    }
  };
  return (
    <div className="daemon-down">
      <div className="daemon-down__card">
        <Logo size={56} />
        <h1>{t("daemonDown.title")}</h1>
        <p>{t("daemonDown.body")}</p>
        {native.available ? (
          <Button variant="primary" size="lg" icon="power" disabled={starting} onClick={() => void start()}>
            {starting ? t("daemonDown.starting") : t("daemonDown.start")}
          </Button>
        ) : (
          <p className="mono muted">{t("daemonDown.browserHint")}</p>
        )}
        {error ? (
          <Banner tone="danger" icon="x">
            {t("daemonDown.failed", { message: error })}
          </Banner>
        ) : null}
      </div>
    </div>
  );
}
