import { describe, expect, it } from "vitest";
import { elapsedMs, formatBytes, formatDuration, formatElapsed, formatRelative } from "./format";

describe("formatDuration", () => {
  it.each([
    [0, "0s"],
    [45_000, "45s"],
    [512_000, "8m 32s"],
    [60_000, "1m 00s"],
    [3_840_000, "1h 04m"],
    [2 * 86_400_000 + 3 * 3_600_000, "2d 3h"],
    [-5_000, "0s"],
  ])("%d ms reads %s", (ms, text) => {
    expect(formatDuration(ms)).toBe(text);
  });
});

describe("elapsed time", () => {
  const start = "2026-10-20T19:23:14Z";

  it("runs to now while the execution has no end", () => {
    expect(elapsedMs(start, undefined, Date.parse("2026-10-20T19:31:46Z"))).toBe(512_000);
    expect(formatElapsed(start, undefined, Date.parse("2026-10-20T19:31:46Z"))).toBe("8m 32s");
  });

  it("stops at the end once there is one", () => {
    expect(formatElapsed(start, "2026-10-20T19:24:14Z", Date.parse("2026-10-21T00:00:00Z"))).toBe("1m 00s");
  });

  it("is zero without a start or with an unreadable date", () => {
    expect(elapsedMs(undefined)).toBe(0);
    expect(elapsedMs("not a date")).toBe(0);
  });
});

describe("locale formatting", () => {
  const now = Date.parse("2026-10-20T12:00:00Z");

  it("says relative times in the selected language", () => {
    expect(formatRelative("2026-10-20T12:18:00Z", "en-US", now)).toMatch(/18 min/);
    expect(formatRelative("2026-10-20T09:00:00Z", "pt-BR", now)).toMatch(/3 h/);
    expect(formatRelative("2026-10-20T12:18:00Z", "es-ES", now)).toMatch(/18 min/);
  });

  it("formats sizes with the locale's separators", () => {
    expect(formatBytes(2.1 * 1024 ** 3, "en-US")).toBe("2.1 GB");
    expect(formatBytes(2.1 * 1024 ** 3, "pt-BR")).toBe("2,1 GB");
  });
});
