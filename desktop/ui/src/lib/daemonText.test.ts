import { beforeAll, describe, expect, it } from "vitest";
import i18n from "../i18n";
import { daemonText, eventText } from "./daemonText";

describe("daemonText", () => {
  beforeAll(async () => {
    await i18n.changeLanguage("pt-BR");
  });

  it("translates a daemon text from its key and params", () => {
    const text = {
      message: 'skill "lint" cannot be found for Codex (the Codex configuration)',
      messageKey: "skill_not_found" as const,
      params: { name: "lint", harness: "Codex", where: "the Codex configuration", source: "harness" },
    };
    expect(daemonText(text)).toBe('a skill "lint" não foi encontrada para Codex (na configuração do Codex)');
  });

  it("translates what a text names: a status, a cron field, a screen, the settings that widen a tool list", () => {
    expect(daemonText({ message: "", messageKey: "execution_ended", params: { status: "cancelled" } })).toBe(
      "a execução já terminou (Cancelada)",
    );
    expect(
      daemonText({ message: "", messageKey: "cron_value_invalid", params: { field: "day_of_month", fieldName: "day of month", value: "32" } }),
    ).toBe('campo de dia do mês inválido "32"');
    expect(daemonText({ message: "", messageKey: "path_variable_undefined", params: { names: "ROOT", path: "${ROOT}/x" } })).toBe(
      'variável de caminho ROOT não definida em "${ROOT}/x"; defina-a em Configurações',
    );
    expect(
      daemonText({ message: "", messageKey: "restriction_claude_allow_rules", params: { where: "user and local settings", sources: "user_local" } }),
    ).toBe(
      "o Claude Code soma as regras de permissão das suas configurações de usuário e locais à lista de ferramentas da tarefa, " +
        "e elas permitem mais que a lista ou não puderam ser lidas; as configurações gerenciadas não são verificadas",
    );
  });

  it("picks the plural form from the count", () => {
    const running = (count: string) => daemonText({ message: "", messageKey: "executions_running", params: { count } });
    expect(running("1")).toBe("1 execução está em andamento; confirme para parar o daemon e cancelá-la");
    expect(running("3")).toBe("3 execuções estão em andamento; confirme para parar o daemon e cancelá-las");
  });

  it("translates a daemon text quoted in another, at every depth, and quotes a harness's own error as it is", () => {
    const unavailable = {
      harness: "Codex",
      reason: "harness unavailable",
      errorClass: "harness_unavailable",
      detail: "Codex is unavailable: Codex is not signed in; run `codex login` in a terminal",
      detailKey: "harness_unavailable",
      "detail.harness": "Codex",
      "detail.detail": "Codex is not signed in; run `codex login` in a terminal",
      "detail.detailKey": "not_signed_in",
      "detail.detail.name": "Codex",
      "detail.detail.command": "codex login",
    };
    expect(daemonText({ message: "", messageKey: "no_alternative_harness_detail", params: unavailable })).toBe(
      "Codex não pode continuar (harness indisponível) e nenhum harness alternativo está disponível: " +
        "Codex está indisponível: Codex não está conectado; execute `codex login` em um terminal",
    );
    const quota = { harness: "Codex", reason: "quota exhausted", errorClass: "quota_exhausted", detail: "You've hit your usage limit" };
    expect(daemonText({ message: "", messageKey: "no_alternative_harness_detail", params: quota })).toBe(
      "Codex não pode continuar (cota esgotada) e nenhum harness alternativo está disponível: You've hit your usage limit",
    );
  });

  it("names a task file before the error it quotes", () => {
    const syntax = {
      file: "broken.yml",
      detail: "YAML syntax error: line 3: did not find expected ',' or ']'",
      detailKey: "yaml_syntax",
      "detail.detail": "line 3: did not find expected ',' or ']'",
    };
    expect(daemonText({ message: "", messageKey: "task_file_error", params: syntax })).toBe(
      "broken.yml: erro de sintaxe YAML: line 3: did not find expected ',' or ']'",
    );
    expect(daemonText({ message: "", messageKey: "task_file_invalid", params: { file: "broken.yml" } })).toBe(
      "o arquivo da tarefa broken.yml não passa na validação",
    );
  });

  it("translates an event's message, and leaves the agent's words as they are", () => {
    expect(eventText({ message: "switching from Claude Code to Codex", messageKey: "harness_switched", params: { from: "Claude Code", to: "Codex" } })).toBe(
      "trocando de Claude Code para Codex",
    );
    expect(eventText({ message: "I opened the draft pull request." })).toBe("I opened the draft pull request.");
    expect(eventText({})).toBe("");
  });

  it("keeps the daemon's text when it has no key, or a key with no translation", () => {
    expect(daemonText({ message: "exit status 1" })).toBe("exit status 1");
    expect(daemonText({ message: "a newer daemon's text", messageKey: "not_a_key" as never })).toBe("a newer daemon's text");
  });
});
