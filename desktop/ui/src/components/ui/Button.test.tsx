// @vitest-environment happy-dom
// Button's link variant (harmflow-app#990), drawn with the look of a bare text-styled action and used by every one of
// them.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import { Button } from "./Button";

let root: Root | undefined;
let host: HTMLElement;

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await i18n.changeLanguage("en-US");
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
});

function render(node: ReactNode): HTMLElement {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
  return host;
}

const src = join(__dirname, "..", "..");
const appCss = readFileSync(join(src, "design", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

/** The declarations of the rule whose selector list is exactly `selectors`. */
function rule(selectors: string): Record<string, string> {
  const escaped = selectors.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/,\s*/g, ",\\s*");
  const match = new RegExp(`(?:^|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(appCss);
  if (!match) throw new Error(`no rule for ${selectors}`);
  return Object.fromEntries(
    match[1]
      .split(";")
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => [d.slice(0, d.indexOf(":")).trim(), d.slice(d.indexOf(":") + 1).trim()]),
  );
}

/** Every component and module file of the interface, outside the tests. */
function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

describe("Button's link variant", () => {
  it("renders a button with the link variant's class, an extra class, and its children", () => {
    const onClick = vi.fn();
    render(
      <Button variant="link" className="mono" onClick={onClick}>
        #9
      </Button>,
    );
    const button = host.querySelector("button")!;
    expect(button.type).toBe("button");
    expect([...button.classList]).toEqual(["btn", "btn--link", "btn--md", "mono"]);
    expect(button.textContent).toBe("#9");
    act(() => button.click());
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("draws no padding, border, or background, the link color, and an underline on hover", () => {
    expect(rule(".btn--link, .link-like")).toMatchObject({
      padding: "0",
      border: "none",
      background: "none",
      color: "var(--hf-color-blue)",
      "font-size": "var(--hf-font-size-sm)",
    });
    expect(rule(".btn--link")).toMatchObject({ "font-weight": "inherit", "white-space": "inherit" });
    expect(rule(".btn--link:hover:not(:disabled)")).toMatchObject({
      background: "none",
      color: "var(--hf-color-cyan)",
      "text-decoration": "underline",
    });
    expect(rule(".link-like")).toEqual({ color: "inherit" });
  });

  it("is the only text-styled action: no .link rule and no button with the link class outside the Button module", () => {
    expect(appCss).not.toMatch(/(^|[\s,])\.link(?![\w-])/m);
    const files = sourceFiles(src);
    expect(files).toContain(join(src, "components", "ui", "Button.tsx"));
    const bare = files
      .filter((file) => file !== join(src, "components", "ui", "Button.tsx"))
      .filter((file) => /className=["{`]+[^"}`]*\blink\b(?!-)/.test(readFileSync(file, "utf8")));
    expect(bare).toEqual([]);
  });
});
