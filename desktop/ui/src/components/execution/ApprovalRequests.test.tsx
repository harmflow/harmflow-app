// @vitest-environment happy-dom
// Drives the approval requests of a running execution in a DOM, against a stand-in for the daemon's local API: each
// request read in full, and exactly the answer the owner chose sent back.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { HarmflowEvent } from "../../api/types";
import i18n from "../../i18n";
import { pendingApprovals } from "../../lib/approvals";
import { ApprovalRequests } from "./ApprovalRequests";

const daemon = vi.hoisted(() => ({ requests: [] as { method: string; path: string; body: unknown }[] }));
vi.mock("../../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      return { status: 202, body: { ok: true, message: "answer sent" } };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

let seq = 0;
function event(type: string, attempt: number, payload: Record<string, unknown>): HarmflowEvent {
  seq++;
  return {
    id: seq,
    schemaVersion: "1",
    timestamp: "2026-11-19T10:00:00Z",
    type,
    severity: "info",
    executionId: "exec-1",
    taskId: "t",
    attempt,
    harness: "codex",
    payload,
  } as unknown as HarmflowEvent;
}

const request = {
  requestId: "7",
  kind: "command",
  command: "curl https://example.com",
  cwd: "/w",
  reason: "needs the network",
  networkHost: "example.com",
};

describe("pendingApprovals", () => {
  it("lists a request until it is answered or withdrawn, and never one of an earlier attempt", () => {
    const asked = event("approval.requested", 1, request);
    expect(pendingApprovals([asked]).map((q) => q.requestId)).toEqual(["7"]);
    expect(pendingApprovals([asked, event("approval.answered", 1, { requestId: "7", decision: "accept" })])).toEqual([]);
    expect(pendingApprovals([asked, event("approval.withdrawn", 1, { requestId: "7" })])).toEqual([]);
    // A retry asks again: the earlier attempt's request waits no more.
    expect(pendingApprovals([asked, event("harness.started", 2, {})])).toEqual([]);
    // The same id in another attempt is another request.
    const again = event("approval.requested", 2, { ...request, command: "ls" });
    expect(pendingApprovals([asked, again]).map((q) => q.command)).toEqual(["ls"]);
  });
});

describe("ApprovalRequests", () => {
  let root: Root | undefined;
  let host: HTMLElement;

  beforeAll(async () => {
    await i18n.changeLanguage("en-US");
  });

  afterEach(() => {
    act(() => root?.unmount());
    root = undefined;
    host?.remove();
    daemon.requests.length = 0;
  });

  function render(events: HarmflowEvent[]) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<ApprovalRequests executionId="exec-1" requests={pendingApprovals(events)} />));
  }

  it("shows the request in full, with the three answers and no cancel", () => {
    render([event("approval.requested", 1, request)]);
    const text = host.textContent ?? "";
    for (const part of ["curl https://example.com", "/w", "needs the network", "example.com", "Waiting for your approval"]) {
      expect(text).toContain(part);
    }
    const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(buttons).toEqual(["Accept once", "Accept for this session", "Decline"]);
  });

  it.each([
    ["Accept once", "accept"],
    ["Accept for this session", "accept_for_session"],
    ["Decline", "decline"],
  ])("sends exactly %s as %s", async (label, decision) => {
    render([event("approval.requested", 1, request)]);
    const button = [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
    await act(async () => {
      button.click();
    });
    expect(daemon.requests).toEqual([
      { method: "POST", path: "/executions/exec-1/approvals/7", body: { decision } },
    ]);
  });

  it("shows nothing when no request waits", () => {
    render([]);
    expect(host.textContent).toBe("");
  });
});
