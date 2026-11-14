// @vitest-environment happy-dom
// The typeface choice of Settings → General (harmflow-app#486): a missing or unknown value reads as HarmFlow, and each
// option gives the three font stacks theme.css declares for it — the bundled fonts for HarmFlow, the platform's own for
// System, with no optical size forced on the display text. theme.css is in the document, and the root element's
// computed tokens are read after each choice, as the components read them.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { applyStoredTypeface, applyTypeface, resolveTypeface } from "./typeface";

let sheet: HTMLStyleElement;

beforeEach(() => {
  sheet = document.createElement("style");
  sheet.textContent = readFileSync(join(__dirname, "theme.css"), "utf8");
  document.head.appendChild(sheet);
  localStorage.clear();
});

afterEach(() => {
  sheet.remove();
  document.documentElement.removeAttribute("data-typeface");
  localStorage.clear();
});

/** A font token as the root element computes it, its families split and unquoted. */
function stack(token: string): string[] {
  const value = getComputedStyle(document.documentElement).getPropertyValue(token).trim();
  return value.split(",").map((name) => name.trim().replace(/^"|"$/g, ""));
}

const displaySettings = () => getComputedStyle(document.documentElement).getPropertyValue("--hf-font-display-settings").trim();

describe("the typeface setting", () => {
  it.each([undefined, null, "", "harmflow", "System", "serif"])("reads %j as HarmFlow", (setting) => {
    expect(resolveTypeface(setting)).toBe("harmflow");
    expect(applyTypeface(setting)).toBe("harmflow");
    expect(document.documentElement.hasAttribute("data-typeface")).toBe(false);
  });

  it("reads system as System", () => {
    expect(resolveTypeface("system")).toBe("system");
    expect(applyTypeface("system")).toBe("system");
    expect(document.documentElement.dataset.typeface).toBe("system");
  });

  it("gives HarmFlow the bundled stacks, with Inter's display cut at its optical size", () => {
    applyTypeface("harmflow");
    expect(stack("--hf-font-sans")[0]).toBe("Inter");
    expect(stack("--hf-font-display")[0]).toBe("Inter Display");
    expect(stack("--hf-font-mono")[0]).toBe("JetBrains Mono");
    expect(displaySettings()).toBe('"opsz" 32');
  });

  it("gives System the platform's own fonts in all three stacks, macOS's, then Windows's, then Linux's", () => {
    applyTypeface("system");
    const sans = ["-apple-system", "SF Pro", "Segoe UI Variable", "Segoe UI", "system-ui", "Cantarell", "Ubuntu", "Noto Sans", "DejaVu Sans", "sans-serif"];
    expect(stack("--hf-font-sans")).toEqual(sans);
    expect(stack("--hf-font-display")).toEqual(sans);
    expect(stack("--hf-font-mono")).toEqual(["ui-monospace", "SF Mono", "Cascadia Mono", "Consolas", "DejaVu Sans Mono", "Liberation Mono", "monospace"]);
    for (const token of ["--hf-font-sans", "--hf-font-display", "--hf-font-mono"]) {
      expect(stack(token).filter((f) => /^(Inter|JetBrains Mono)/.test(f)), token).toEqual([]);
    }
    // The display text keeps the font's own optical size.
    expect(displaySettings()).toBe("normal");
  });

  it("switches back to the bundled stacks when HarmFlow is chosen again", () => {
    applyTypeface("system");
    applyTypeface("harmflow");
    expect(stack("--hf-font-sans")[0]).toBe("Inter");
    expect(stack("--hf-font-mono")[0]).toBe("JetBrains Mono");
  });

  it("keeps the last choice for the next launch's first paint", () => {
    expect(applyStoredTypeface()).toBe("harmflow");
    applyTypeface("system");
    document.documentElement.removeAttribute("data-typeface");
    expect(applyStoredTypeface()).toBe("system");
    expect(document.documentElement.dataset.typeface).toBe("system");
  });
});
