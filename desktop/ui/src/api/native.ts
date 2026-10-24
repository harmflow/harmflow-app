// Desktop capabilities of the Tauri shell. In a plain browser each one degrades to what a web page can do.
import { isTauri } from "./transport";

export interface DaemonStatus {
  /** The runtime file exists and GET /health answered. */
  running: boolean;
  address?: string;
  pid?: number;
  version?: string;
  commit?: string;
  builtAt?: string;
  startedAt?: string;
  error?: string;
}

async function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const core = await import("@tauri-apps/api/core");
  return core.invoke<T>(cmd, args);
}

export const native = {
  available: isTauri(),

  async appVersion(): Promise<string> {
    if (!isTauri()) return __APP_VERSION__;
    const { getVersion } = await import("@tauri-apps/api/app");
    return getVersion();
  },

  async daemonStatus(): Promise<DaemonStatus | null> {
    if (!isTauri()) return null;
    return invoke<DaemonStatus>("daemon_status");
  },

  async startDaemon(): Promise<DaemonStatus> {
    return invoke<DaemonStatus>("daemon_start");
  },

  /** Waits for a requested stop to finish before daemon_start checks whether it needs to spawn a process. */
  async waitForDaemonStop(): Promise<void> {
    if (!isTauri()) return;
    for (let attempt = 0; attempt < 40; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 250));
      const status = await invoke<DaemonStatus>("daemon_status");
      if (!status.running) return;
    }
    throw new Error("the daemon did not stop within 10 seconds");
  },

  async setTrayEnabled(enabled: boolean): Promise<void> {
    if (isTauri()) await invoke("set_tray_enabled", { enabled });
  },

  /** Tray texts in the interface's language; the tray itself follows the daemon's events on its own. */
  async setTrayLabels(labels: Record<string, string>): Promise<void> {
    if (isTauri()) await invoke("set_tray_labels", { labels });
  },

  /** The first render committed. The shell decides whether that shows the window: a login start stays hidden. */
  async interfaceReady(): Promise<void> {
    if (isTauri()) await invoke("interface_ready");
  },

  async openPath(path: string): Promise<void> {
    if (isTauri()) await invoke("open_path", { path });
  },

  async openUrl(url: string): Promise<void> {
    if (isTauri()) await invoke("open_url", { url });
    else window.open(url, "_blank", "noopener");
  },

  /** Asks where to save, then writes the file. Returns false when the user cancelled. */
  async saveTextFile(defaultName: string, content: string): Promise<boolean> {
    if (!isTauri()) {
      const url = URL.createObjectURL(new Blob([content], { type: "application/yaml" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = defaultName;
      a.click();
      URL.revokeObjectURL(url);
      return true;
    }
    const { save } = await import("@tauri-apps/plugin-dialog");
    const path = await save({ defaultPath: defaultName, filters: [{ name: "YAML", extensions: ["yaml", "yml"] }] });
    if (!path) return false;
    await invoke("write_text_file", { path, content });
    return true;
  },

  async confirm(message: string, title: string, okLabel: string, cancelLabel: string): Promise<boolean> {
    if (!isTauri()) return window.confirm(message);
    const { confirm } = await import("@tauri-apps/plugin-dialog");
    return confirm(message, { title, kind: "warning", okLabel, cancelLabel });
  },

  async setLoginItem(enabled: boolean): Promise<void> {
    if (!isTauri()) return;
    const autostart = await import("@tauri-apps/plugin-autostart");
    if (enabled) await autostart.enable();
    else await autostart.disable();
  },

  async ensureNotificationPermission(): Promise<boolean> {
    if (!isTauri()) return false;
    const n = await import("@tauri-apps/plugin-notification");
    if (await n.isPermissionGranted()) return true;
    return (await n.requestPermission()) === "granted";
  },

  /** A click on the notice opens the execution it names; the shell handles the click (FS-011 FR-003). */
  async notify(title: string, body: string, executionId?: string): Promise<void> {
    if (!isTauri()) return;
    const n = await import("@tauri-apps/plugin-notification");
    if (await n.isPermissionGranted()) n.sendNotification({ title, body, extra: executionId ? { executionId } : undefined });
  },

  async onNavigate(handler: (target: { executionId?: string; screen?: string }) => void): Promise<() => void> {
    if (!isTauri()) return () => {};
    const { listen } = await import("@tauri-apps/api/event");
    return listen<{ executionId?: string; screen?: string }>("harmflow://navigate", (e) => handler(e.payload));
  },
};
