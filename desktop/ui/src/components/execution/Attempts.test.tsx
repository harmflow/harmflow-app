// harmflow-app#826: an attempt on a custom entry shows the entry's name and identifier beside its harness, never the
// value of any of the entry's assignments; an attempt on a home shows its identifier and the home's path.
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
});
