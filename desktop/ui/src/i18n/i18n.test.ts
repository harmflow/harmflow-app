import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import eventSchema from "../../../../contracts/schemas/event.schema.json";
import taskSchema from "../../../../contracts/schemas/task.schema.json";
import { CONNECTION_STATES } from "../api/transport";
import { COUNTERS } from "../components/Aside";
import { SCRIPT_STREAMS } from "../components/ExecutionDetail";
import { CUSTOM_LISTS, OPTION_FIELDS } from "../components/TaskForm";
import { COPIED_FIELDS } from "../lib/agentCopy";
import { RECEIPT_CLASSES, SUMMARY_CLASSES } from "../lib/costs";
import { DAY_NAMES, INTERVAL_UNITS, MONTH_NAMES, REPEATS } from "../lib/schedule";
import { HARNESS_PROVIDERS, NOTIFIABLE_EVENTS, schemaEnum } from "../lib/schema";
import { SHORTCUTS, SHORTCUT_GROUPS } from "../lib/shortcuts";
import { SPEND_REASONS, SPEND_VIEWS } from "../lib/spend";
import { SCOPE_POINTS } from "../screens/OnboardingScreen";
import { DIRECTORIES, SECTION_GROUPS } from "../screens/SettingsScreen";
import { enUS } from "./en-US";
import { esES } from "./es-ES";
import { ptBR } from "./pt-BR";
import i18n, { systemLanguage } from "./index";

type Tree = { [k: string]: string | Tree };

function leaves(tree: Tree, prefix = ""): string[] {
  return Object.entries(tree).flatMap(([k, v]) => (typeof v === "string" ? [`${prefix}${k}`] : leaves(v, `${prefix}${k}.`)));
}

// As i18next does, a name that holds a dot, such as the notifiable event "execution.started", is found by joining the
// parts of the key that follow, the longest join first.
function lookup(tree: Tree, key: string): unknown {
  const find = (node: unknown, parts: string[]): unknown => {
    if (parts.length === 0) return node;
    if (!node || typeof node !== "object") return undefined;
    for (let n = parts.length; n > 0; n--) {
      const child = (node as Tree)[parts.slice(0, n).join(".")];
      const found = child === undefined ? undefined : find(child, parts.slice(n));
      if (found !== undefined) return found;
    }
    return undefined;
  };
  return find(tree, key.split("."));
}

// A counted key may exist only in its plural forms, which i18next picks from the count.
function defined(tree: Tree, key: string): boolean {
  return (
    typeof lookup(tree, key) === "string" ||
    (typeof lookup(tree, `${key}_one`) === "string" && typeof lookup(tree, `${key}_other`) === "string")
  );
}

const contract = readFileSync(join(__dirname, "../../../../contracts/api/openapi.yaml"), "utf8").split("\n");

// The values of an enum of the API contract: a schema's own, or a property's, reached by name through the properties
// that hold it. Both forms the contract writes are read: [a, b] on the line, or a block of "- a" items, its comment
// lines skipped. A name it cannot reach reads as no value.
function contractEnum(schema: string, ...property: string[]): string[] {
  const indent = (i: number) => contract[i].search(/\S/);
  // The end of the lines under line i: the next line, blank lines aside, that is indented no deeper.
  const end = (i: number) => {
    let j = i + 1;
    while (j < contract.length && (contract[j].trim() === "" || indent(j) > indent(i))) j++;
    return j;
  };
  let at = contract.findIndex((line) => new RegExp(`^ {4}${schema}:\\s*$`).test(line));
  for (const name of property) {
    if (at < 0) return [];
    const last = end(at);
    const from = at;
    at = contract.findIndex((line, i) => i > from && i < last && new RegExp(`^\\s+${name}:`).test(line));
  }
  if (at < 0) return [];
  for (let i = at; i < end(at); i++) {
    const inline = contract[i].match(/\benum:\s*\[([^\]]*)\]/);
    if (inline) return inline[1].split(",").map((v) => v.trim().replace(/^"(.*)"$/, "$1"));
    if (!/^\s+enum:\s*$/.test(contract[i])) continue;
    const values: string[] = [];
    for (const line of contract.slice(i + 1)) {
      if (/^\s+#/.test(line)) continue;
      const item = line.match(/^\s+- ([A-Za-z0-9_.-]+)\s*$/);
      if (!item) break;
      values.push(item[1]);
    }
    return values;
  }
  return [];
}

