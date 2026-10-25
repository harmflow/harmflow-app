// The notices name their execution where the shell looks for it on a click (desktop/src-tauri/src/notice.rs), and the
// zoom reaches the web view as its scale.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "./native";

const plugin = vi.hoisted(() => ({ isPermissionGranted: vi.fn(async () => true), sendNotification: vi.fn() }));
vi.mock("@tauri-apps/plugin-notification", () => plugin);

const webview = vi.hoisted(() => ({ setZoom: vi.fn(async (_scale: number) => {}) }));
vi.mock("@tauri-apps/api/webview", () => ({ getCurrentWebview: () => webview }));

describe("native.setZoom", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    webview.setZoom.mockClear();
  });

  it("sets the web view's scale from the percent", async () => {
    vi.stubGlobal("window", { __TAURI_INTERNALS__: {} });
    await native.setZoom(125);
    expect(webview.setZoom).toHaveBeenCalledWith(1.25);
    await native.setZoom(80);
    expect(webview.setZoom).toHaveBeenLastCalledWith(0.8);
  });

  it("leaves a plain browser's zoom alone", async () => {
    vi.stubGlobal("window", {});
    await native.setZoom(150);
    expect(webview.setZoom).not.toHaveBeenCalled();
  });
});

describe("native.notify", () => {
  beforeEach(() => vi.stubGlobal("window", { __TAURI_INTERNALS__: {} }));
  afterEach(() => {
    vi.unstubAllGlobals();
    plugin.sendNotification.mockClear();
  });

  it("names the execution the notice concerns", async () => {
    await native.notify("Failed", "Implement GitHub issues", "exec-8f3a2c");
    expect(plugin.sendNotification).toHaveBeenCalledWith({
      title: "Failed",
      body: "Implement GitHub issues",
      extra: { executionId: "exec-8f3a2c" },
    });
  });

  it("sends a notice about no execution without one", async () => {
    await native.notify("Daemon", "Started");
    expect(plugin.sendNotification).toHaveBeenCalledWith({ title: "Daemon", body: "Started", extra: undefined });
  });

  it("sends nothing without the permission", async () => {
    plugin.isPermissionGranted.mockResolvedValueOnce(false);
    await native.notify("Failed", "Implement GitHub issues", "exec-8f3a2c");
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
});
