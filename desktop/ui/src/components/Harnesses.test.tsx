// The installed-agents inventory shows every field a check found (FS-004 FR-007; harmflow-app#68). A ready harness
// whose version or sign-in detection could not establish is shown in a warning tone, with what is unknown (FS-004
// FR-006, FR-008; harmflow-app#71).
import { renderToString } from "react-dom/server";
import { beforeAll, describe, expect, it, vi } from "vitest";
import type { Harness } from "../api/types";
import i18n from "../i18n";
import { formatRelative } from "../lib/format";
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

// The inventory's rows, label to text, as rendered.
function rows(html: string): Record<string, string> {
  const text = (s: string) => s.replace(/<[^>]+>/g, "").replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, "&");
  return Object.fromEntries([...html.matchAll(/<dt>(.*?)<\/dt><dd>(.*?)<\/dd>/g)].map(([, k, v]) => [text(k), text(v)]));
}

describe("HarnessInventory", () => {
  // What a check of a detected, signed-in Claude Code found, below the minimum version, so the check left a diagnostic.
  const checked: Harness = {
    ...ready,
    version: "2.1.200",
    versionSupport: "below_minimum",
    readiness: "not_ready",
    models: [{ id: "default" }, { id: "opus" }, { id: "sonnet" }],
    efforts: ["default", "low", "high"],
    diagnostic: "this Claude Code version lacks --permission-mode, which HarmFlow needs",
    diagnosticKey: "harness_feature_missing",
    diagnosticParams: { harness: "Claude Code", feature: "--permission-mode" },
  };

  it("shows every field of a detected, signed-in harness (FS-004 FR-007, AC-008; harmflow-app#68)", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-22T09:05:00Z"));
    try {
      const shown = rows(renderToString(<HarnessInventory harness={checked} locale="en-US" />));
      expect(shown).toMatchObject({
        [i18n.t("harness.detected")]: i18n.t("common.yes"),
        [i18n.t("harness.path")]: `/opt/homebrew/bin/claude · ${i18n.t("harness.pathSource.path")}`,
        [i18n.t("harness.version")]: `2.1.200 · ${i18n.t("harness.versionSupport.below_minimum")}`,
        [i18n.t("harness.authLabel")]: i18n.t("harness.authentication.valid"),
        [i18n.t("harness.models")]: "default, opus, sonnet",
        [i18n.t("harness.efforts")]: "default, low, high",
        [i18n.t("harness.diagnostic")]: "this Claude Code version lacks --permission-mode, which HarmFlow needs",
      });
      expect(shown[i18n.t("harness.lastCheck")]).toBe(formatRelative(checked.lastCheckedAt, "en-US"));
      expect(shown[i18n.t("harness.lastCheck")]).toMatch(/5 min/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("labels a version newer than the tested ones (FS-004 FR-008, AC-009; harmflow-app#68)", () => {
    const shown = rows(renderToString(<HarnessInventory harness={{ ...ready, version: "0.999.0", versionSupport: "untested_newer" }} locale="en-US" />));
    expect(shown[i18n.t("harness.version")]).toBe(`0.999.0 · ${i18n.t("harness.versionSupport.untested_newer")}`);
  });

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
