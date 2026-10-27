// The schedule builder's frequencies, interval, and description. Which expressions the daemon accepts, and when they
// fire, is domain › TestScheduleFormats's and TestCronFields's; the description here follows the same rules.
import { describe, expect, it } from "vitest";
import "../i18n";
import { cronOf, describeCron, intervalOf, intervalText, parseCron, repeatOf, startRepeat, type GuidedRepeat } from "./schedule";
import { getIn, setScheduleKind } from "./taskForm";

describe("schedule frequencies", () => {
  it.each<[GuidedRepeat, string]>([
    [{ kind: "minutes", every: 5 }, "*/5 * * * *"],
    [{ kind: "minutes", every: 30 }, "*/30 * * * *"],
    [{ kind: "hourly", minute: 15 }, "15 * * * *"],
    [{ kind: "daily", hour: 0, minute: 0 }, "0 0 * * *"],
    [{ kind: "daily", hour: 18, minute: 45 }, "45 18 * * *"],
    [{ kind: "weekdays", hour: 9, minute: 0 }, "0 9 * * 1-5"],
    [{ kind: "weekly", hour: 8, minute: 30, days: [1, 3, 5] }, "30 8 * * 1,3,5"],
    [{ kind: "weekly", hour: 8, minute: 30, days: [0] }, "30 8 * * 0"],
    [{ kind: "weekly", hour: 9, minute: 0, days: [1, 2, 3, 4, 5] }, "0 9 * * 1,2,3,4,5"],
    [{ kind: "monthly", hour: 7, minute: 5, day: 31 }, "5 7 31 * *"],
  ])("writes %o as %s and reads it back", (repeat, cron) => {
    expect(cronOf(repeat)).toBe(cron);
    expect(repeatOf(cron)).toEqual(repeat);
  });

  it("writes the weekly days once each, in ascending order, Sunday as 0", () => {
    expect(cronOf({ kind: "weekly", hour: 9, minute: 0, days: [5, 0, 3, 5] })).toBe("0 9 * * 0,3,5");
  });

  it.each([
    ["a list of hours", "0 9,17 * * *"],
    ["a step on hours", "0 */2 * * *"],
    ["a month field", "0 9 * 1 *"],
    ["a macro", "@daily"],
    ["day names", "0 9 * * MON-FRI"],
    ["Sunday as 7", "0 9 * * 7"],
    ["days out of order", "0 9 * * 5,1"],
    ["a repeated day", "0 9 * * 1,1"],
    ["a leading zero", "09 9 * * *"],
    ["a doubled space", "0  9 * * *"],
    ["surrounding spaces", " 0 9 * * *"],
    ["a step that does not divide the hour", "*/7 * * * *"],
    ["a range of minutes", "0-30 * * * *"],
    ["both day fields", "0 9 1 * 1"],
    ["an hour out of range", "0 24 * * *"],
    ["a day of the month out of range", "0 9 32 * *"],
    ["six fields", "0 0 9 * * *"],
    ["an empty expression", ""],
  ])("opens %s in Custom: %s", (_what, cron) => {
    expect(repeatOf(cron)).toEqual({ kind: "custom" });
  });

  it("reads a midnight expression as Daily, though @daily fires at the same times", () => {
    expect(repeatOf("0 0 * * *")).toEqual({ kind: "daily", hour: 0, minute: 0 });
    expect(repeatOf("@daily")).toEqual({ kind: "custom" });
  });

  it("starts Cron at 0 9 * * 1-5, which reads as Weekdays at 09:00", () => {
    const cron = getIn<string>(setScheduleKind({}, "cron"), ["spec", "schedule", "cron"]);
    expect(cron).toBe("0 9 * * 1-5");
    expect(repeatOf(cron)).toEqual({ kind: "weekdays", hour: 9, minute: 0 });
  });

  it("starts a chosen frequency at the time of the expression it replaces", () => {
    expect(startRepeat("daily", "30 7 * * 1-5")).toEqual({ kind: "daily", hour: 7, minute: 30 });
    expect(startRepeat("weekly", "30 7 * * 1-5")).toEqual({ kind: "weekly", hour: 7, minute: 30, days: [1, 2, 3, 4, 5] });
    expect(startRepeat("weekly", "30 7 * * *")).toEqual({ kind: "weekly", hour: 7, minute: 30, days: [1] });
    expect(startRepeat("monthly", "30 7 * * *")).toEqual({ kind: "monthly", hour: 7, minute: 30, day: 1 });
    expect(startRepeat("hourly", "45 9 * * *")).toEqual({ kind: "hourly", minute: 45 });
    expect(startRepeat("daily", "15 * * * *")).toEqual({ kind: "daily", hour: 9, minute: 15 });
    expect(startRepeat("minutes", "*/10 * * * *")).toEqual({ kind: "minutes", every: 10 });
    expect(startRepeat("minutes", "0 9 * * *")).toEqual({ kind: "minutes", every: 15 });
  });

  it("starts a frequency chosen from Custom at its single time, or at 09:00", () => {
    expect(startRepeat("daily", "30 6 * 1 *")).toEqual({ kind: "daily", hour: 6, minute: 30 });
    expect(startRepeat("daily", "0 9,17 * * *")).toEqual({ kind: "daily", hour: 9, minute: 0 });
    expect(startRepeat("daily", "@hourly")).toEqual({ kind: "daily", hour: 9, minute: 0 });
    expect(startRepeat("weekdays", "")).toEqual({ kind: "weekdays", hour: 9, minute: 0 });
  });
});

