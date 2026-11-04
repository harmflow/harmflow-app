import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { native } from "./api/native";
import { RootBoundary, ScreenBoundary } from "./components/ErrorBoundary";
import { Header, NAV, Sidebar, noticeText, type ScreenRoute } from "./components/Shell";
import type { LeaveCheck } from "./components/TaskEditor";
import { Toasts } from "./components/toast";
import { applyLanguage } from "./i18n";
import { matches, matching } from "./lib/shortcuts";
import { storeZoom, storedZoom, zoomKey, zoomed } from "./lib/zoom";
import { ExecutionsScreen } from "./screens/ExecutionsScreen";
import { HistoryScreen } from "./screens/HistoryScreen";
import { ImportDialog } from "./screens/ImportDialog";
import { DaemonDownScreen, LAST_STEP, OnboardingScreen, type FirstUse } from "./screens/OnboardingScreen";
import { OverviewScreen } from "./screens/OverviewScreen";
import { NOTHING_REGISTERED, RegisteredProvider, dynamicText, useRegistered, type Registered } from "./registrations";
import { SettingsScreen, isSettingsSection, type SettingsSectionKey } from "./screens/SettingsScreen";
import { TemplatePicker } from "./components/Templates";
import { TasksScreen, type TaskView } from "./screens/TasksScreen";
import { actions, setNotificationHandler, startSync, useStore } from "./state/store";

const ONBOARDED_KEY = "harmflow.onboarded";
const SIDEBAR_KEY = "harmflow.sidebarCollapsed";
const EXECUTIONS_ASIDE_KEY = "harmflow.executionsAsideHidden";

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

/** Whether the Executions screen's aside was hidden; a missing or unreadable value shows it. */
function wasExecutionsAsideHidden(): boolean {
  try {
    return localStorage.getItem(EXECUTIONS_ASIDE_KEY) === "1";
  } catch {
    return false;
  }
}

/**
 * What the entry file, `dashboard.tsx`, renders: the interface inside the boundary that keeps the root from ending up
 * empty, with what the registrations add, checked; nothing without them.
 */
export function Root({ registered = NOTHING_REGISTERED }: { registered?: Registered } = {}) {
  return (
    <RootBoundary>
      <RegisteredProvider value={registered}>
        <App />
      </RegisteredProvider>
    </RootBoundary>
  );
}

