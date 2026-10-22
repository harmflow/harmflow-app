// A ready harness whose version or sign-in detection could not establish is shown in a warning tone, with what is
// unknown (FS-004 FR-006, FR-008; harmflow-app#71).
import { renderToString } from "react-dom/server";
import { beforeAll, describe, expect, it } from "vitest";
import type { Harness } from "../api/types";
import i18n from "../i18n";
import { HarnessInventory, HarnessRow, readinessTone } from "./Harnesses";

const ready: Harness = {
  id: "claude-code",
  name: "Claude Code",
  detected: true,
  path: "/opt/homebrew/bin/claude",
  pathSource: "path",
  installations: ["/opt/homebrew/bin/claude"],
  version: "2.1.239",
  versionSupport: "supported",
  authentication: "valid",
  readiness: "ready",
  models: [{ id: "sonnet" }],
  efforts: ["default"],
  lastCheckedAt: "2026-10-22T09:00:00Z",
};

const signInUnknown: Harness = {
  ...ready,
  authentication: "unknown",
  diagnostic: "authentication could not be verified",
  diagnosticKey: "authentication_unverified",
};

const versionUnknown: Harness = { ...ready, version: undefined, versionSupport: "unknown" };

beforeAll(async () => {
  await i18n.changeLanguage("en-US");
});

describe("readinessTone", () => {
  it("is success only for a ready harness with nothing left unverified", () => {
    expect(readinessTone(ready)).toBe("success");
    expect(readinessTone(signInUnknown)).toBe("warning");
    expect(readinessTone(versionUnknown)).toBe("warning");
    expect(readinessTone({ ...ready, versionSupport: "untested_newer" })).toBe("warning");
  });
});

describe("HarnessRow", () => {
  it("shows a ready harness with an unknown sign-in in a warning tone, its diagnostic on the chip", () => {
    const html = renderToString(<HarnessRow harness={signInUnknown} />);
    expect(html).toContain("chip--warning");
    expect(html).toContain('title="authentication could not be verified"');
  });
});

describe("HarnessInventory", () => {
  it("names a version support it could not establish, with no version to show beside it", () => {
    const html = renderToString(<HarnessInventory harness={versionUnknown} locale="en-US" />);
    expect(html).toContain(i18n.t("harness.versionSupport.unknown"));
  });

  it("shows an unknown sign-in in a warning tone", () => {
    const html = renderToString(<HarnessInventory harness={signInUnknown} locale="en-US" />);
    expect(html).not.toContain("chip--success");
    expect(html).toContain(`<span class="chip chip--warning">${i18n.t("harness.authentication.unknown")}</span>`);
  });
});
