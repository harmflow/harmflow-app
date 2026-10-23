import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { native } from "./api/native";
import { RootBoundary, ScreenBoundary } from "./components/ErrorBoundary";
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
const SIDEBAR_KEY = "harmflow.sidebarCollapsed";

function wasOnboarded(): boolean {
  try {
    return localStorage.getItem(ONBOARDED_KEY) === "1";
  } catch {
    return false;
  }
}

function wasSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_KEY) === "1";
  } catch {
    return false;
  }
}

/** What `main.tsx` renders: the interface inside the boundary that keeps the root from ending up empty. */
export function Root() {
  return (
    <RootBoundary>
      <App />
    </RootBoundary>
  );
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
  const [sidebarCollapsed, setSidebarCollapsed] = useState(wasSidebarCollapsed);

  const settings = useStore((s) => s.settings);
  const daemonRunning = useStore((s) => s.daemonRunning);
  const navigation = useStore((s) => s.navigation);

  // The first render committed: the shell may show the window now (harmflow-app#96). An effect rather than an
  // animation frame, which WebKit may throttle for a hidden page; the shell acts on the first signal alone.
  useEffect(() => void native.interfaceReady(), []);

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

  useEffect(() => {
    try {
      localStorage.setItem(SIDEBAR_KEY, sidebarCollapsed ? "1" : "0");
    } catch {
      // Without storage the choice lasts until the app quits.
    }
  }, [sidebarCollapsed]);

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
      void native.notify(text.title, text.body, n.executionId);
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
  const shell = !onboarding && !daemonDown;

  // ⌘B collapses and expands the sidebar, and ⌘N opens the template picker as the topbar's New task does (D-120), from
  // any focus, a text field or the YAML editor included. The onboarding and daemon-down screens have no shell, and a
  // modal dialog, the picker included, keeps the keyboard. Linux and Windows would check ctrlKey.
  useEffect(() => {
    if (!shell) return;
    const onKey = (e: KeyboardEvent) => {
      if (!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || e.repeat) return;
      const key = e.key.toLowerCase();
      if (key !== "b" && key !== "n") return;
      if (document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      if (key === "b") setSidebarCollapsed((collapsed) => !collapsed);
      else setPicker(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [shell]);

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
    <div className={native.available ? "app app--tauri" : "app"} data-sidebar={sidebarCollapsed ? "collapsed" : "expanded"}>
      <div className="app__backdrop" aria-hidden="true" />
      <Sidebar screen={screen} onNavigate={navigate} collapsed={sidebarCollapsed} onToggle={() => setSidebarCollapsed(!sidebarCollapsed)} />
      <main className="app__content">
        <Header screen={screen} onNewTask={() => setPicker(true)} onNavigate={navigate} onOpenExecution={openExecution} />
        <ScreenBoundary key={screen}>
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
        </ScreenBoundary>
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