export function App() {
  const { t, i18n } = useTranslation();
  const registered = useRegistered();
  const [screen, setScreen] = useState<ScreenRoute>("executions");
  // Settings opens on General the first time after a launch, and later on the section last open; never kept longer.
  const [settingsSection, setSettingsSection] = useState<SettingsSectionKey>("general");
  const [selectedExecution, setSelectedExecution] = useState<string | null>(null);
  const [historyOpen, setHistoryOpen] = useState<string | null>(null);
  const [taskView, setTaskView] = useState<TaskView>({ kind: "list" });
  const [picker, setPicker] = useState(false);
  const [importing, setImporting] = useState(false);
  const [onboardingDismissed, setOnboardingDismissed] = useState(false);
  // First use hands its first task to the Tasks screen, in the task's editor or the import dialog: `firstTask` says the
  // hand-over is on, and `firstUse` where first use comes back to, with the autostart choice it had.
  const [firstTask, setFirstTask] = useState(false);
  const [firstUse, setFirstUse] = useState<FirstUse | undefined>(undefined);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(wasSidebarCollapsed);
  const [executionsAsideHidden, setExecutionsAsideHidden] = useState(wasExecutionsAsideHidden);
  const [zoom, setZoom] = useState(storedZoom);
  const zoomApplied = useRef<Promise<void>>(Promise.resolve());

  const settings = useStore((s) => s.settings);
  const daemonRunning = useStore((s) => s.daemonRunning);
  const restarting = useStore((s) => s.daemonRestart === "restarting");
  const navigation = useStore((s) => s.navigation);

  useEffect(() => {
    zoomApplied.current = native.setZoom(zoom).catch((error) => console.error("HarmFlow: zoom not applied", error));
    storeZoom(zoom);
  }, [zoom]);

  // The first render committed and the kept zoom applied: the shell may show the window now (harmflow-app#96), at its
  // zoom rather than jumping to it. Two commands may reach the shell in either order, so this one waits for the
  // zoom, failed or not. An effect rather than an animation frame, which WebKit may throttle for a hidden page; the
  // shell acts on the first signal alone.
  useEffect(() => void zoomApplied.current.then(() => native.interfaceReady()), []);

  useEffect(() => {
    startSync();
    // macOS asks once; later launches answer at once (notices respect the OS's permission).
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

  useEffect(() => {
    try {
      localStorage.setItem(EXECUTIONS_ASIDE_KEY, executionsAsideHidden ? "1" : "0");
    } catch {
      // Without storage the choice lasts until the app quits.
    }
  }, [executionsAsideHidden]);

  // The tray, the app menu's Settings…, and the Help menu's Keyboard Shortcuts speak the interface's language too. A count given as a string reads the key
  // as written, so both forms of the stop confirmation go with their `{{count}}` left in, for the tray to choose.
  useEffect(() => {
    void native.setTrayLabels({
      idle: t("tray.idle"),
      active: t("tray.active", { count: "{{count}}" as unknown as number }),
      open: t("tray.open"),
      quit: t("tray.quit"),
      stop: t("tray.stop"),
      stopTitle: t("settings.stopConfirmTitle"),
      stopConfirm: t("settings.stopConfirm"),
      stopConfirmRunning_one: t("settings.stopConfirmRunning_one", { count: "{{count}}" as unknown as number }),
      stopConfirmRunning_other: t("settings.stopConfirmRunning_other", { count: "{{count}}" as unknown as number }),
      stopOk: t("settings.stopDaemon"),
      stopFailedTitle: t("settings.stopFailedTitle"),
      stopFailedOk: t("settings.stopFailedOk"),
      cancel: t("common.cancel"),
      settings: t("menu.settings"),
      shortcuts: t("menu.shortcuts"),
    });
  }, [t]);

  useEffect(() => {
    setNotificationHandler((n) => {
      const text = noticeText(t, n);
      void native.notify(text.title, text.body, n.executionId);
    });
  }, [t]);

  // The open task editor's question while it holds unsaved changes. Every way out of the editor that the shell owns
  // goes through `leave`: at once with no question held, after a confirmed Discard with one.
  const leaveCheck = useRef<LeaveCheck | null>(null);
  const holdLeaveCheck = useCallback((check: LeaveCheck | null) => {
    leaveCheck.current = check;
  }, []);
  const leave = (go: () => void) => {
    const check = leaveCheck.current;
    if (!check) return go();
    void check().then((discard) => discard && go());
  };
  const picks = useRef(0);

  const openExecution = (id: string) =>
    leave(() => {
      setScreen("executions");
      setSelectedExecution(id);
    });

  // Settings, on the section asked for, or the one last open. Opening what is already open changes nothing, so it asks
  // nothing either.
  const openSettings = (section: SettingsSectionKey = settingsSection) => {
    if (screen === "settings" && section === settingsSection) return;
    leave(() => {
      setScreen("settings");
      setSettingsSection(section);
    });
  };

  const navigate = (s: ScreenRoute) => {
    if (s === "settings") return openSettings();
    leave(() => {
      setScreen(s);
      if (s === "tasks") setTaskView({ kind: "list" });
    });
  };
  // The keydown handler below is added again only when the shell or the screen changes, so it reads the navigation of
  // the latest render: the Settings section last open changes with no new handler.
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;

  // The shell shows first use's first task while its editor or the import dialog is open. Leaving them without saving,
  // by any way out, brings first use back on its first-task step; saving brings it back on its last step, whose button
  // alone finishes onboarding and opens the Executions screen.
  const firstTaskOpen = firstTask && screen === "tasks" && (taskView.kind === "new" || importing);
  const firstTaskSaved = () => {
    setFirstTask(false);
    setFirstUse((at) => ({ step: LAST_STEP, autostart: at?.autostart ?? false }));
  };
  const onboarding =
    !onboardingDismissed &&
    !firstTaskOpen &&
    ((settings && !settings.onboardingCompleted) || (daemonRunning === false && !wasOnboarded()));
  // A daemon being restarted is down for a moment: the shell stays, with the topbar saying so.
  const daemonDown = !onboarding && native.available && daemonRunning === false && !restarting;
  const shell = !onboarding && !daemonDown;
  const RegisteredScreen = registered.screens.find((s) => s.route === screen)?.component;

  // A target from the tray or a notice shows its execution. A screen from the app menu (⌘,), or the Help menu's
  // Keyboard Shortcuts with its Settings section, acts where ⌘N does: in
  // the shell, with no modal dialog open. A native menu shortcut never reaches the keydown handler below, which keeps
  // ⌘B and ⌘N off the other screens, so the check is here. Taken or ignored, the target is dropped: one ignored on the
  // onboarding screen never changes the screen once the shell returns.
  useEffect(() => {
    if (!navigation) return;
    actions.clearNavigation();
    const { executionId, screen: target, section } = navigation;
    if (executionId) {
      leave(() => {
        setScreen("executions");
        setSelectedExecution(executionId);
      });
    } else if (target && shell && !document.querySelector('[aria-modal="true"]')) {
      const known = isSettingsSection(section) || registered.settingsSections.some((s) => s.key === section);
      if (target === "settings") openSettings(known ? section : undefined);
      else leave(() => setScreen(target));
    }
  }, [navigation]);

  // ⌘B collapses and expands the sidebar, ⌘N opens the template picker as the topbar's New task does, ⌘E hides and
  // shows the Executions screen's aside, and ⌘1 to ⌘5 open the sidebar's items in its order, as a click on each does,
  // from any focus, a text field or the YAML editor included. ⌘E acts on the Executions screen alone: elsewhere it is
  // left to the system and the saved state stays as it was. The onboarding and daemon-down screens have no shell, and a
  // modal dialog, the picker included, keeps the keyboard. The keys are the shortcut table's.
  useEffect(() => {
    if (!shell) return;
    const onKey = (e: KeyboardEvent) => {
      const item = NAV.find((nav) => matches(e, nav.shortcut));
      const key = matching(e, screen === "executions" ? ["toggleSidebar", "newTask", "toggleExecutionsAside"] : ["toggleSidebar", "newTask"]);
      if (!key && !item) return;
      if (document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      if (item) navigateRef.current(item.key);
      else if (key === "toggleSidebar") setSidebarCollapsed((collapsed) => !collapsed);
      else if (key === "toggleExecutionsAside") setExecutionsAsideHidden((hidden) => !hidden);
      else setPicker(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [shell, screen]);

  // ⌘+, ⌘−, and ⌘0 zoom the dashboard everywhere, from any focus: unlike ⌘B, on the onboarding and daemon-down
  // screens too, and over an open dialog.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const key = zoomKey(e);
      if (!key) return;
      e.preventDefault();
      setZoom((level) => zoomed(level, key));
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (onboarding) {
    return (
      <>
        <OnboardingScreen
          resume={firstUse}
          onFinish={() => {
            setOnboardingDismissed(true);
            setFirstTask(false);
            setScreen("executions");
          }}
          onCreateTask={(k, at) => {
            setFirstUse(at);
            setFirstTask(true);
            setScreen("tasks");
            setTaskView({ kind: "new", template: k });
          }}
          onImport={(at) => {
            setFirstUse(at);
            setFirstTask(true);
            setScreen("tasks");
            setTaskView({ kind: "list" });
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
        <Header
          screen={screen}
          crumb={screen === "settings" ? dynamicText(t, `settings.sections.${settingsSection}`).toLocaleLowerCase(i18n.language) : undefined}
          onNewTask={() => setPicker(true)}
          onNavigate={navigate}
          onOpenExecution={openExecution}
        />
        <ScreenBoundary key={screen}>
          {screen === "executions" ? (
            <ExecutionsScreen
              selectedId={selectedExecution}
              onSelect={setSelectedExecution}
              asideHidden={executionsAsideHidden}
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
          {screen === "tasks" ? (
            <TasksScreen
              view={taskView}
              onView={setTaskView}
              onOpenExecution={openExecution}
              onLeaveCheck={holdLeaveCheck}
              onCreated={
                firstTaskOpen
                  ? () => {
                      setTaskView({ kind: "list" });
                      firstTaskSaved();
                    }
                  : undefined
              }
            />
          ) : null}
          {screen === "history" ? <HistoryScreen openId={historyOpen} onOpen={setHistoryOpen} /> : null}
          {screen === "settings" ? <SettingsScreen section={settingsSection} onSection={openSettings} onLeaveCheck={holdLeaveCheck} /> : null}
          {RegisteredScreen ? <RegisteredScreen /> : null}
        </ScreenBoundary>
      </main>
      {picker ? (
        <TemplatePicker
          onClose={() => setPicker(false)}
          onPick={(k) => {
            setPicker(false);
            leave(() => {
              setScreen("tasks");
              setTaskView({ kind: "new", template: k, pick: ++picks.current });
            });
          }}
        />
      ) : null}
      {importing ? (
        <ImportDialog
          onClose={() => setImporting(false)}
          onImported={(task) => {
            setImporting(false);
            if (firstTaskOpen) firstTaskSaved();
            else setTaskView({ kind: "edit", id: task.id });
          }}
        />
      ) : null}
      <Toasts />
    </div>
  );
}
