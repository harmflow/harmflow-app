// @vitest-environment happy-dom
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "../api/client";
import { native } from "../api/native";
import type { Health } from "../api/types";
import i18n from "../i18n";
import { Header, daemonBuildMismatch, restartDaemon } from "./Shell";
import { patchStateForTests } from "../state/store";

const health: Health = {
  status: "ok",
  version: "0.1.0",
  apiVersion: "v1",
  pid: 7,
  startedAt: "2026-10-20T19:00:00Z",
  acceptingFirings: true,
};

beforeAll(async () => {
  await i18n.changeLanguage("en-US");
});

afterEach(() => {
  vi.restoreAllMocks();
  patchStateForTests({
    appVersion: "0.1.0",
    appCommit: "",
    health: undefined,
    connection: "connected",
    lastUpdate: undefined,
  });
});

function header() {
  return renderToString(<Header screen="overview" onNewTask={() => {}} onNavigate={() => {}} onOpenExecution={() => {}} />);
}

describe("daemon build identity", () => {
  it("does not report equal commits, including a dirty suffix", () => {
    expect(daemonBuildMismatch("abc1234", "abc1234-dirty")).toBe(false);
    patchStateForTests({ appCommit: "abc1234", health: { ...health, commit: "abc1234-dirty" } });
    expect(header()).not.toContain("This dashboard was built from");
  });

  it("reports different known commits in a persistent banner", () => {
    expect(daemonBuildMismatch("abc1234", "def5678")).toBe(true);
    patchStateForTests({ appCommit: "abc1234", health: { ...health, commit: "def5678" } });
    const html = header();
    expect(html).toContain("This dashboard was built from abc1234, but the daemon was built from def5678.");
  });

  it("does not report a mismatch when either commit is empty", () => {
    expect(daemonBuildMismatch("", "def5678")).toBe(false);
    expect(daemonBuildMismatch("abc1234", "")).toBe(false);
    patchStateForTests({ appCommit: "", health: { ...health, commit: "def5678" } });
    expect(header()).not.toContain("This dashboard was built from");
    patchStateForTests({ appCommit: "abc1234", health: { ...health, commit: undefined } });
    expect(header()).not.toContain("This dashboard was built from");
  });
});

describe("daemon restart", () => {
  it("keeps the old daemon and does not start when executions cause a 409", async () => {
    const stop = vi.spyOn(api, "stopDaemon").mockRejectedValue(new ApiError(409, { error: { code: "executions_running", message: "executions are running" } }));
    const wait = vi.spyOn(native, "waitForDaemonStop").mockResolvedValue();
    const start = vi.spyOn(native, "startDaemon");

    await expect(restartDaemon()).rejects.toMatchObject({ status: 409 });
    expect(stop).toHaveBeenCalledWith(false);
    expect(wait).not.toHaveBeenCalled();
    expect(start).not.toHaveBeenCalled();
  });
});
