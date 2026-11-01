import { describe, expect, it } from "vitest";
import { elapsedMs, formatBytes, formatDuration, formatElapsed, formatRelative } from "./format";

describe("formatDuration", () => {
  // The same durations in each language: the locale's narrow units, the two largest at most, no zero padding.
  it.each([
    [0, "en-US", "0s"],
    [45_000, "en-US", "45s"],
    [512_000, "en-US", "8m 32s"],
    [60_000, "en-US", "1m"],
    [3_840_000, "en-US", "1h 4m"],
    [2 * 86_400_000 + 3 * 3_600_000, "en-US", "2d 3h"],
    [2 * 86_400_000 + 3 * 3_600_000 + 512_000, "en-US", "2d 3h"],
    [-5_000, "en-US", "0s"],
    [0, "pt-BR", "0 s"],
    [45_000, "pt-BR", "45 s"],
    [512_000, "pt-BR", "8 min 32 s"],
    [60_000, "pt-BR", "1 min"],
    [3_840_000, "pt-BR", "1 h 4 min"],
    [2 * 86_400_000 + 3 * 3_600_000, "pt-BR", "2 dias 3 h"],
    [2 * 86_400_000 + 3 * 3_600_000 + 512_000, "pt-BR", "2 dias 3 h"],
    [-5_000, "pt-BR", "0 s"],
    [0, "es-ES", "0s"],
    [45_000, "es-ES", "45s"],
    [512_000, "es-ES", "8min 32s"],
    [60_000, "es-ES", "1min"],
    [3_840_000, "es-ES", "1h 4min"],
    [2 * 86_400_000 + 3 * 3_600_000, "es-ES", "2d 3h"],
    [2 * 86_400_000 + 3 * 3_600_000 + 512_000, "es-ES", "2d 3h"],
    [-5_000, "es-ES", "0s"],
  ])("%d ms in %s reads %s", (ms, locale, text) => {
    expect(formatDuration(ms, locale)).toBe(text);
  });
});

describe("elapsed time", () => {
  const start = "2026-10-20T19:23:14Z";

  it("runs to now while the execution has no end", () => {
    expect(elapsedMs(start, undefined, Date.parse("2026-10-20T19:31:46Z"))).toBe(512_000);
    expect(formatElapsed(start, undefined, "en-US", Date.parse("2026-10-20T19:31:46Z"))).toBe("8m 32s");
  });

  it("stops at the end once there is one", () => {
    expect(formatElapsed(start, "2026-10-20T19:24:14Z", "en-US", Date.parse("2026-10-21T00:00:00Z"))).toBe("1m");
  });

  it("reads in the language it is given", () => {
    expect(formatElapsed(start, "2026-10-20T19:31:46Z", "pt-BR")).toBe("8 min 32 s");
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
