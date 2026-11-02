// @vitest-environment happy-dom
// The installed-agents inventory shows every field a check found (harmflow-app#68). A ready harness whose version or
// sign-in detection could not establish is shown in a warning tone, with what is unknown (harmflow-app#71). Each
// installation is offered as a choice, driven in a DOM against a stand-in for the daemon's local API (harmflow-app#70).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Harness } from "../api/types";
import i18n from "../i18n";
import { formatRelative } from "../lib/format";
import { patchStateForTests, useStore } from "../state/store";
import { HarnessInventory, HarnessRow, readinessTone } from "./Harnesses";

// The daemon as the client sees it: Codex's installations, and every request it received. A path PUT is in use and
// offered with the ones found; a null one returns to the first found, as the daemon does.
const daemon = vi.hoisted(() => ({
  found: [] as string[],
  codex: undefined as unknown as Harness,
  requests: [] as Array<{ method: string; path: string; body?: unknown }>,
}));
vi.mock("../api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string, body?: unknown) => {
      daemon.requests.push({ method, path, body });
      if (method === "PUT" && path === "/harnesses/codex") {
        const chosen = (body as { path: string | null }).path;
        const installation = chosen ?? daemon.found[0];
        daemon.codex = {
          ...daemon.codex,
          installation,
          path: installation,
          pathSource: chosen ? "manual" : "path",
          installations: daemon.found.includes(installation) ? daemon.found : [...daemon.found, installation],
        };
        return { status: 200, body: daemon.codex };
      }
      if (method === "GET" && path === "/harnesses") return { status: 200, body: [daemon.codex] };
      if (method === "GET" && path === "/tools") return { status: 200, body: [] };
      return { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

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

  it("shows a signed-in Codex whose managed requirements override a setting in a warning tone, naming it (harmflow-app#442)", () => {
    const overridden: Harness = {
      ...ready,
      id: "codex",
      name: "Codex",
      diagnostic: "Codex's managed requirements do not allow approval_policy = never",
      diagnosticKey: "codex_requirements_disallow",
      diagnosticParams: { settings: "approval_policy = never", allowed: "approval_policy = on-request, untrusted" },
    };
    expect(readinessTone(overridden)).toBe("warning");
    const html = renderToString(<HarnessRow harness={overridden} />);
    expect(html).toContain("chip--warning");
    expect(html).toContain(
      "do not allow approval_policy = never, which HarmFlow runs it with, so Codex falls back to what they allow: " +
        "approval_policy = on-request, untrusted",
    );
  });

  it("shows a ready harness whose help check timed out in a warning tone, saying so (harmflow-app#329)", () => {
    const helpTimedOut: Harness = {
      ...ready,
      diagnostic: "the Claude Code --help check timed out",
      diagnosticKey: "harness_help_timed_out",
      diagnosticParams: { harness: "Claude Code", command: "--help" },
    };
    expect(readinessTone(helpTimedOut)).toBe("warning");
    const html = renderToString(<HarnessRow harness={helpTimedOut} />);
    expect(html).toContain("chip--warning");
    expect(html).toContain(
      "the Claude Code --help check timed out, so the flags it has are unknown; the first attempt proves them",
    );
  });

  it("shows a ready Codex whose top-level help check timed out in a warning tone, its app server unknown (harmflow-app#643)", () => {
    const appServerUnknown: Harness = {
      ...ready,
      id: "codex",
      name: "Codex",
      diagnostic: "the Codex --help check timed out",
      diagnosticKey: "codex_app_server_help_timed_out",
      diagnosticParams: { harness: "Codex" },
    };
    expect(readinessTone(appServerUnknown)).toBe("warning");
    const html = renderToString(<HarnessRow harness={appServerUnknown} />);
    expect(html).toContain("chip--warning");
    expect(html).toContain(
      "the Codex --help check timed out, so whether it has an app server is unknown; the first call to the app server proves it",
    );
    expect(html).not.toContain("flags");
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

  it("shows every field of a detected, signed-in harness (harmflow-app#68)", () => {
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

  it("labels a version newer than the tested ones (harmflow-app#68)", () => {
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

// harmflow-app#571: Codex's card shows how its account signs in and its plan, and whether an administrator manages its
// requirements, with what they allow; a plan it cannot read shows as unknown with the reason. Credentials stay unshown.
describe("HarnessInventory of a Codex account", () => {
  const codex: Harness = { ...ready, id: "codex", name: "Codex", path: "/opt/homebrew/bin/codex", version: "0.163.0" };
  const unmanaged = { managed: false, approvalPolicies: null, approvalsReviewers: null, permissionProfiles: null };

  it("shows a personal plan, its sign-in, and requirements nobody manages", () => {
    const shown = rows(
      renderToString(
        <HarnessInventory harness={{ ...codex, account: { signInMethod: "chatgpt", plan: "plus" }, managedRequirements: unmanaged }} locale="en-US" />,
      ),
    );
    expect(shown).toMatchObject({
      [i18n.t("harness.signInMethod")]: "ChatGPT",
      [i18n.t("harness.plan")]: "plus",
      [i18n.t("harness.requirements")]: "Not managed",
    });
    expect(shown).not.toHaveProperty(i18n.t("harness.allowedApprovalPolicies"));
  });

  it("shows a workspace plan with the requirements an administrator manages and what they allow", () => {
    const shown = rows(
      renderToString(
        <HarnessInventory
          harness={{
            ...codex,
            account: { signInMethod: "chatgpt", plan: "business" },
            managedRequirements: { managed: true, approvalPolicies: ["never", "on-request"], approvalsReviewers: [], permissionProfiles: null },
          }}
          locale="en-US"
        />,
      ),
    );
    expect(shown).toMatchObject({
      [i18n.t("harness.plan")]: "business",
      [i18n.t("harness.requirements")]: "Managed by an administrator",
      [i18n.t("harness.allowedApprovalPolicies")]: "never, on-request",
      [i18n.t("harness.allowedApprovalsReviewers")]: "None",
      [i18n.t("harness.allowedPermissionProfiles")]: "Any",
    });
  });

  it("shows an unknown plan with Codex's reason, and a failing account read with its error", () => {
    const unknownPlan = rows(
      renderToString(
        <HarnessInventory
          harness={{
            ...codex,
            account: { signInMethod: "chatgpt", plan: "unknown", planReason: "Codex reports the account's plan as unknown", planReasonKey: "codex_plan_unknown" },
            managedRequirements: unmanaged,
          }}
          locale="en-US"
        />,
      ),
    );
    expect(unknownPlan[i18n.t("harness.plan")]).toBe("Unknown · Codex reports the account's plan as unknown");

    const failed = rows(
      renderToString(
        <HarnessInventory
          harness={{
            ...codex,
            account: {
              signInMethod: "unknown",
              plan: "unknown",
              planReason: "Codex's app server answered account/read with an error: account unavailable",
              planReasonKey: "codex_app_server_error",
              planReasonParams: { method: "account/read", detail: "account unavailable" },
            },
          }}
          locale="en-US"
        />,
      ),
    );
    expect(failed).toMatchObject({
      [i18n.t("harness.signInMethod")]: "Unknown",
      [i18n.t("harness.plan")]: "Unknown · Codex's app server answered account/read with an error: account unavailable",
      [i18n.t("harness.requirements")]: "Unknown",
    });
  });

  it("shows an API key's sign-in with no plan row, and no account rows for a harness that does not say", () => {
    const apiKey = rows(renderToString(<HarnessInventory harness={{ ...codex, account: { signInMethod: "api_key" }, managedRequirements: unmanaged }} locale="en-US" />));
    expect(apiKey[i18n.t("harness.signInMethod")]).toBe("API key");
    expect(apiKey).not.toHaveProperty(i18n.t("harness.plan"));
    const claude = rows(renderToString(<HarnessInventory harness={ready} locale="en-US" />));
    expect(claude).not.toHaveProperty(i18n.t("harness.signInMethod"));
    expect(claude).not.toHaveProperty(i18n.t("harness.requirements"));
  });

  it("labels the account's rows in pt-BR and es-ES", async () => {
    try {
      for (const [lang, plan, managed] of [
        ["pt-BR", "Plano", "Gerenciados por um administrador"],
        ["es-ES", "Plan", "Gestionados por un administrador"],
      ]) {
        await i18n.changeLanguage(lang);
        const shown = rows(
          renderToString(
            <HarnessInventory
              harness={{ ...codex, account: { signInMethod: "chatgpt", plan: "team" }, managedRequirements: { ...unmanaged, managed: true } }}
              locale={lang}
            />,
          ),
        );
        expect(shown[plan]).toBe("team");
        expect(shown[i18n.t("harness.requirements")]).toBe(managed);
      }
    } finally {
      await i18n.changeLanguage("en-US");
    }
  });
});

describe("choosing an installation", () => {
  const onPath = "/usr/local/bin/codex";
  const inApp = "/Applications/Codex.app/Contents/Resources/codex-cli/bin/codex";
  const byHand = "/opt/tools/codex-next/codex";
  let root: Root | undefined;
  let host: HTMLElement | undefined;

  beforeAll(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = undefined;
    daemon.requests.length = 0;
  });

  /** Lets the stand-in daemon's answers arrive and React render them. */
  const settle = () => act(() => new Promise<void>((resolve) => setImmediate(resolve)));

  // The inventory as Settings shows it: from the store, so a refresh is seen.
  function Inventory() {
    const codex = useStore((s) => s.harnesses.find((h) => h.id === "codex"));
    return codex ? <HarnessInventory harness={codex} locale="en-US" /> : null;
  }

  async function open(installation: string, installations: string[]) {
    daemon.found = [onPath, inApp];
    daemon.codex = {
      ...ready,
      id: "codex",
      name: "Codex",
      installation,
      path: installation,
      pathSource: installation === byHand ? "manual" : "path",
      installations,
    };
    patchStateForTests({ harnesses: [daemon.codex] });
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Inventory />));
  }

  // Each installation as listed: its path, and "In use" or the control that chooses it.
  const offered = () =>
    [...host!.querySelectorAll(".installations__item")].map((li) => [li.querySelector(".mono")!.textContent, li.querySelector(".chip, button")!.textContent]);

  const button = (label: string) => {
    const found = [...host!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label);
    expect(found, label).toBeDefined();
    return found!;
  };

  const writes = () => daemon.requests.filter((r) => r.method === "PUT");

  it("lists each installation with a control to use it, the one in use marked, and uses the one chosen", async () => {
    await open(byHand, [onPath, inApp, byHand]);
    const use = i18n.t("harness.use");
    const inUse = i18n.t("harness.inUse");
    expect(offered()).toEqual([
      [onPath, use],
      [inApp, use],
      [byHand, inUse],
    ]);

    await act(async () => button(i18n.t("harness.useInstallation", { path: inApp })).click());
    await settle();
    expect(writes()).toEqual([{ method: "PUT", path: "/harnesses/codex", body: { path: inApp } }]);
    expect(daemon.requests.map((r) => `${r.method} ${r.path}`)).toContain("GET /harnesses");
    // The refreshed inventory: the chosen one in use, and the path set by hand no longer offered, since it was replaced.
    expect(offered()).toEqual([
      [onPath, use],
      [inApp, inUse],
    ]);
  });

  it("keeps Set path and Detect automatically working", async () => {
    await open(onPath, [onPath, inApp]);
    expect(host!.textContent).not.toContain(i18n.t("harness.autoDetect"));

    await act(async () => button(i18n.t("harness.setPath")).click());
    const input = host!.querySelector<HTMLInputElement>(".inline-form input")!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, ` ${byHand} `);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => button(i18n.t("common.save")).click());
    await settle();
    expect(writes()).toEqual([{ method: "PUT", path: "/harnesses/codex", body: { path: byHand } }]);
    expect(offered()).toEqual([
      [onPath, i18n.t("harness.use")],
      [inApp, i18n.t("harness.use")],
      [byHand, i18n.t("harness.inUse")],
    ]);

    await act(async () => button(i18n.t("harness.autoDetect")).click());
    await settle();
    expect(writes()).toEqual([
      { method: "PUT", path: "/harnesses/codex", body: { path: byHand } },
      { method: "PUT", path: "/harnesses/codex", body: { path: null } },
    ]);
    expect(offered()).toEqual([
      [onPath, i18n.t("harness.inUse")],
      [inApp, i18n.t("harness.use")],
    ]);
  });
});
