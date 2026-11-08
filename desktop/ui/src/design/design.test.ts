import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { esES } from "../i18n/es-ES";
import { ptBR } from "../i18n/pt-BR";

const ui = join(__dirname, "..", "..");
const appCss = readFileSync(join(__dirname, "app.css"), "utf8");
const fontsCss = readFileSync(join(__dirname, "fonts.css"), "utf8");
const themeCss = readFileSync(join(__dirname, "theme.css"), "utf8");

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
  const value = new RegExp(`${token}:\\s*([^;]+);`).exec(themeCss)?.[1] ?? "";
  return value.split(",").map((name) => name.trim().replace(/^"|"$/g, ""));
}

/** Every @keyframes of the stylesheets beside this test, with the properties its frames set. */
function keyframes(): { file: string; name: string; properties: string[] }[] {
  return readdirSync(__dirname)
    .filter((file) => file.endsWith(".css"))
    .flatMap((file) =>
      [...readFileSync(join(__dirname, file), "utf8").matchAll(/@keyframes\s+([\w-]+)\s*{((?:[^{}]*{[^}]*})*)\s*}/g)].map(
        ([, name, frames]) => ({
          file,
          name,
          properties: [...frames.matchAll(/([\w-]+)\s*:/g)].map(([, property]) => property),
        }),
      ),
    );
}

interface Declaration {
  file: string;
  line: number;
  selector: string;
  property: string;
  value: string;
}

/** Every declaration of the stylesheets beside this test, with its line and the selector of the rule that holds it. */
function declarations(): Declaration[] {
  return readdirSync(__dirname)
    .filter((file) => file.endsWith(".css"))
    .flatMap((file) => {
      // Comments become spaces, so that the offsets, and the lines counted from them, stay those of the file.
      const css = readFileSync(join(__dirname, file), "utf8").replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
      const found: Declaration[] = [];
      const blocks: string[] = [];
      let start = 0;
      for (let i = 0; i < css.length; i++) {
        if (css[i] === "{") {
          blocks.push(css.slice(start, i).trim().replace(/\s+/g, " "));
          start = i + 1;
        } else if (css[i] === ";" || css[i] === "}") {
          const text = css.slice(start, i);
          const colon = text.indexOf(":");
          if (blocks.length > 0 && colon > 0) {
            const at = start + text.length - text.trimStart().length;
            found.push({
              file,
              line: css.slice(0, at).split("\n").length,
              selector: blocks[blocks.length - 1],
              property: text.slice(0, colon).trim(),
              value: text.slice(colon + 1).trim(),
            });
          }
          if (css[i] === "}") blocks.pop();
          start = i + 1;
        }
      }
      return found;
    });
}

const spacing = /^(?:(?:padding|margin)(?:-[a-z-]+)?|(?:row-|column-)?gap)$/;

/**
 * The only literal spacing lengths: optical nudges of one side by 1 or 2 px, which a token would round away. Each is
 * named by its selector and property.
 */
const hairlines = [
  { selector: ".tl-item__time", property: "padding-top" },
  { selector: ".tl-item__state", property: "padding-top" },
  { selector: ".costs-note svg", property: "margin-top" },
];

const isHairline = (d: Declaration) => hairlines.some((h) => h.selector === d.selector && h.property === d.property);

type Tree = { [k: string]: string | Tree };

function strings(tree: Tree): string[] {
  return Object.values(tree).flatMap((v) => (typeof v === "string" ? [v] : strings(v)));
}

describe("animations", () => {
  it("animates only transform and opacity, which WebKit runs on the compositor without repainting", () => {
    const found = keyframes();
    expect(found.map((k) => k.name)).toContain("scan");
    for (const { file, name, properties } of found) {
      expect(
        properties.filter((p) => p !== "transform" && p !== "opacity"),
        `@keyframes ${name} in ${file}`,
      ).toEqual([]);
    }
  });
});

describe("spacing", () => {
  const found = declarations().filter((d) => d.file !== "theme.css");

  it("reads every padding, margin, and gap from the spacing tokens, so a token's change reaches every component", () => {
    // A value passes when, once its spacing tokens are taken out, no length and no other custom property is left:
    // 0, auto, a token, or a calc() of tokens.
    const literal = (value: string) =>
      /\d*\.?\d+[a-z%]+|var\(/i.test(value.replace(/var\(--hf-space-[\w-]+\)/g, ""));
    const offending = found
      .filter((d) => spacing.test(d.property) && !isHairline(d) && literal(d.value))
      .map((d) => `${d.file}:${d.line} ${d.selector} { ${d.property}: ${d.value} }`);
    expect(offending).toEqual([]);
  });

  it("keeps as literals only the listed hairline offsets: one side, 1 or 2 px", () => {
    for (const hairline of hairlines) {
      const matches = found.filter((d) => d.selector === hairline.selector && d.property === hairline.property);
      expect(matches.map((d) => d.value), `${hairline.selector} { ${hairline.property} }`).toEqual([
        expect.stringMatching(/^[12]px$/),
      ]);
      expect(hairline.property).toMatch(/^(?:padding|margin)-(?:top|right|bottom|left)$/);
    }
  });

  it("defines the spacing scale in theme.css only, with every token the stylesheets read", () => {
    const scale = declarations().filter((d) => d.property.startsWith("--hf-space-"));
    expect(new Set(scale.map((d) => d.file))).toEqual(new Set(["theme.css"]));
    expect(Object.fromEntries(scale.map((d) => [d.property, d.value]))).toMatchObject({
      "--hf-space-0-5": "2px",
      "--hf-space-1-5": "6px",
    });
    const defined = new Set(scale.map((d) => d.property));
    const read = found.flatMap((d) => [...d.value.matchAll(/var\((--hf-space-[\w-]+)\)/g)].map(([, token]) => token));
    expect([...new Set(read)].filter((token) => !defined.has(token))).toEqual([]);
  });
});

describe("the History filter bar", () => {
  it("keeps a date field only as wide as its date, not the full width of the bar's line", () => {
    // A rule of app.css by its exact selector, with where it starts: of two rules of one class each, the later wins.
    const rule = (selector: string) => {
      const match = new RegExp(`(?:^|\\n)${selector.replace(/[.]/g, "\\.")}\\s*{([^}]*)}`).exec(appCss);
      expect(match, selector).not.toBeNull();
      return { at: match!.index, body: match![1] };
    };
    const input = rule(".input");
    const date = rule(".filters__date");
    expect(input.body).toMatch(/width:\s*100%;/);
    expect(date.body).toMatch(/(?:^|[\s;])width:\s*auto;/);
    expect(date.body).not.toMatch(/width:\s*100%/);
    expect(date.at).toBeGreaterThan(input.at);
  });
});

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
    expect(themeCss).toMatch(/--hf-font-display-settings:\s*"opsz" 32;/);
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
