import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { esES } from "../i18n/es-ES";
import { ptBR } from "../i18n/pt-BR";

const ui = join(__dirname, "..", "..");
const fontsCss = readFileSync(join(__dirname, "fonts.css"), "utf8");
const tokensCss = readFileSync(join(__dirname, "tokens.css"), "utf8");

interface Face {
  family: string;
  url: string;
  ranges: [number, number][];
}

const faces: Face[] = [...fontsCss.matchAll(/@font-face\s*{([^}]*)}/g)].map(([, body]) => {
  const family = /font-family:\s*"([^"]+)"/.exec(body)?.[1] ?? "";
  const url = /src:\s*url\("([^"]+)"\)/.exec(body)?.[1] ?? "";
  const range = /unicode-range:\s*([^;]+);/.exec(body)?.[1] ?? "";
  const ranges = range.split(",").map((part) => {
    const [from, to] = part.trim().replace(/^U\+/, "").split("-");
    return [parseInt(from, 16), parseInt(to ?? from, 16)] as [number, number];
  });
  return { family, url, ranges };
});

function stack(token: string): string[] {
  const value = new RegExp(`${token}:\\s*([^;]+);`).exec(tokensCss)?.[1] ?? "";
  return value.split(",").map((name) => name.trim().replace(/^"|"$/g, ""));
}

type Tree = { [k: string]: string | Tree };

function strings(tree: Tree): string[] {
  return Object.values(tree).flatMap((v) => (typeof v === "string" ? [v] : strings(v)));
}

describe("bundled fonts", () => {
  it("declares Inter, Inter Display, and JetBrains Mono", () => {
    expect(new Set(faces.map((f) => f.family))).toEqual(new Set(["Inter", "Inter Display", "JetBrains Mono"]));
  });

  it("loads every face from a WOFF2 file the app ships, with its license beside it", () => {
    for (const face of faces) {
      expect(face.url, face.family).toMatch(/^\/fonts\/[a-z-]+\/[a-z0-9-]+\.woff2$/);
      const file = join(ui, "public", face.url);
      expect(existsSync(file), file).toBe(true);
      expect(readFileSync(file).subarray(0, 4).toString("latin1"), file).toBe("wOF2");
      const license = readFileSync(join(dirname(file), "LICENSE"), "utf8");
      expect(license).toContain("SIL OPEN FONT LICENSE Version 1.1");
    }
  });

  it("starts each font stack with its bundled font and keeps the system's fonts after it", () => {
    expect(stack("--hf-font-sans").slice(0, 3)).toEqual(["Inter", "SF Pro Text", "-apple-system"]);
    expect(stack("--hf-font-display").slice(0, 3)).toEqual(["Inter Display", "SF Pro Display", "-apple-system"]);
    expect(stack("--hf-font-mono").slice(0, 3)).toEqual(["JetBrains Mono", "SF Mono", "ui-monospace"]);
    expect(tokensCss).toMatch(/--hf-font-display-settings:\s*"opsz" 32;/);
  });

  it.each([
    ["pt-BR", ptBR],
    ["es-ES", esES],
  ])("covers every letter and punctuation mark of %s in Inter and in JetBrains Mono", (_, resources) => {
    // Symbols such as the arrows and ⌘ are outside the latin subsets; the system's fonts draw them.
    const text = strings(resources as unknown as Tree)
      .join("")
      .replace(/\p{S}/gu, "");
    for (const family of ["Inter", "Inter Display", "JetBrains Mono"]) {
      const ranges = faces.filter((f) => f.family === family).flatMap((f) => f.ranges);
      const missing = [...new Set(text)].filter((ch) => {
        const cp = ch.codePointAt(0) ?? 0;
        return !ranges.some(([from, to]) => cp >= from && cp <= to);
      });
      expect(missing, family).toEqual([]);
    }
  });
});
