// @vitest-environment happy-dom
// Drives the dashboard's entry file, its registration point (harmflow-app#323): a registered screen joins the sidebar
// and opens from it, a registered Settings section joins its group and opens from the list and from a navigation, and
// their texts go through the dashboard's translations, never replacing one of its own. With nothing registered, the
// sidebar and Settings hold their built-in items alone. A registration that fails its check shows the root's failure
// screen, and nothing of the dashboard starts.
import { act } from "react";
import type { Root as ReactRoot } from "react-dom/client";
import { useTranslation } from "react-i18next";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Settings } from "./api/types";
import { NAV } from "./components/Shell";
import { renderDashboard, type Registration } from "./dashboard";
import i18n, { resources } from "./i18n";
import { SECTION_GROUPS } from "./screens/SettingsScreen";
import { patchStateForTests } from "./state/store";

// The daemon answers nothing, and keeps every request it was sent.
const daemon = vi.hoisted(() => ({ sent: [] as { method: string; path: string }[] }));

vi.mock("./api/transport", () => ({
  isTauri: () => false,
  EVENT_TYPES: [],
  transport: {
    mode: "http",
    request: async (method: string, path: string) => {
      daemon.sent.push({ method, path });
      return { status: 404, body: null };
    },
    requestText: async () => ({ status: 404, text: "" }),
    stream: () => () => {},
  },
}));

// No event stream or periodic refresh; a start of the sync is counted, since a failed registration must start none.
const sync = vi.hoisted(() => ({ started: 0 }));

vi.mock("./state/store", async (importOriginal) => {
  const original = await importOriginal<typeof import("./state/store")>();
  return {
    ...original,
    startSync: () => {
      sync.started++;
    },
  };
});

const settings: Settings = {
  language: "en-US",
  trayEnabled: true,
  keepTrayOnQuit: false,
  showRestrictionWarnings: true,
  notificationLevel: "all",
  logRetentionDays: 30,
  theme: "harmflow-dark",
  autostart: { enabled: false, mechanism: "launchd" },
  pathVariables: {},
  allowedFolders: [],
  onboardingCompleted: true,
  quota: {},
};

function StubScreen() {
  const { t } = useTranslation();
  return <p className="stub-screen">{(t as unknown as (key: string) => string)("stub.body")}</p>;
}

function StubSection() {
  return <p className="stub-section">The stub section</p>;
}

/** A registration of one screen and one Settings section, with its texts in en-US and pt-BR. */
const stub: Registration = {
  screens: [{ route: "notes", icon: "list", component: StubScreen }],
  settingsSections: [{ key: "stub", group: "agents", icon: "agent", component: StubSection }],
  strings: {
    "en-US": {
      nav: { notes: "Notes" },
      screens: { notes: { breadcrumb: "notes", title: "Notes", subtitle: "What the stub screen keeps" } },
      settings: { sections: { stub: "Stub section" } },
      stub: { body: "The stub screen" },
    },
    "pt-BR": {
      nav: { notes: "Notas" },
      screens: { notes: { breadcrumb: "notas", title: "Notas", subtitle: "O que a tela de teste guarda" } },
      settings: { sections: { stub: "Seção de teste" } },
      stub: { body: "A tela de teste" },
    },
  },
};

let root: ReactRoot | undefined;
let host: HTMLElement | undefined;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

beforeEach(async () => {
  await i18n.changeLanguage("en-US");
  localStorage.clear();
  patchStateForTests({ settings, daemonRunning: true, connection: "connected" });
  sync.started = 0;
  daemon.sent.length = 0;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = undefined;
});

/** Renders the dashboard as main.tsx does, with the registrations given. */
async function launch(...registrations: Registration[]) {
  host = document.body.appendChild(document.createElement("div"));
  await act(async () => {
    root = renderDashboard(host!, ...registrations);
  });
}

const navItems = () => [...host!.querySelectorAll<HTMLButtonElement>(".sidebar .nav__item")];
const navButton = (label: string) => navItems().find((b) => b.textContent?.startsWith(label))!;
const sectionItems = () => [...host!.querySelectorAll<HTMLButtonElement>(".settings-nav__item")];
const title = () => host!.querySelector(".topbar__title")?.textContent;

