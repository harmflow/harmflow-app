import { beforeAll, describe, expect, it } from "vitest";
import i18n from "../i18n";
import { daemonText } from "./daemonText";

describe("daemonText", () => {
  beforeAll(async () => {
    await i18n.changeLanguage("pt-BR");
  });

  it("translates a daemon text from its key and params (FS-012 FR-005)", () => {
    const text = {
      message: 'skill "lint" cannot be found for Codex (the Codex configuration)',
      messageKey: "skill_not_found" as const,
      params: { name: "lint", harness: "Codex", where: "the Codex configuration", source: "harness" },
    };
    expect(daemonText(text)).toBe('a skill "lint" não foi encontrada para Codex (na configuração do Codex)');
  });

  it("translates what a text names: a status, a cron field, a screen", () => {
    expect(daemonText({ message: "", messageKey: "execution_ended", params: { status: "cancelled" } })).toBe(
      "a execução já terminou (Cancelada)",
    );
    expect(
      daemonText({ message: "", messageKey: "cron_value_invalid", params: { field: "day_of_month", fieldName: "day of month", value: "32" } }),
    ).toBe('campo de dia do mês inválido "32"');
    expect(daemonText({ message: "", messageKey: "path_variable_undefined", params: { names: "ROOT", path: "${ROOT}/x" } })).toBe(
      'variável de caminho ROOT não definida em "${ROOT}/x"; defina-a em Configurações',
    );
  });

  it("picks the plural form from the count", () => {
    const running = (count: string) => daemonText({ message: "", messageKey: "executions_running", params: { count } });
    expect(running("1")).toBe("1 execução está em andamento; confirme para parar o daemon e cancelá-la");
    expect(running("3")).toBe("3 execuções estão em andamento; confirme para parar o daemon e cancelá-las");
  });

  it("keeps the daemon's text when it has no key, or a key with no translation", () => {
    expect(daemonText({ message: "exit status 1" })).toBe("exit status 1");
    expect(daemonText({ message: "a newer daemon's text", messageKey: "not_a_key" as never })).toBe("a newer daemon's text");
  });
});