describe("schedule interval", () => {
  it.each([
    ["1h", 1, "h"],
    ["30m", 30, "m"],
    ["90m", 90, "m"],
    ["2d", 2, "d"],
  ] as const)("reads %s as a number and a unit and writes it back", (text, every, unit) => {
    expect(intervalOf(text)).toEqual({ every, unit });
    expect(intervalText(every, unit)).toBe(text);
  });

  it.each(["1.5h", "30s", "0m", "030m", " 30m", "30M", "h", "", "99999999999999999m"])(
    "leaves %j to the text field, which the controls cannot show",
    (text) => {
      expect(intervalOf(text)).toBeUndefined();
    },
  );
});

describe("cron parsing", () => {
  it("reads the fields as the daemon does", () => {
    expect(parseCron("*/20 9-17/4 1,15 JAN-mar sun,7")).toEqual({
      minutes: [0, 20, 40],
      hours: [9, 13, 17],
      days: [1, 15],
      months: [1, 2, 3],
      weekdays: [0],
      daysStar: false,
      weekdaysStar: false,
    });
    // A value with a step runs to the end of its field.
    expect(parseCron("50/5 * * * *")?.minutes).toEqual([50, 55]);
    expect(parseCron("0 9 */10 * *")).toMatchObject({ days: [1, 11, 21, 31], daysStar: true });
  });

  it.each([
    "0 9 * *",
    "0 0 9 * * *",
    "60 * * * *",
    "0 9 0 * *",
    "0 9 * 13 *",
    "0 9 * * 8",
    "0 9 * * 5-1",
    "*/0 * * * *",
    "0 9 L * *",
    "0 9 * * 1#2",
    "0 9 15W * *",
    "0 9 ? * *",
    "@DAILY",
    "@reboot",
    "0 9 * * FRI-",
  ])("refuses %s, as the daemon does", (cron) => {
    expect(parseCron(cron)).toBeUndefined();
    expect(describeCron(cron, "en-US")).toBeUndefined();
  });
});

