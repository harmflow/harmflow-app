// What a registration adds to the dashboard, and how the shell reads it. The entry file, dashboard.tsx, checks the
// registrations it is given before anything renders; the shell then reads what they add through one context, which holds
// nothing when nothing is registered.
import { createContext, useContext, type ComponentType } from "react";
import type { IconName } from "./components/Icon";
import { NAV } from "./components/Shell";
import { LANGUAGES, resources, type Language } from "./i18n";
import { SECTION_GROUPS, isSettingsSection } from "./screens/SettingsScreen";

/** A group of Settings' section list, which a registered section joins at its end. */
export type SettingsGroup = (typeof SECTION_GROUPS)[number]["key"];

/**
 * A screen a registration adds: its route is the key the shell navigates by, and its entry in the sidebar follows the
 * built-in ones. Its texts are looked up as a built-in screen's are: `nav.<route>` names the entry, and
 * `screens.<route>.breadcrumb`, `.title`, and `.subtitle` head the screen.
 */
export interface RegisteredScreen {
  route: string;
  icon: IconName;
  component: ComponentType;
}

/** A section a registration adds to Settings, at the end of its group; `settings.sections.<key>` names it. */
export interface RegisteredSettingsSection {
  key: string;
  group: SettingsGroup;
  icon: IconName;
  component: ComponentType;
}

/**
 * What one registration adds to the dashboard. Every field is optional, and so is every field added later. `strings`
 * holds texts per language, added to the dashboard's own translations, so a registered screen or section switches
 * language with the rest of the interface. Each key of `strings` is a plain key, with no `.` or `:`, and none can be
 * read in place of one of the dashboard's own texts (`register`). `register` reads a registration once, into a copy:
 * `strings` is plain data, which `structuredClone` copies, and a getter is read once.
 */
export interface Registration {
  screens?: readonly RegisteredScreen[];
  settingsSections?: readonly RegisteredSettingsSection[];
  strings?: Partial<Record<Language, Record<string, unknown>>>;
}

/** Every registration's additions, merged and checked. */
export interface Registered {
  screens: readonly RegisteredScreen[];
  settingsSections: readonly RegisteredSettingsSection[];
}

/** What the shell reads when nothing is registered. */
export const NOTHING_REGISTERED: Registered = { screens: [], settingsSections: [] };

// A route or a section's key is one key of its texts' keys, such as `nav.<route>`, so it holds no dot and no space, and
// no `_`, which i18next reads as the start of a plural's or a context's suffix.
const KEY = /^[A-Za-z][A-Za-z0-9-]*$/;

// One key of a registered text's path. i18next reads `.` as the separator of a key's path, `:` as a namespace's, and a
// key that holds either as the path it spells too, so neither is in a key.
const TEXT_KEY = /^[A-Za-z0-9_-]+$/;

// Names an object already has from its prototype: as a text's key, one would be read from, or written to, every object.
const RESERVED = new Set(["__proto__", "constructor", "prototype"]);

const isKey = (key: string) => KEY.test(key) && !RESERVED.has(key);

/**
 * Merges the registrations and checks them, before anything renders: a route or a key that is not a plain key, that a
 * built-in screen or section already has, or that two registrations share, throws, and so does a group or a language
 * the dashboard does not have, or a text whose key is not a plain key or that could be read in place of one of the
 * dashboard's own (`clash`). Nothing is added to the translations unless every registration passes.
 */
export function register(given: readonly Registration[]): Registered {
  // The checks and the merge below read one copy of each registration, so nothing can show the checks one text and the
  // merge another.
  const registrations = Array.from(given, copied);
  const screens = registrations.flatMap((r) => r.screens ?? []);
  const settingsSections = registrations.flatMap((r) => r.settingsSections ?? []);
  const builtinScreens = new Set<string>(NAV.map((item) => item.key));
  const routes = new Set<string>();
  for (const { route } of screens) {
    if (!isKey(route)) throw new Error(`A registered screen's route is not a key: "${route}"`);
    if (builtinScreens.has(route)) throw new Error(`A registered screen takes the route of a built-in screen: "${route}"`);
    if (routes.has(route)) throw new Error(`Two registered screens take the route "${route}"`);
    routes.add(route);
  }
  const groups = new Set<string>(SECTION_GROUPS.map((group) => group.key));
  const keys = new Set<string>();
  for (const { key, group } of settingsSections) {
    if (!isKey(key)) throw new Error(`A registered Settings section's key is not a key: "${key}"`);
    if (isSettingsSection(key)) throw new Error(`A registered Settings section takes the key of a built-in section: "${key}"`);
    if (keys.has(key)) throw new Error(`Two registered Settings sections take the key "${key}"`);
    if (!groups.has(group)) throw new Error(`The registered Settings section "${key}" joins no group of Settings: "${group}"`);
    keys.add(key);
  }
  for (const registration of registrations) {
    for (const [language, strings] of Object.entries(registration.strings ?? {})) {
      if (!(LANGUAGES as readonly string[]).includes(language)) throw new Error(`Registered strings for a language the dashboard does not have: "${language}"`);
      const found = strings ? clash(SHIPPED, strings) : undefined;
      if (found?.why === "key") throw new Error(`A registered text's key is not a plain key: ${language} "${found.at}"`);
      if (found) throw new Error(`A registered text would be read in place of one of the dashboard's: ${language} "${found.at}"`);
    }
  }
  for (const registration of registrations) {
    for (const [language, strings] of Object.entries(registration.strings ?? {})) {
      if (strings) join(resources[language as Language].translation as unknown as Strings, strings);
    }
  }
  return { screens, settingsSections };
}

