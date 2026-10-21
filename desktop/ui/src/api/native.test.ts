// The notices name their execution where the shell looks for it on a click (desktop/src-tauri/src/notice.rs).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { native } from "./native";

const plugin = vi.hoisted(() => ({ isPermissionGranted: vi.fn(async () => true), sendNotification: vi.fn() }));
vi.mock("@tauri-apps/plugin-notification", () => plugin);

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
