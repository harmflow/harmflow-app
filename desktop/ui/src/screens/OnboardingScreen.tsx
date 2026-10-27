// First use: the scope of local operation, what was detected, autostart offered unchecked, a manual start of the
// daemon, a first task, then the live executions screen.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import { HarnessRow, ToolRow } from "../components/Harnesses";
import { Icon, Logo } from "../components/Icon";
import { toast } from "../components/toast";
import { Banner, Button, Checkbox, Chip, cx } from "../components/ui";
import type { TemplateKind } from "../lib/taskForm";
import { actions, useStore } from "../state/store";

const TOTAL = 5;

export function OnboardingScreen({
  onFinish,
  onCreateTask,
  onImport,
}: {
  onFinish: () => void;
  onCreateTask: (k: TemplateKind) => void;
  onImport: () => void;
}) {
  const { t } = useTranslation();
  const [step, setStep] = useState(1);
  const [autostart, setAutostart] = useState(false);
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
      toast(t("daemonDown.failed", { message: (err as Error).message }), "danger");
    } finally {
      setStarting(false);
    }
  };

  const complete = async (then?: () => void) => {
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
    (then ?? onFinish)();
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

        {step === 4 ? (
          <section className="onboarding__step">
            <h2>{t("onboarding.firstTask.title")}</h2>
            <p>{t("onboarding.firstTask.body")}</p>
            <div className="templates">
              <button type="button" className="template" disabled={!daemonUp} onClick={() => void complete(() => onCreateTask("github"))}>
                <span className="template__icon">
                  <Icon name="code" size={24} />
                </span>
                <strong>{t("tasks.templates.github")}</strong>
                <span className="muted">{t("tasks.templates.githubHint")}</span>
              </button>
              <button type="button" className="template" disabled={!daemonUp} onClick={() => void complete(() => onCreateTask("general"))}>
                <span className="template__icon template__icon--violet">
                  <Icon name="sparkle" size={24} />
                </span>
                <strong>{t("tasks.templates.general")}</strong>
                <span className="muted">{t("tasks.templates.generalHint")}</span>
              </button>
            </div>
            <Button icon="upload" disabled={!daemonUp} onClick={() => void complete(onImport)}>
              {t("common.import")}
            </Button>
          </section>
        ) : null}

        {step === 5 ? (
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
          {step === 4 ? (
            <Button variant="ghost" onClick={() => setStep(5)}>
              {t("onboarding.firstTask.skip")}
            </Button>
          ) : null}
          {step < TOTAL ? (
            step !== 4 ? (
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
      setError((err as Error).message);
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
