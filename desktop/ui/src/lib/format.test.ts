import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { elapsedMs, formatBytes, formatDateTime, formatDuration, formatElapsed, formatRelative, formatTime } from "./format";

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
  const afternoon = "2026-10-20T15:30:45Z";

  // formatTime takes no zone, so the host is pinned to UTC; the patterns allow any space ICU puts between parts.
  beforeAll(() => {
    vi.stubEnv("TZ", "UTC");
  });

  afterAll(() => {
    vi.unstubAllEnvs();
  });

  it("says relative times in the selected language", () => {
    expect(formatRelative("2026-10-20T12:18:00Z", "en-US", now)).toMatch(/^in\s18\smin/);
    expect(formatRelative("2026-10-20T09:00:00Z", "en-US", now)).toMatch(/^3\shr\.?\sago$/);
    expect(formatRelative("2026-10-20T12:18:00Z", "pt-BR", now)).toMatch(/^em\s18\smin/);
    expect(formatRelative("2026-10-20T09:00:00Z", "pt-BR", now)).toMatch(/^há\s3\sh/);
    expect(formatRelative("2026-10-20T12:18:00Z", "es-ES", now)).toMatch(/^dentro\sde\s18\smin/);
    expect(formatRelative("2026-10-20T09:00:00Z", "es-ES", now)).toMatch(/^hace\s3\sh/);
  });

  it("tells the time on a 12-hour clock in en-US and a 24-hour one in pt-BR and es-ES", () => {
    expect(formatTime(afternoon, "en-US")).toMatch(/^03:30:45\sPM$/);
    expect(formatTime(afternoon, "en-US", false)).toMatch(/^03:30\sPM$/);
    for (const locale of ["pt-BR", "es-ES"]) {
      expect(formatTime(afternoon, locale)).toBe("15:30:45");
      expect(formatTime(afternoon, locale, false)).toBe("15:30");
    }
  });

  it("orders the date and names the month in the selected language", () => {
    expect(formatDateTime(afternoon, "en-US", "UTC")).toMatch(/^Oct\s20,\s2026,\s3:30\sPM$/);
    expect(formatDateTime(afternoon, "pt-BR", "UTC")).toMatch(/^20\sde\sout\.?\sde\s2026,?\s15:30$/);
    expect(formatDateTime(afternoon, "es-ES", "UTC")).toMatch(/^20\soct\.?\s2026,?\s15:30$/);
  });

  it("gives the date and time of the zone it is given", () => {
    // 01:30 on the 21st in UTC is 22:30 on the 20th in São Paulo, three hours behind.
    const pastMidnight = "2026-10-21T01:30:00Z";
    expect(formatDateTime(pastMidnight, "en-US", "UTC")).toMatch(/^Oct\s21,\s2026,\s1:30\sAM$/);
    expect(formatDateTime(pastMidnight, "en-US", "America/Sao_Paulo")).toMatch(/^Oct\s20,\s2026,\s10:30\sPM$/);
    expect(formatDateTime(pastMidnight, "pt-BR", "America/Sao_Paulo")).toMatch(/^20\sde\sout\.?\sde\s2026,?\s22:30$/);
    expect(formatDateTime(pastMidnight, "es-ES", "America/Sao_Paulo")).toMatch(/^20\soct\.?\s2026,?\s22:30$/);
  });

  it("formats sizes with the locale's separators", () => {
    expect(formatBytes(2.1 * 1024 ** 3, "en-US")).toBe("2.1 GB");
    expect(formatBytes(2.1 * 1024 ** 3, "pt-BR")).toBe("2,1 GB");
  });
});