const en = resources["en-US"].translation;
const builtinNav = NAV.map((item) => en.nav[item.key]);
const builtinSections = [...SECTION_GROUPS.flatMap((group) => group.sections), "about" as const].map((s) => en.settings.sections[s]);

describe("a registered screen", () => {
  it("follows the built-in screens in the sidebar, and opens from it with its own texts", async () => {
    await launch(stub);
    expect(navItems().map((b) => b.textContent)).toEqual([...builtinNav, "Notes"]);

    await act(async () => navButton("Notes").click());
    expect(navButton("Notes").getAttribute("aria-current")).toBe("page");
    expect(title()).toBe("Notes");
    expect(host!.querySelector(".topbar__subtitle")?.textContent).toBe("What the stub screen keeps");
    expect(host!.querySelector(".stub-screen")?.textContent).toBe("The stub screen");

    await act(async () => navButton("Executions").click());
    expect(host!.querySelector(".stub-screen")).toBeNull();
  });

  it("switches language with the rest of the interface", async () => {
    await launch(stub);
    await act(async () => navButton("Notes").click());
    await act(async () => void (await i18n.changeLanguage("pt-BR")));
    expect(navButton("Notas")).toBeDefined();
    expect(title()).toBe("Notas");
    expect(host!.querySelector(".stub-screen")?.textContent).toBe("A tela de teste");
  });

  it("has no shortcut of its own, and its collapsed icon is named in a tooltip", async () => {
    await launch(stub);
    expect(navButton("Notes").hasAttribute("aria-keyshortcuts")).toBe(false);
    await act(async () => host!.querySelector<HTMLButtonElement>(".sidebar__toggle")!.click());
    await act(async () => void navButton("Notes").dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(document.body.querySelector(".tooltip")?.textContent).toBe("Notes");
  });
});

describe("a registered Settings section", () => {
  it("ends its group in the section list, and opens from it", async () => {
    await launch(stub);
    await act(async () => navButton("Settings").click());
    const agents = host!.querySelector("#settings-group-agents")!.parentElement!;
    expect([...agents.querySelectorAll(".settings-nav__item")].map((b) => b.textContent)).toEqual([
      en.settings.sections.agents,
      en.settings.sections.paths,
      "Stub section",
    ]);

    await act(async () => sectionItems().find((b) => b.textContent === "Stub section")!.click());
    expect(host!.querySelector(".settings__title")?.textContent).toBe("Stub section");
    expect(host!.querySelector(".stub-section")).not.toBeNull();
    expect(host!.querySelector(".breadcrumb")?.textContent).toContain("/ stub section");
  });

  it("opens from a navigation to its section, as the app menu sends one", async () => {
    await launch(stub);
    await act(async () => patchStateForTests({ navigation: { screen: "settings", section: "stub", nonce: 1 } }));
    expect(host!.querySelector(".settings__title")?.textContent).toBe("Stub section");
    expect(host!.querySelector(".stub-section")).not.toBeNull();
  });
});

describe("nothing registered", () => {
  it("keeps the sidebar and Settings to their built-in items", async () => {
    await launch();
    expect(navItems().map((b) => b.textContent)).toEqual(builtinNav);
    await act(async () => navButton("Settings").click());
    expect(sectionItems().map((b) => b.textContent)).toEqual(builtinSections);
    expect(sync.started).toBeGreaterThan(0);
  });

  it("opens no section for a navigation to a key nothing registered", async () => {
    await launch();
    await act(async () => patchStateForTests({ navigation: { screen: "settings", section: "stub", nonce: 1 } }));
    expect(host!.querySelector(".settings__title")?.textContent).toBe(en.settings.sections.general);
  });
});

describe("a registration that fails its check", () => {
  const screen = (route: string) => ({ route, icon: "list" as const, component: StubScreen });
  const section = (key: string, group = "agents") => ({ key, group: group as "agents", icon: "agent" as const, component: StubSection });
  const cases: Array<{ name: string; registrations: Registration[]; message: string }> = [
    { name: "a built-in screen's route", registrations: [{ screens: [screen("tasks")] }], message: 'built-in screen: "tasks"' },
    { name: "a route two registrations share", registrations: [{ screens: [screen("notes")] }, { screens: [screen("notes")] }], message: 'Two registered screens take the route "notes"' },
    { name: "an empty route", registrations: [{ screens: [screen("")] }], message: 'route is not a key: ""' },
    { name: "a route with a dot", registrations: [{ screens: [screen("notes.list")] }], message: 'route is not a key: "notes.list"' },
    { name: "a built-in section's key", registrations: [{ settingsSections: [section("general")] }], message: 'built-in section: "general"' },
    { name: "a section key two registrations share", registrations: [{ settingsSections: [section("stub")] }, { settingsSections: [section("stub")] }], message: 'Two registered Settings sections take the key "stub"' },
    { name: "a group Settings does not have", registrations: [{ settingsSections: [section("stub", "other")] }], message: 'joins no group of Settings: "other"' },
    { name: "strings for a language the dashboard does not have", registrations: [{ strings: { "fr-FR": { nav: { notes: "Notes" } } } as Registration["strings"] }], message: 'language the dashboard does not have: "fr-FR"' },
    { name: "a route every object has", registrations: [{ screens: [screen("constructor")] }], message: 'route is not a key: "constructor"' },
    { name: "a route with an underscore", registrations: [{ screens: [screen("tasks_archive")] }], message: 'route is not a key: "tasks_archive"' },
    // A registration adds texts, and never one that would be read in place of one of the dashboard's.
    { name: "a built-in text's key", registrations: [{ strings: { "en-US": { nav: { tasks: "Not the built-in text" } } } }], message: 'en-US "nav.tasks"' },
    { name: "a built-in text's key, in another language", registrations: [{ strings: { "es-ES": { nav: { tasks: "No es el texto" } } } }], message: 'es-ES "nav.tasks"' },
    { name: "a plural form a built-in text lacks", registrations: [{ strings: { "en-US": { tasks: { groups: { count_zero: "No tasks at all" } } } } }], message: 'en-US "tasks.groups.count_zero"' },
    { name: "a context form of a built-in text", registrations: [{ strings: { "en-US": { nav: { tasks_beta: "Tasks (beta)" } } } }], message: 'en-US "nav.tasks_beta"' },
    { name: "a text where the dashboard has a group", registrations: [{ strings: { "en-US": { nav: "Navigation" } } }], message: 'en-US "nav"' },
    { name: "a group where the dashboard has a text", registrations: [{ strings: { "en-US": { nav: { tasks: { label: "Tasks" } } } } }], message: 'en-US "nav.tasks"' },
    { name: "a key every object has", registrations: [{ strings: JSON.parse('{"en-US": {"__proto__": {"polluted": "yes"}}}') as Registration["strings"] }], message: 'en-US "__proto__"' },
    // i18next reads a key that holds a dot as the path it spells when that path has no text, a plural form included.
    { name: "a key that spells a built-in text's path", registrations: [{ strings: { "en-US": { "tasks.groups.count_zero": "No tasks" } } }], message: 'not a plain key: en-US "tasks.groups.count_zero"' },
    { name: "a key that spells a path inside a group", registrations: [{ strings: { "en-US": { tasks: { "groups.count_zero": "No tasks" } } } }], message: 'not a plain key: en-US "tasks.groups.count_zero"' },
    { name: "a key that names a namespace", registrations: [{ strings: { "en-US": { "translation:nav": { notes: "Notes" } } } }], message: 'not a plain key: en-US "translation:nav"' },
  ];

  for (const c of cases) {
    it(`shows the root's failure screen and starts nothing: ${c.name}`, async () => {
      const logged = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        await launch(...c.registrations, { strings: { "en-US": { failed: { text: "Never added" } } } });
        const failure = host!.querySelector(".crash-root [role='alert']");
        expect(failure?.textContent).toContain(en.crash.rootTitle);
        expect(failure?.textContent).toContain(c.message);
        expect(host!.querySelector(".app")).toBeNull();
        expect(sync.started).toBe(0);
        expect(daemon.sent).toEqual([]);
        // A failed check adds none of the registrations' texts, and changes none of the dashboard's.
        expect(i18n.exists("failed.text")).toBe(false);
        expect(i18n.t("nav.tasks")).toBe(en.nav.tasks);
        expect(i18n.t("tasks.groups.count", { count: 0 })).toBe("0 tasks");
        expect(({} as { polluted?: string }).polluted).toBeUndefined();
        expect(logged).toHaveBeenCalledWith("HarmFlow: render error", expect.anything(), expect.anything());
      } finally {
        logged.mockRestore();
      }
    });
  }
});
