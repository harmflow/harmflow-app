import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { native } from "./api/native";
import { Header, Sidebar, noticeText, type Screen } from "./components/Shell";
import { Toasts } from "./components/toast";
import { applyLanguage } from "./i18n";
import { ExecutionsScreen } from "./screens/ExecutionsScreen";
import { HistoryScreen } from "./screens/HistoryScreen";
import { DaemonDownScreen, OnboardingScreen } from "./screens/OnboardingScreen";
import { OverviewScreen } from "./screens/OverviewScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { ImportDialog, TasksScreen, TemplatePicker, type TaskView } from "./screens/TasksScreen";
import { setNotificationHandler, startSync, useStore } from "./state/store";

const ONBOARDED_KEY = "harmflow.onboarded";

function wasOnboarded(): boolean {
  try {
    return localStorage.getItem(ONBOARDED_KEY) === "1";
  } catch {
    return false;
  }
}

export function App() {
  const { t } = useTranslation();
  const [screen, setScreen] = useState<Screen>("executions");
  const [selectedExecution, setSelectedExecution] = useState<string | null>(null);
  const [historyOpen, setHistoryOpen] = useState<string | null>(null);
  const [taskView, setTaskView] = useState<TaskView>({ kind: "list" });
  const [picker, setPicker] = useState(false);
  const [importing, setImporting] = useState(false);
  const [onboardingDismissed, setOnboardingDismissed] = useState(false);

  const settings = useStore((s) => s.settings);
  const daemonRunning = useStore((s) => s.daemonRunning);
  const navigation = useStore((s) => s.navigation);

  useEffect(() => {
    startSync();
    // macOS asks once; later launches answer at once (FS-011 FR-003 — notices respect the OS's permission).
    void native.ensureNotificationPermission();
  }, []);

  useEffect(() => {
    if (settings) applyLanguage(settings.language);
    if (settings?.onboardingCompleted) {
      try {
        localStorage.setItem(ONBOARDED_KEY, "1");
      } catch {
        // Only a hint for the first paint.
      }
    }
  }, [settings?.language, settings?.onboardingCompleted]);

  // The tray speaks the interface's language too.
  useEffect(() => {
    void native.setTrayLabels({
      idle: t("tray.idle"),
      active: t("tray.active", { count: "{{count}}" as unknown as number }),
      open: t("tray.open"),
      quit: t("tray.quit"),
      stop: t("tray.stop"),
      stopTitle: t("settings.stopConfirmTitle"),
      stopConfirm: t("settings.stopConfirm"),
      stopConfirmRunning: t("settings.stopConfirmRunning", { count: "{{count}}" as unknown as number }),
      stopOk: t("settings.stopDaemon"),
      cancel: t("common.cancel"),
    });
  }, [t]);

  useEffect(() => {
    setNotificationHandler((n) => {
      const text = noticeText(t, n);
      void native.notify(text.title, text.body);
    });
  }, [t]);

  // A target from the tray or a notice: show the window's matching screen.
  useEffect(() => {
    if (!navigation) return;
    if (navigation.executionId) {
      setScreen("executions");
      setSelectedExecution(navigation.executionId);
    } else if (navigation.screen) {
      setScreen(navigation.screen as Screen);
    }
  }, [navigation]);

  const openExecution = (id: string) => {
    setScreen("executions");
    setSelectedExecution(id);
  };

  const navigate = (s: Screen) => {
    setScreen(s);
    if (s === "tasks") setTaskView({ kind: "list" });
  };

  const onboarding =
    !onboardingDismissed && ((settings && !settings.onboardingCompleted) || (daemonRunning === false && !wasOnboarded()));
  const daemonDown = !onboarding && native.available && daemonRunning === false;

  if (onboarding) {
    return (
      <>
        <OnboardingScreen
          onFinish={() => {
            setOnboardingDismissed(true);
            setScreen("executions");
          }}
          onCreateTask={(k) => {
            setOnboardingDismissed(true);
            setScreen("tasks");
            setTaskView({ kind: "new", template: k });
          }}
          onImport={() => {
            setOnboardingDismissed(true);
            setScreen("tasks");
            setImporting(true);
          }}
        />
        <Toasts />
      </>
    );
  }

  if (daemonDown) {
    return (
      <>
        <DaemonDownScreen />
        <Toasts />
      </>
    );
  }

  return (
    <div className={native.available ? "app app--tauri" : "app"}>
      <div className="app__backdrop" aria-hidden="true" />
      <Sidebar screen={screen} onNavigate={navigate} />
      <main className="app__content">
        <Header screen={screen} onNewTask={() => setPicker(true)} onNavigate={navigate} onOpenExecution={openExecution} />
        {screen === "executions" ? (
          <ExecutionsScreen
            selectedId={selectedExecution}
            onSelect={setSelectedExecution}
            onGoToTasks={() => navigate("tasks")}
            onSeeHistory={() => navigate("history")}
          />
        ) : null}
        {screen === "overview" ? (
          <OverviewScreen
            onOpenExecution={openExecution}
            onNewTask={() => setPicker(true)}
            onOpenTask={(id) => {
              setScreen("tasks");
              setTaskView({ kind: "edit", id });
            }}
          />
        ) : null}
        {screen === "tasks" ? <TasksScreen view={taskView} onView={setTaskView} onOpenExecution={openExecution} /> : null}
        {screen === "history" ? <HistoryScreen openId={historyOpen} onOpen={setHistoryOpen} /> : null}
        {screen === "settings" ? <SettingsScreen /> : null}
      </main>
      {picker ? (
        <TemplatePicker
          onClose={() => setPicker(false)}
          onPick={(k) => {
            setPicker(false);
            setScreen("tasks");
            setTaskView({ kind: "new", template: k });
          }}
        />
      ) : null}
      {importing ? (
        <ImportDialog
          onClose={() => setImporting(false)}
          onImported={(task) => {
            setImporting(false);
            setTaskView({ kind: "edit", id: task.id });
          }}
        />
      ) : null}
      <Toasts />
    </div>
  );
}
