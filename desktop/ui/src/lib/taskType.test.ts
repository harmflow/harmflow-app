import { describe, expect, it } from "vitest";
import { toneClass, typeGlyph } from "./taskType";

describe("a task type's glyph", () => {
  it("is the New task dialog's: code in blue, a violet sparkle, and a terminal in blue", () => {
    expect(typeGlyph("github")).toEqual({ icon: "code", tone: "blue" });
    expect(typeGlyph("general")).toEqual({ icon: "sparkle", tone: "violet" });
    expect(typeGlyph("script")).toEqual({ icon: "terminal", tone: "blue" });
  });

  it("falls back to a blue dot for a type it does not know, a missing one, or a name an object inherits", () => {
    for (const type of ["webhook", "", undefined, "constructor", "toString"]) {
      expect(typeGlyph(type), String(type)).toEqual({ icon: "dot", tone: "blue" });
    }
  });

  it("gives settings no type", () => {
    for (const type of ["github", "general", "script", "other"]) expect(typeGlyph(type).icon).not.toBe("settings");
  });

  it("names one modifier class per tone", () => {
    expect(toneClass("blue")).toBe("type-glyph--blue");
    expect(toneClass("violet")).toBe("type-glyph--violet");
  });
});
