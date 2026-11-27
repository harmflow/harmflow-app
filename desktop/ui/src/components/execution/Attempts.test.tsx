// harmflow-app#826: an attempt on a custom entry shows the entry's name and identifier beside its harness, never the
// value of any of the entry's assignments; an attempt on a home shows its identifier and the home's path.
// harmflow-app#1176: an attempt on any other installation, Claude Code's included, shows it by its name too.
import { renderToString } from "react-dom/server";
import { beforeAll, describe, expect, it } from "vitest";
import type { Attempt, Execution } from "../../api/types";
import i18n from "../../i18n";
import { Attempts } from "./Attempts";

beforeAll(async () => {
  await i18n.changeLanguage("en-US");
});

const attempt = (over: Partial<Attempt>): Attempt => ({
  number: 1,
  harness: "codex",
  configuredModel: "default",
  configuredEffort: "default",
  startedAt: "2026-11-19T09:00:00Z",
  ...over,
});
const detail = (a: Attempt) => ({ attempts: [a] }) as unknown as Execution;

describe("Attempts", () => {
  it("shows a custom entry's name and identifier, and no home even when the record holds one", () => {
    const html = renderToString(
      <Attempts
        detail={detail(attempt({ installation: "custom-1", installationName: "Codex 2", installationHome: "/Users/someone/.codex-personal" }))}
        locale="en-US"
      />,
    );
    expect(html).toContain("Codex 2");
    expect(html).toContain("custom-1");
    expect(html).not.toContain(".codex-personal");
  });

  it("shows a home's identifier and its path", () => {
    const html = renderToString(
      <Attempts detail={detail(attempt({ installation: "~/.codex-b", installationHome: "/Users/someone/.codex-b" }))} locale="en-US" />,
    );
    expect(html).toContain("~/.codex-b");
    expect(html).toContain("/Users/someone/.codex-b");
  });

  // harmflow-app#1176: every attempt shows its installation by its name, whatever kind of installation it is.
  it("shows a detected home by its name, its identifier, once, and its path", () => {
    const html = renderToString(
      <Attempts
        detail={detail(attempt({ installation: "~/.codex", installationName: "~/.codex", installationHome: "/Users/someone/.codex" }))}
        locale="en-US"
      />,
    );
    expect(html).toContain("<strong>~/.codex</strong>");
    expect(html.split("~/.codex<").length - 1).toBe(1);
    expect(html).toContain("/Users/someone/.codex");
  });

  it("shows Claude Code's installation by its name, claude-code", () => {
    const html = renderToString(
      <Attempts
        detail={detail(attempt({ harness: "claude-code", installation: "claude-code", installationName: "claude-code" }))}
        locale="en-US"
      />,
    );
    expect(html).toContain("<strong>claude-code</strong>");
  });

  it("shows each attempt of a failover by its installation's name beside its harness, and no assignment's value", () => {
    const html = renderToString(
      <Attempts
        detail={
          {
            attempts: [
              attempt({ installation: "custom-1", installationName: "Personal", outcome: "quota_exhausted" }),
              attempt({ number: 2, harness: "claude-code", installation: "claude-code", installationName: "claude-code" }),
            ],
          } as unknown as Execution
        }
        locale="en-US"
      />,
    );
    const [first, second] = html.split('<li class="attempt"').slice(1);
    expect(first).toContain("<strong>Personal</strong>");
    expect(first).toContain("custom-1");
    expect(second).toContain("<strong>claude-code</strong>");
    expect(html).not.toContain("HARMFLOW_TEST_MARK");
  });

  it("shows the identifier alone for a record older than the installation's name", () => {
    const html = renderToString(<Attempts detail={detail(attempt({ installation: "~/.codex-b" }))} locale="en-US" />);
    expect(html).toContain("<strong>~/.codex-b</strong>");
  });
});
