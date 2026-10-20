import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import eventSchema from "../../../../contracts/schemas/event.schema.json";
import taskSchema from "../../../../contracts/schemas/task.schema.json";
import { enUS } from "./en-US";
import { esES } from "./es-ES";
import { ptBR } from "./pt-BR";
import { systemLanguage } from "./index";

type Tree = { [k: string]: string | Tree };

function leaves(tree: Tree, prefix = ""): string[] {
  return Object.entries(tree).flatMap(([k, v]) => (typeof v === "string" ? [`${prefix}${k}`] : leaves(v, `${prefix}${k}.`)));
}

function lookup(tree: Tree, key: string): unknown {
  return key.split(".").reduce<unknown>((node, part) => (node && typeof node === "object" ? (node as Tree)[part] : undefined), tree);
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") && !path.includes("/i18n/") ? [path] : [];
  });
}

describe("i18n resources", () => {
  const reference = leaves(enUS as unknown as Tree).sort();

  it.each([
    ["pt-BR", ptBR],
    ["es-ES", esES],
  ])("%s has exactly the keys of en-US", (_lang, resource) => {
    expect(leaves(resource as unknown as Tree).sort()).toEqual(reference);
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
        const tree = enUS as unknown as Tree;
        // A counted key may exist only in its plural forms, which i18next picks from the count.
        const defined =
          typeof lookup(tree, key) === "string" ||
          (typeof lookup(tree, `${key}_one`) === "string" && typeof lookup(tree, `${key}_other`) === "string");
        if (!defined) missing.push(`${file}: ${key}`);
      }
    }
    expect(missing).toEqual([]);
  });

  it("adopts the system language when official, en-US otherwise", () => {
    expect(systemLanguage(["pt-PT"])).toBe("pt-BR");
    expect(systemLanguage(["es-MX", "en-US"])).toBe("es-ES");
    expect(systemLanguage(["fr-FR", "de-DE"])).toBe("en-US");
    expect(systemLanguage([])).toBe("en-US");
  });
});