/**
 * A registration read once: its screens and sections as new arrays of new records, each field read once, and its
 * strings as a structured clone. `Array.from` builds the arrays, so a list's own `map` or species cannot hand back an
 * object that reads differently later. Strings that are not plain data, such as a proxy or a function, throw, as any
 * other failed check does.
 */
function copied(registration: Registration): Registration {
  const { screens, settingsSections, strings } = registration;
  let texts: Registration["strings"];
  try {
    texts = strings === undefined ? undefined : structuredClone(strings);
  } catch (error) {
    throw new Error(`A registration's strings are not plain data: ${error instanceof Error ? error.message : String(error)}`);
  }
  return {
    screens: screens === undefined ? undefined : Array.from(screens, (screen) => ({ ...screen })),
    settingsSections: settingsSections === undefined ? undefined : Array.from(settingsSections, (section) => ({ ...section })),
    strings: texts,
  };
}

type Strings = Record<string, unknown>;

const isTree = (value: unknown): value is Strings => typeof value === "object" && value !== null && !Array.isArray(value);

// Every language's texts as the app ships them, copied before any registration joins its own: a registered text is held
// to these, in every language, since a text missing in one language is read from another.
const SHIPPED = LANGUAGES.map((language) => structuredClone(resources[language].translation) as unknown as Strings);

// The suffixes i18next adds to a key to find a plural's or an ordinal's form of it, such as `count_zero`.
const PLURAL = /(?:_ordinal)?_(?:zero|one|two|few|many|other)$/;

/**
 * Whether i18next may read one key of a level for the other: they are equal once a plural or ordinal suffix is removed,
 * or one is the other with a context's `_<context>` added. A registered key is held to every built-in key this way, so
 * a registered `count_zero` is refused beside a built-in `count_one`, and a registered `title_short` beside `title`.
 */
function sameText(a: string, b: string): boolean {
  const x = a.replace(PLURAL, "");
  const y = b.replace(PLURAL, "");
  return x === y || x.startsWith(`${y}_`) || y.startsWith(`${x}_`);
}

/** A registered text `clash` refuses: its key's path, and whether its key is not a plain one or it meets a built-in text. */
interface Clash {
  at: string;
  why: "key" | "builtin";
}

/**
 * The first registered text that i18next could read in place of a built-in one, or that a built-in one would hide:
 * - a key that is not a plain key (`TEXT_KEY`), such as `"tasks.groups.count_zero"`, which i18next would also read as
 *   the path it spells, or a key every object has;
 * - a text whose key is one of the built-in texts' keys, or a plural, ordinal, or context form of one;
 * - a text where the dashboard has a group of texts, or the reverse.
 *
 * A group of texts joins the built-in group of its very name, and is held to it in turn.
 */
function clash(builtins: readonly Strings[], strings: Strings, path: readonly string[] = []): Clash | undefined {
  for (const [key, value] of Object.entries(strings)) {
    const at = [...path, key];
    if (RESERVED.has(key) || !TEXT_KEY.test(key)) return { at: at.join("."), why: "key" };
    for (const builtin of builtins) {
      for (const [name, text] of Object.entries(builtin)) {
        if (sameText(name, key) && !(name === key && isTree(text) && isTree(value))) return { at: at.join("."), why: "builtin" };
      }
    }
    if (isTree(value)) {
      const groups = builtins.flatMap((builtin) => (Object.hasOwn(builtin, key) && isTree(builtin[key]) ? [builtin[key]] : []));
      const found = clash(groups, value, at);
      if (found) return found;
    }
  }
  return undefined;
}

/**
 * Adds strings to a language's resource before the dashboard renders, deep. `clash` has already refused any text that
 * would be read in place of a built-in one, so this adds texts and replaces none. The resource is the object i18next was
 * started with, so the texts join the running instance at once, in one of the languages the app ships: no language is
 * added, and nothing is loaded from anywhere.
 */
function join(into: Strings, strings: Strings) {
  for (const [key, value] of Object.entries(strings)) {
    if (isTree(value)) {
      if (!Object.hasOwn(into, key)) into[key] = {};
      if (isTree(into[key])) join(into[key], value);
    } else if (!Object.hasOwn(into, key)) {
      into[key] = value;
    }
  }
}

const RegisteredContext = createContext<Registered>(NOTHING_REGISTERED);

/** Gives the shell below it what the registrations add. */
export const RegisteredProvider = RegisteredContext.Provider;

/** What the registrations add; nothing outside a provider. */
export function useRegistered(): Registered {
  return useContext(RegisteredContext);
}

/** A text whose key is built at run time, as a registered screen's or section's is, which the typed keys cannot hold. */
export function dynamicText(t: unknown, key: string): string {
  return (t as (key: string) => string)(key);
}