describe("cron description", () => {
  it.each([
    ["0 9 * * 1-5", "At 09:00, Monday through Friday"],
    ["0 9 * * mon-fri", "At 09:00, Monday through Friday"],
    ["0 9 * * 1,3,5", "At 09:00, on Mondays, Wednesdays, and Fridays"],
    ["0 9 * * 1-3,5", "At 09:00, on Monday through Wednesday and Fridays"],
    ["0 9 * * 7", "At 09:00, on Sundays"],
    ["0 9 * * 5-7", "At 09:00, on Sundays, Fridays, and Saturdays"],
    ["0 9 * * 0-7", "At 09:00"],
    ["45 18 * * *", "At 18:45"],
    ["0 9,17 * * *", "At 09:00 and 17:00"],
    ["0,30 9-11 * * *", "At 09:00, 09:30, 10:00, 10:30, 11:00, and 11:30"],
    ["* * * * *", "Every minute"],
    ["*/15 * * * *", "Every 15 minutes"],
    ["*/15 9-17 * * 1-5", "Every 15 minutes, between 09:00 and 17:59, Monday through Friday"],
    ["* 9 * * *", "Every minute, between 09:00 and 09:59"],
    ["30 * * * *", "At minute 30 of every hour"],
    ["15,45 * * * *", "At minutes 15 and 45 of every hour"],
    // A list that steps evenly through the hour reads as the step it is.
    ["0,30 * * * *", "Every 30 minutes"],
    // `*/7` restarts each hour, firing at :56 and again at :00, so it is no even step.
    ["*/7 * * * *", "At minutes 0, 7, 14, 21, 28, 35, 42, 49, and 56 of every hour"],
    ["0 */2 * * *", "At minute 0, every 2 hours"],
    ["0 9-17 * * *", "At minute 0, between 09:00 and 17:59"],
    ["0 1,3,5,7,9,11,13 * * *", "At minute 0, in hours 1, 3, 5, 7, 9, 11, and 13"],
    ["*/30 */2 * * *", "Every 30 minutes, in hours 0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, and 22"],
    ["0 9 15 * *", "At 09:00, on day 15 of the month"],
    ["0 9 1,15 * *", "At 09:00, on days 1 and 15 of the month"],
    ["0 9 1-7 * *", "At 09:00, on days 1 through 7 of the month"],
    ["0 9 * 1,7 *", "At 09:00, in January and July"],
    ["0 9 * JAN-MAR *", "At 09:00, January through March"],
    ["0 9 * 1-3,12 *", "At 09:00, in January through March and December"],
    // Both day fields restricted: either one matching fires the task.
    ["0 9 1 * MON", "At 09:00, on day 1 of the month or on Mondays"],
    ["0 9 1,15 * 1-5", "At 09:00, on days 1 and 15 of the month or Monday through Friday"],
    // A day field that holds every value matches every day, so the other one restricts nothing.
    ["0 9 1-31 * MON", "At 09:00"],
    // A day field written `*/N` counts as unrestricted: a day fires when both fields match it.
    ["0 9 1-7 * */2", "At 09:00, on days 1 through 7 of the month, if it is Sunday, Tuesday, Thursday, or Saturday"],
    ["0 9 */10 * 1-5", "At 09:00, on days 1, 11, 21, and 31 of the month, if it is Monday through Friday"],
    ["0 9 */2 * *", "At 09:00, on days 1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23, 25, 27, 29, and 31 of the month"],
    ["@yearly", "At 00:00, on day 1 of the month, in January"],
    ["@annually", "At 00:00, on day 1 of the month, in January"],
    ["@monthly", "At 00:00, on day 1 of the month"],
    ["@weekly", "At 00:00, on Sundays"],
    ["@daily", "At 00:00"],
    ["@midnight", "At 00:00"],
    ["@hourly", "At minute 0 of every hour"],
    [" 0  9 * * 1-5 ", "At 09:00, Monday through Friday"],
  ])("describes %s in en-US", (cron, text) => {
    expect(describeCron(cron, "en-US")).toBe(text);
  });

  it.each([
    ["0 9 * * 1-5", "Às 09:00, de segunda-feira a sexta-feira"],
    ["0 9 * * 1,3,5", "Às 09:00, às segundas-feiras, às quartas-feiras e às sextas-feiras"],
    ["0 9 * * 0", "Às 09:00, aos domingos"],
    ["0 9 1 * MON", "Às 09:00, no dia 1 do mês ou às segundas-feiras"],
    ["0 9 1-7 * */2", "Às 09:00, nos dias 1 a 7 do mês, se cair em domingo, terça-feira, quinta-feira ou sábado"],
    ["*/15 9-17 * * *", "A cada 15 minutos, entre 09:00 e 17:59"],
    ["* * * * *", "A cada minuto"],
    ["30 * * * *", "No minuto 30 de cada hora"],
    ["15,45 * * * *", "Nos minutos 15 e 45 de cada hora"],
    ["0 */2 * * *", "No minuto 0, a cada 2 horas"],
    ["0 1,3,5,7,9,11,13 * * *", "No minuto 0, nas horas 1, 3, 5, 7, 9, 11 e 13"],
    ["0 9 15 * *", "Às 09:00, no dia 15 do mês"],
    ["0 9 * JAN-MAR *", "Às 09:00, de janeiro a março"],
    ["0 9 * 1,7 *", "Às 09:00, em janeiro e julho"],
    ["@yearly", "Às 00:00, no dia 1 do mês, em janeiro"],
  ])("describes %s in pt-BR", (cron, text) => {
    expect(describeCron(cron, "pt-BR")).toBe(text);
  });

  it.each([
    ["0 9 * * 1-5", "A las 09:00, de lunes a viernes"],
    ["0 9 * * 1,3,5", "A las 09:00, los lunes, los miércoles y los viernes"],
    ["0 9 * * 0", "A las 09:00, los domingos"],
    ["0 9 1 * MON", "A las 09:00, el día 1 del mes o los lunes"],
    ["0 9 1-7 * */2", "A las 09:00, los días 1 a 7 del mes, si cae en domingo, martes, jueves o sábado"],
    ["*/15 9-17 * * *", "Cada 15 minutos, entre las 09:00 y las 17:59"],
    ["* * * * *", "Cada minuto"],
    ["30 * * * *", "En el minuto 30 de cada hora"],
    ["15,45 * * * *", "En los minutos 15 y 45 de cada hora"],
    ["0 */2 * * *", "En el minuto 0, cada 2 horas"],
    ["0 1,3,5,7,9,11,13 * * *", "En el minuto 0, en las horas 1, 3, 5, 7, 9, 11 y 13"],
    ["0 9 15 * *", "A las 09:00, el día 15 del mes"],
    ["0 9 * JAN-MAR *", "A las 09:00, de enero a marzo"],
    ["0 9 * 1,7 *", "A las 09:00, en enero y julio"],
    ["@yearly", "A las 00:00, el día 1 del mes, en enero"],
  ])("describes %s in es-ES", (cron, text) => {
    expect(describeCron(cron, "es-ES")).toBe(text);
  });
});
