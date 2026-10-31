import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import eventSchema from "../../../../contracts/schemas/event.schema.json";
import taskSchema from "../../../../contracts/schemas/task.schema.json";
import { enUS } from "./en-US";
import { esES } from "./es-ES";
import { ptBR } from "./pt-BR";
import i18n, { systemLanguage } from "./index";

type Tree = { [k: string]: string | Tree };

function leaves(tree: Tree, prefix = ""): string[] {
  return Object.entries(tree).flatMap(([k, v]) => (typeof v === "string" ? [`${prefix}${k}`] : leaves(v, `${prefix}${k}.`)));
}

function lookup(tree: Tree, key: string): unknown {
  return key.split(".").reduce<unknown>((node, part) => (node && typeof node === "object" ? (node as Tree)[part] : undefined), tree);
}

// A counted key may exist only in its plural forms, which i18next picks from the count.
function defined(tree: Tree, key: string): boolean {
  return (
    typeof lookup(tree, key) === "string" ||
    (typeof lookup(tree, `${key}_one`) === "string" && typeof lookup(tree, `${key}_other`) === "string")
  );
}

// The message keys of the API contract: the items of the MessageKey schema's enum block, its comment lines skipped.
function contractMessageKeys(): string[] {
  const lines = readFileSync(join(__dirname, "../../../../contracts/api/openapi.yaml"), "utf8").split("\n");
  const schema = lines.findIndex((line) => /^ {4}MessageKey:\s*$/.test(line));
  if (schema < 0) return [];
  const block = lines.findIndex((line, i) => i > schema && /^\s+enum:\s*$/.test(line));
  if (block < 0) return [];
  const keys: string[] = [];
  for (const line of lines.slice(block + 1)) {
    if (/^\s+#/.test(line)) continue;
    const item = line.match(/^\s+- ([a-z0-9_]+)\s*$/);
    if (!item) break;
    keys.push(item[1]);
  }
  return keys;
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") && !path.includes("/i18n/") ? [path] : [];
  });
}

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

  it("labels every notifiable event and execution status", () => {
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