// The message keys of the API contract: the items of the MessageKey schema's enum.
function contractMessageKeys(): string[] {
  return contractEnum("MessageKey");
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) && !path.includes("/i18n/") ? [path] : [];
  });
}

// A key the interface builds from a value at run time, t(`status.${status}`), is read as its pattern, each placeholder
// written ${}: `status.${}`. The modules may name the value differently from one call to the next.
const PLACEHOLDER = /\$\{([^}]*)\}/g;

// Every t call of the interface's modules whose key is a template literal with a placeholder, by pattern: the files
// that build it, and its placeholders as they are written.
function builtKeys(): Map<string, { files: Set<string>; written: string[] }> {
  const found = new Map<string, { files: Set<string>; written: string[] }>();
  for (const file of sourceFiles(join(__dirname, ".."))) {
    for (const match of readFileSync(file, "utf8").matchAll(/\bt\(\s*`([^`]*)`/g)) {
      if (!match[1].includes("${")) continue;
      const pattern = match[1].replace(PLACEHOLDER, "${}");
      const entry = found.get(pattern) ?? { files: new Set<string>(), written: [] };
      entry.files.add(file);
      entry.written.push(...[...match[1].matchAll(PLACEHOLDER)].map((m) => m[1]));
      found.set(pattern, entry);
    }
  }
  return found;
}

// A value fills a pattern's one placeholder; a list fills its placeholders in order.
type BuiltValue = string | number | readonly string[];

function fill(pattern: string, value: BuiltValue): string {
  const parts = Array.isArray(value) ? value : [value];
  let i = 0;
  return pattern.replace(/\$\{\}/g, () => String(parts[i++]));
}

// The string literals a placeholder holds itself, as in `${!requirements ? "unknown" : "managed"}`.
const literals = (written: string[]) => written.flatMap((w) => [...w.matchAll(/"([^"]*)"/g)].map((m) => m[1]));

// The values each built key takes, by its pattern. A set the daemon sends is read from the contract that declares it:
// the API contract's enums, or the task and event schemas. Any other set is read from the module that holds it. None is
// written here, so a value added there is checked here.
const BUILT_KEYS: Record<string, (written: string[]) => readonly BuiltValue[]> = {
  "status.${}": () => contractEnum("ExecutionStatus"),
  "trigger.${}": () => contractEnum("Trigger"),
  "testsStatus.${}": () => [...contractEnum("ExecutionSummary", "testsStatus"), ...contractEnum("WorkItemResult", "tests", "status")],
  "attemptOutcome.${}": () => contractEnum("Attempt", "outcome"),
  "warnings.${}": () => contractEnum("Warning", "code"),
  "eventTypes.${}": () => eventSchema.$defs.eventType.enum,
  "executions.detail.commandStatus.${}": () => contractEnum("Execution", "commands", "status"),
  "executions.detail.costs.classes.${}": () => [...SUMMARY_CLASSES, ...RECEIPT_CLASSES],
  "executions.detail.script.streams.${}": () => SCRIPT_STREAMS,
  "harness.names.${}": () => [...HARNESS_PROVIDERS, ...contractEnum("Harness", "id")],
  "harness.readiness.${}": () => contractEnum("ReadinessState"),
  "harness.authentication.${}": () => [...contractEnum("Harness", "authentication"), ...contractEnum("Tool", "authentication")],
  "harness.versionSupport.${}": () => contractEnum("Harness", "versionSupport"),
  "harness.pathSource.${}": () => contractEnum("Harness", "pathSource"),
  "harness.capabilitySource.${}": () => contractEnum("Harness", "capabilitySource"),
  "harness.signIn.${}": () => contractEnum("Harness", "account", "signInMethod"),
  "harness.requirementsState.${}": literals,
  "tools.${}": () => contractEnum("Tool", "id"),
  "tasks.types.${}": () => schemaEnum(["spec", "type"]),
  "tasks.fields.${}": () => [...CUSTOM_LISTS, ...COPIED_FIELDS.map((f) => f.field)],
  "tasks.fields.parametersOf.${}": () => ["skill", "agent"],
  "tasks.paramTypes.${}": () => contractEnum("ParameterDeclaration", "properties", "type"),
  "tasks.options.${}.${}": () => Object.entries(OPTION_FIELDS).flatMap(([key, path]) => schemaEnum(path).map((v) => [key, v])),
  "tasks.options.fallbackOn.${}": () => schemaEnum(["spec", "harness", "fallback", "on"]),
  "tasks.options.notifiable.${}": () => NOTIFIABLE_EVENTS,
  "tasks.options.systems.${}": () => schemaEnum(["spec", "script", "systems"]),
  "tasks.options.repeat.${}": () => REPEATS,
  "tasks.options.intervalUnit.${}": () => INTERVAL_UNITS,
  "cronText.weekdays.${}": () => Object.values(DAY_NAMES),
  "cronText.weekdaysOn.${}": () => Object.values(DAY_NAMES),
  "cronText.months.${}": () => Object.values(MONTH_NAMES),
  "settings.groups.${}": () => SECTION_GROUPS.map((g) => g.key),
  "settings.languages.${}": () => contractEnum("Settings", "language"),
  "settings.notificationLevels.${}": () => contractEnum("Settings", "notificationLevel"),
  "settings.shortcuts.groups.${}": () => SHORTCUT_GROUPS,
  "settings.shortcuts.labels.${}": () => Object.keys(SHORTCUTS),
  "settings.dirs.${}": () => DIRECTORIES,
  "settings.tokenStores.${}": () => contractEnum("Diagnostics", "tokenStore"),
  "counters.${}": () => COUNTERS.map((c) => c.key),
  "overview.spend.${}": () => SPEND_VIEWS,
  "overview.spend.reasons.${}": () => SPEND_REASONS,
  "onboarding.scope.points.${}": () => SCOPE_POINTS,
  "connection.${}": () => CONNECTION_STATES,
};

// Every module of the interface but its tests, i18n/ included, since i18n/index.ts is where i18next is set up.
function interfaceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return interfaceFiles(path);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
  });
}

// The functions of i18next that add resources to a started instance or load them from a backend.
const RESOURCE_LOADERS = /\b(addResourceBundle|addResources|addResource|loadResources|loadLanguages|loadNamespaces|reloadResources)\s*\(/g;

describe("i18n resources", () => {
  const reference = leaves(enUS as unknown as Tree).sort();

  it.each([
    ["pt-BR", ptBR],
    ["es-ES", esES],
  ])("%s has exactly the keys of en-US", (_lang, resource) => {
    expect(leaves(resource as unknown as Tree).sort()).toEqual(reference);
  });

  // Broken interpolation is a known risk: a translation keeps every placeholder and nested key of en-US.
  it.each([
    ["pt-BR", ptBR],
    ["es-ES", esES],
  ])("%s keeps the placeholders of en-US", (lang, resource) => {
    const marks = (text: unknown) => [...new Set(String(text).match(/\{\{[^}]+\}\}|\$t\([^)]*\)/g) ?? [])].sort();
    for (const key of reference) {
      expect(marks(lookup(resource as unknown as Tree, key)), `${lang} ${key}`).toEqual(marks(lookup(enUS as unknown as Tree, key)));
    }
  });

  it("has no empty translation", () => {
    for (const resource of [enUS, ptBR, esES]) {
      for (const key of leaves(resource as unknown as Tree)) expect(lookup(resource as unknown as Tree, key), key).not.toBe("");
    }
  });

  it("labels every event type of the contract", () => {
    for (const type of eventSchema.$defs.eventType.enum) {
      expect(typeof lookup(enUS as unknown as Tree, `eventTypes.${type}`), type).toBe("string");
    }
  });

  it.each([
    ["en-US", enUS],
    ["pt-BR", ptBR],
    ["es-ES", esES],
  ])("%s has a text for every message key of the contract", (lang, resource) => {
    const keys = contractMessageKeys();
    expect(keys.length, "message keys read from the contract").toBeGreaterThan(0);
    const missing = keys.filter((key) => !defined(resource as unknown as Tree, `messages.${key}`));
    expect(missing.map((key) => `${lang} ${key}`)).toEqual([]);
  });

  it("labels every notifiable event", () => {
    for (const ev of taskSchema.$defs.notifiableEvent.enum) {
      expect(typeof lookup(enUS as unknown as Tree, `notify.${ev}.title`), ev).toBe("string");
      expect((enUS.tasks.options.notifiable as Record<string, string>)[ev], ev).toBeTruthy();
    }
  });

  it("defines every literal key the interface uses", () => {
    const missing: string[] = [];
    for (const file of sourceFiles(join(__dirname, ".."))) {
      const text = readFileSync(file, "utf8");
      for (const match of text.matchAll(/\bt\(\s*"([a-zA-Z0-9_.-]+)"/g)) {
        const key = match[1];
        if (!defined(enUS as unknown as Tree, key)) missing.push(`${file}: ${key}`);
      }
    }
    expect(missing).toEqual([]);
  });

  // pt-BR and es-ES have exactly the keys of en-US (above), so a built key en-US defines is defined in each language.
  const built = builtKeys();

  it("knows where the values of every key built at run time come from", () => {
    expect(built.size, "built keys found in the interface").toBeGreaterThan(0);
    const unknown = [...built].filter(([pattern]) => !Object.hasOwn(BUILT_KEYS, pattern));
    expect(unknown.flatMap(([pattern, { files }]) => [...files].map((file) => `${file}: ${pattern}`))).toEqual([]);
    expect(Object.keys(BUILT_KEYS).filter((pattern) => !built.has(pattern)), "patterns no module builds").toEqual([]);
  });

  it.each(Object.keys(BUILT_KEYS))("defines in en-US every key built as %s", (pattern) => {
    const values = BUILT_KEYS[pattern](built.get(pattern)?.written ?? []);
    expect(values.length, `values of ${pattern}`).toBeGreaterThan(0);
    const missing = values.map((value) => fill(pattern, value)).filter((key) => !defined(enUS as unknown as Tree, key));
    expect(missing).toEqual([]);
  });

  // Translations are maintained in the app: its languages are the ones it ships, and none is added from outside.
  it("holds only the three bundled languages once started, each from the resource the app ships, with no backend", async () => {
    if (!i18n.isInitialized) await new Promise((resolve) => i18n.on("initialized", resolve));
    expect(Object.keys(i18n.store.data).sort()).toEqual(["en-US", "es-ES", "pt-BR"]);
    for (const [lang, resource] of [
      ["en-US", enUS],
      ["pt-BR", ptBR],
      ["es-ES", esES],
    ] as const) {
      expect(Object.keys(i18n.store.data[lang]), lang).toEqual(["translation"]);
      expect(i18n.getResourceBundle(lang, "translation"), lang).toBe(resource);
    }
    expect(i18n.modules.backend).toBeUndefined();
    expect(i18n.options.backend).toBeUndefined();
    expect(i18n.services.backendConnector.backend).toBeFalsy();
  });

  // The one change to the resources after they ship is the dashboard's registrations': registrations.tsx merges their
  // texts into the languages above before the first render, adding no language and replacing no text, as
  // dashboard.test.tsx proves.
  it("adds or loads no resource at run time", () => {
    const calls: string[] = [];
    for (const file of interfaceFiles(join(__dirname, ".."))) {
      for (const match of readFileSync(file, "utf8").matchAll(RESOURCE_LOADERS)) calls.push(`${file}: ${match[1]}`);
    }
    expect(calls).toEqual([]);
  });

  it("adopts the system language when official, en-US otherwise", () => {
    expect(systemLanguage(["pt-PT"])).toBe("pt-BR");
    expect(systemLanguage(["es-MX", "en-US"])).toBe("es-ES");
    expect(systemLanguage(["fr-FR", "de-DE"])).toBe("en-US");
    expect(systemLanguage([])).toBe("en-US");
  });
});
