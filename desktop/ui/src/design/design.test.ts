import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { esES } from "../i18n/es-ES";
import { ptBR } from "../i18n/pt-BR";

const ui = join(__dirname, "..", "..");
const src = join(__dirname, "..");
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

/**
 * Every declaration of the stylesheets under a folder, beside this test by default, with its file's path from that
 * folder, its line, and the selector of the rule that holds it.
 */
function declarations(dir = __dirname): Declaration[] {
  return filesUnder(dir)
    .filter((file) => file.endsWith(".css"))
    .flatMap((file) => {
      // Comments become spaces, so that the offsets, and the lines counted from them, stay those of the file.
      const css = readFileSync(join(dir, file), "utf8").replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
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

const typography = /^(?:font|font-size|font-weight|line-height|letter-spacing)$/;
const typographyToken = /^--hf-(?:font-size|font-weight|line-height|letter-spacing)(?:-[\w-]+)?$/;

/**
 * Whether a typography value holds a literal: once its typography tokens (and, in the font shorthand, its font
 * stacks) are taken out, a number, a named size or weight, or another custom property is left. A keyword such as
 * inherit or normal is none.
 */
const typographyLiteral = (value: string) =>
  /\d|var\(|(?<![\w-])(?:bold|bolder|lighter|medium|smaller|larger|(?:x{1,3}-)?(?:small|large))(?![\w-])/i.test(
    value.replace(/var\(--hf-(?:font|line-height|letter-spacing)[\w-]*\)/g, ""),
  );

/**
 * The only literal typography values: the line heights of the two badges, which follow the badge's box, not the type
 * scale. Each is named by its selector and property.
 */
const badgeLineHeights = [
  {
    selector: '.app[data-sidebar="collapsed"] .nav__badge',
    property: "line-height",
    why: "the collapsed sidebar's count badge: its line height is its 16px height, which centers the count",
  },
  {
    selector: ".badge",
    property: "line-height",
    why: "the unread notifications' badge: its line height is its 18px height, which centers the count",
  },
];

const isBadgeLineHeight = (d: Declaration) => badgeLineHeights.some((b) => b.selector === d.selector && b.property === d.property);

// The style properties of an inline style object or an editor theme that set typography, with their value up to the
// next property, and the SVG attributes that do, with theirs.
const typographySetting =
  /(?<![\w$.-])(?:font|fontSize|fontWeight|lineHeight|letterSpacing|font-size|font-weight|line-height|letter-spacing)\s*(?::\s*((?:"[^"\n]*"|'[^'\n]*'|`[^`]*`|[^,;}\n"'`])*)|=\s*("[^"]*"|'[^']*'|\{[^}\n]*\}))/g;

/** The literal typography values a component sets in an inline style, an editor theme, or an SVG attribute. */
function componentTypography(file: string, source: string): Literal[] {
  const text = withoutComments(source, /(?<![:"'`\\])\/\/[^\n]*/g);
  return [...text.matchAll(typographySetting)]
    .map((setting) => ({ at: setting.index, value: (setting[1] ?? setting[2]).trim() }))
    .filter(({ value }) => typographyLiteral(value))
    .map(({ at, value }) => ({ file, line: lineAt(text, at), value }));
}

describe("typography", () => {
  const found = declarations(src).filter((d) => d.file !== "design/theme.css");

  it("finds a literal typography value in a stylesheet's value or a component, and lets a token through", () => {
    expect(
      ["0.94em", "600", "1.6", "bold", "small", "var(--hf-space-2)", "calc(var(--hf-font-size-sm) * 2)"].filter(typographyLiteral),
    ).toEqual(["0.94em", "600", "1.6", "bold", "small", "var(--hf-space-2)", "calc(var(--hf-font-size-sm) * 2)"]);
    expect(
      ["var(--hf-font-size-mono)", "var(--hf-line-height-relaxed)", "inherit", "normal", "var(--hf-font-weight-medium) var(--hf-font-size-sm) var(--hf-font-sans)"].filter(
        typographyLiteral,
      ),
    ).toEqual([]);

    const tsx = [
      'const theme = { ".cm-scroller": { lineHeight: "1.6", fontSize: "var(--hf-font-size-sm)" } };',
      "export function A() {",
      '  return <svg><text fontSize={11} fontWeight="var(--hf-font-weight-bold)" /><p style={{ letterSpacing: "0.02em", gap: 4 }} /></svg>;',
      "}",
      '// lineHeight: "2"',
      "type Props = { fontSize: number };",
      'const label = { font: "600 12px Inter", fontFamily: "var(--hf-font-sans)" };',
      'const field = { font: "inherit" };',
    ].join("\n");
    expect(componentTypography("A.tsx", tsx).map(describeLiteral)).toEqual([
      'A.tsx:1: "1.6"',
      "A.tsx:3: {11}",
      'A.tsx:3: "0.02em"',
      'A.tsx:7: "600 12px Inter"',
    ]);
  });

  it("reads every font size, weight, line height, and letter spacing from the typography tokens, in every stylesheet but theme.css", () => {
    // The @font-face weight ranges of fonts.css describe the bundled font files, not a component's style.
    const offending = found
      .filter((d) => typography.test(d.property) && d.selector !== "@font-face" && !isBadgeLineHeight(d) && typographyLiteral(d.value))
      .map((d) => `${d.file}:${d.line} ${d.selector} { ${d.property}: ${d.value} }`);
    expect(offending).toEqual([]);
  });

  it("keeps as literals only the listed badge line heights, each a length in px", () => {
    for (const badge of badgeLineHeights) {
      const matches = found.filter((d) => d.selector === badge.selector && d.property === badge.property);
      expect(matches.map((d) => d.value), `${badge.selector} { ${badge.property} }: ${badge.why}`).toEqual([expect.stringMatching(/^\d+px$/)]);
    }
  });

  it("sets no literal typography value in a component's inline style, editor theme, or SVG attribute", () => {
    const sources = filesUnder(src).filter((file) => /\.tsx?$/.test(file) && !/\.test\.tsx?$/.test(file));
    expect(sources).toContain("components/YamlEditor.tsx");
    const offending = sources.flatMap((file) => componentTypography(file, readFileSync(join(src, file), "utf8")));
    expect(offending.map(describeLiteral)).toEqual([]);
  });

  it("defines the typography scale in theme.css only, with every token the stylesheets and components read", () => {
    const scale = declarations(src).filter((d) => typographyToken.test(d.property));
    expect(new Set(scale.map((d) => d.file))).toEqual(new Set(["design/theme.css"]));
    expect(Object.fromEntries(scale.map((d) => [d.property, d.value]))).toMatchObject({
      "--hf-font-size-mono": "0.94em",
      "--hf-line-height-tight": "1.2",
      "--hf-line-height": "1.45",
      "--hf-line-height-relaxed": "1.6",
      "--hf-letter-spacing-tight": "0.01em",
      "--hf-letter-spacing-label": "0.08em",
    });
    const defined = new Set(scale.map((d) => d.property));
    const components = filesUnder(src)
      .filter((file) => /\.tsx?$/.test(file) && !/\.test\.tsx?$/.test(file))
      .map((file) => readFileSync(join(src, file), "utf8"));
    const read = [...found.map((d) => d.value), ...components].flatMap((text) =>
      [...text.matchAll(/var\((--hf-[\w-]+)\)/g)].map(([, token]) => token).filter((token) => typographyToken.test(token)),
    );
    expect([...new Set(read)].filter((token) => !defined.has(token))).toEqual([]);
  });
});

// The CSS named colors. transparent and currentColor are left out: they name no color of their own.
const namedColors = new Set(
  (
    "aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood " +
    "cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray " +
    "darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen " +
    "darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue " +
    "firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew " +
    "hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan " +
    "lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue " +
    "lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine " +
    "mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise " +
    "mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange " +
    "orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum " +
    "powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna " +
    "silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet " +
    "wheat white whitesmoke yellow yellowgreen"
  ).split(" "),
);

interface Literal {
  file: string;
  line: number;
  value: string;
}

/**
 * The literal colors of a CSS value, with where each starts in it: a hex value, rgb() or hsl() whose channels are not
 * a token, or a named color. rgba(var(--hf-rgb-…), <alpha>) takes its channels from a token and is none.
 */
function colorsIn(value: string): { at: number; value: string }[] {
  const unquoted = value.replace(/"[^"]*"|'[^']*'/g, (s) => " ".repeat(s.length));
  const matches = [
    ...unquoted.matchAll(/#(?:[0-9a-f]{8}|[0-9a-f]{6}|[0-9a-f]{3,4})(?![\w-])/gi),
    ...unquoted.matchAll(/(?<![\w-])(?:rgb|hsl)a?\((?!\s*var\()[^)]*\)/gi),
    ...[...unquoted.matchAll(/(?<![\w#-])[a-z]+(?![\w(-])/gi)].filter(([word]) => namedColors.has(word.toLowerCase())),
  ];
  return matches.map((m) => ({ at: m.index, value: m[0] })).sort((a, b) => a.at - b.at);
}

/** The line of an offset in a text, counted from 1. */
function lineAt(text: string, offset: number): number {
  return text.slice(0, offset).split("\n").length;
}

/** A text with its comments blanked out, so that offsets and lines stay where they were. */
function withoutComments(text: string, line: RegExp | null): string {
  const blank = (s: string) => s.replace(/[^\n]/g, " ");
  const blocks = text.replace(/\/\*[\s\S]*?\*\//g, blank);
  return line ? blocks.replace(line, blank) : blocks;
}

/** The literal colors of a stylesheet's declarations. */
function stylesheetColors(file: string, css: string): Literal[] {
  const text = withoutComments(css, null);
  return [...text.matchAll(/(?<![\w-])-{0,2}[a-z][\w-]*\s*:\s*([^;{}]+)(?=[;}])/gi)].flatMap((declaration) => {
    const start = declaration.index + declaration[0].length - declaration[1].length;
    return colorsIn(declaration[1]).map(({ at, value }) => ({ file, line: lineAt(text, start + at), value }));
  });
}

// The style properties of an inline style object that take a color, with their value up to the next property, and
// the SVG attributes that take one, with theirs.
const styleProperty =
  /(?<![\w$.])(?:[a-z]+Color|color|background(?:Image)?|border(?:Top|Right|Bottom|Left)?|outline|boxShadow|textShadow|fill|stroke)\s*:\s*((?:"[^"\n]*"|'[^'\n]*'|`[^`]*`|[^,}\n"'`])*)/g;
const svgAttribute = /(?<![\w-])(?:fill|stroke|stopColor|floodColor|lightingColor|color)=("[^"]*"|'[^']*'|\{[^}\n]*\})/g;

/**
 * The literal colors a component sets in an inline style or an SVG attribute: in its string literals there, but the
 * ones a comparison reads (`tone === "cyan"`), which set nothing.
 */
function componentColors(file: string, source: string): Literal[] {
  const text = withoutComments(source, /(?<![:"'`\\])\/\/[^\n]*/g);
  return [...text.matchAll(styleProperty), ...text.matchAll(svgAttribute)]
    .flatMap((setting) => {
      const start = setting.index + setting[0].length - setting[1].length;
      const strings = [...setting[1].matchAll(/"[^"]*"|'[^']*'|`[^`]*`/g)].filter(
        (string) =>
          !/[=!]=\s*$/.test(setting[1].slice(0, string.index)) && !/^\s*[=!]=/.test(setting[1].slice(string.index + string[0].length)),
      );
      return strings.flatMap((string) =>
        colorsIn(string[0].slice(1, -1)).map(({ at, value }) => ({ offset: start + string.index + 1 + at, value })),
      );
    })
    .sort((a, b) => a.offset - b.offset)
    .map(({ offset, value }) => ({ file, line: lineAt(text, offset), value }));
}

/**
 * The gradients whose stops only fill a mask: a mask reads how opaque its fill is, not its color, so their literal
 * stops set opacity and draw no color on screen. Each is named here with its file, and nothing else is allowed.
 */
const maskGradients = [
  {
    file: "components/Icon.tsx",
    id: "fade",
    why: "the logo's fade: its white stops fill the mask that fades the wave's two ends out",
  },
];

/** Where a gradient's element starts and ends in a source, from its id. */
function gradientAt(source: string, id: string): { start: number; end: number } | null {
  const match = new RegExp(`<(linearGradient|radialGradient)\\b[^>]*\\bid=\\{?["'\`][^"'\`]*\\b${id}["'\`]\\}?[^>]*>[\\s\\S]*?</\\1>`).exec(source);
  return match ? { start: match.index, end: match.index + match[0].length } : null;
}

/** Every file under a folder, by its path from that folder. */
function filesUnder(dir: string): string[] {
  return (readdirSync(dir, { recursive: true }) as string[]).map((path) => path.split("\\").join("/"));
}

const describeLiteral = ({ file, line, value }: Literal) => `${file}:${line}: ${value}`;

describe("colors", () => {
  it("finds a literal color in a stylesheet or a component, and lets a token through", () => {
    const css = [
      ".a {",
      "  color: #22d3ee;",
      "  border: 1px solid red;",
      "  background: rgb(1, 2, 3), hsl(200 50% 50%);",
      "  box-shadow: 0 0 4px rgba(var(--hf-rgb-cyan), 0.4);",
      "  fill: currentColor;",
      "  border-color: transparent;",
      "  /* color: #000; */",
      '  font-family: "Red Hat", var(--hf-font-sans);',
      "}",
      ".b:hover { color: var(--hf-color-cyan); background: var(--hf-rgb-white); }",
    ].join("\n");
    expect(stylesheetColors("x.css", css).map(describeLiteral)).toEqual([
      "x.css:2: #22d3ee",
      "x.css:3: red",
      "x.css:4: rgb(1, 2, 3)",
      "x.css:4: hsl(200 50% 50%)",
    ]);

    const tsx = [
      "export function A() {",
      '  return <svg><path fill="#22d3ee" stroke={tone === "cyan" ? color.cyan : "navy"} /><g style={{ color: "white", gap: 4 }} /></svg>;',
      "}",
      'const theme = { ".x": { backgroundColor: on ? "rgba(var(--hf-rgb-cyan), 0.05)" : "hsl(1, 2%, 3%)" } };',
      '// fill="#fff"',
      'const label = "issue #123 is red";',
    ].join("\n");
    expect(componentColors("A.tsx", tsx).map(describeLiteral)).toEqual(["A.tsx:2: #22d3ee", "A.tsx:2: navy", "A.tsx:2: white", "A.tsx:4: hsl(1, 2%, 3%)"]);
  });

  it("writes no literal color in a stylesheet but theme.css", () => {
    const found = filesUnder(src)
      .filter((file) => file.endsWith(".css") && file !== "design/theme.css")
      .flatMap((file) => stylesheetColors(file, readFileSync(join(src, file), "utf8")));
    expect(found.map(describeLiteral)).toEqual([]);
  });

  it("sets no literal color in a component's inline style or SVG attribute, but in a mask's gradient", () => {
    const sources = filesUnder(src).filter((file) => /\.tsx?$/.test(file) && !/\.test\.tsx?$/.test(file));
    expect(sources).toContain("design/theme.ts");
    const found = sources.flatMap((file) => {
      const source = readFileSync(join(src, file), "utf8");
      const allowed = maskGradients.filter((g) => g.file === file).flatMap((g) => gradientAt(source, g.id) ?? []);
      return componentColors(file, source).filter(({ line }) => !allowed.some(({ start, end }) => line >= lineAt(source, start) && line <= lineAt(source, end)));
    });
    expect(found.map(describeLiteral)).toEqual([]);
  });

  it.each(maskGradients)("allows the literal stops of $file's $id gradient, which only fills a mask", ({ file, id }) => {
    const source = readFileSync(join(src, file), "utf8");
    const gradient = gradientAt(source, id);
    expect(gradient, `the ${id} gradient in ${file}`).not.toBeNull();
    const reference = new RegExp(`["'\`-]${id}["'\`]`);
    const masks = [...source.matchAll(/<mask\b[\s\S]*?<\/mask>/g)].map((m) => m[0]);
    expect(masks.some((mask) => reference.test(mask)), `a <mask> in ${file} filled by ${id}`).toBe(true);
    // Outside its own element and the masks, nothing in the file uses the gradient.
    let rest = source.slice(0, gradient!.start) + source.slice(gradient!.end);
    for (const mask of masks) rest = rest.replace(mask, "");
    expect(rest.match(reference), `uses of ${id} outside a <mask>`).toBeNull();
  });
});

/** A rule of app.css by its exact selector, with where it starts: of two rules of one class each, the later wins. */
function rule(selector: string): { at: number; body: string } {
  const match = new RegExp(`(?:^|\\n)${selector.replace(/[.]/g, "\\.")}\\s*{([^}]*)}`).exec(appCss);
  expect(match, selector).not.toBeNull();
  return { at: match!.index, body: match![1] };
}

describe("the History filter bar", () => {
  it("keeps a date field only as wide as its date, not the full width of the bar's line", () => {
    const input = rule(".input");
    const date = rule(".filters__date");
    expect(input.body).toMatch(/width:\s*100%;/);
    expect(date.body).toMatch(/(?:^|[\s;])width:\s*auto;/);
    expect(date.body).not.toMatch(/width:\s*100%/);
    expect(date.at).toBeGreaterThan(input.at);
  });
});

describe("the tab row", () => {
  it("keeps its height in a flex column of fixed height, as the task editor and an execution's detail are", () => {
    // overflow-x makes the row a scroll container, whose automatic minimum height is 0: without flex-shrink: 0, a
    // column whose content is taller than it shrinks the row to its border (harmflow-app#912).
    const tabs = rule(".tabs");
    expect(tabs.body).toMatch(/(?:^|[\s;])flex-shrink:\s*0;/);
    expect(tabs.body).toMatch(/overflow-x:\s*auto;/);
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

const iconSources = join(ui, "..", "src-tauri", "icons", "source");

/** The brand's images whose letters are outlines: no font, bundled or the system's, may set them. */
const wordmarks = ["wordmark.svg", "wordmark-one-color.svg", "lockup.svg"];

/**
 * What in an SVG would set its letters in a font: a text or tspan element, a font-family, a font shorthand in a style
 * attribute or a style block, or an @font-face rule. Each is found by its line.
 */
function fontReferences(file: string, svg: string): Literal[] {
  return [...svg.matchAll(/<text|<tspan|font-family|(?<![\w-])font\s*:|@font-face/gi)].map((m) => ({
    file,
    line: lineAt(svg, m.index),
    value: m[0],
  }));
}

describe("the wordmark and the lockup", () => {
  it("finds live text or a font in an SVG, and lets outlined paths through", () => {
    const svg = [
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 32">',
      '  <text x="0" y="24">HarmFlow</text>',
      '  <g font-family="Inter"><TSPAN>Flow</TSPAN></g>',
      '  <style>@font-face { font-family: "Inter"; } .name { font: 600 12px Inter; }</style>',
      '  <path style="font:12px sans-serif" d="M0 0h4v4H0z" />',
      "</svg>",
    ].join("\n");
    expect(fontReferences("sample.svg", svg).map(describeLiteral)).toEqual([
      "sample.svg:2: <text",
      "sample.svg:3: font-family",
      "sample.svg:3: <TSPAN",
      "sample.svg:4: @font-face",
      "sample.svg:4: font-family",
      "sample.svg:4: font:",
      "sample.svg:5: font:",
    ]);

    const outlined = [
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 32">',
      '  <path fill="#22d3ee" d="M4 4h6v24H4zM14 4h6v24h-6z" />',
      '  <g id="letters"><path d="M24 4c6 0 10 4 10 12s-4 12-10 12z" /></g>',
      "</svg>",
    ].join("\n");
    expect(fontReferences("outlined.svg", outlined)).toEqual([]);
  });

  it.each(wordmarks)("draws %s in outlines, which no typeface choice can change", (file) => {
    const path = join(iconSources, file);
    expect(existsSync(path), path).toBe(true);
    expect(fontReferences(file, readFileSync(path, "utf8")).map(describeLiteral), file).toEqual([]);
  });
});
