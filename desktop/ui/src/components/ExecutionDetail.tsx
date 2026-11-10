// The selected execution: its conversation alone, its events as they happen and replayed from the log, its raw
// output, files, commands, results, attempts with the prompt and session context each sent, and estimated token costs
// (harmflow-app#39). Observe and cancel only — no input reaches the agent.
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Execution, ExecutionSummary } from "../api/types";
import { isActive } from "../api/types";
import { eventText } from "../lib/daemonText";
import { toneClass, typeGlyph } from "../lib/taskType";
import { buildTimeline, changedFilesFromEvents } from "../lib/timeline";
import { actions, useStore } from "../state/store";
import { CostsTab } from "./CostsTab";
import { useCancel } from "./ExecutionCard";
import { HarnessLabel } from "./Shell";
import { Icon, type IconName } from "./Icon";
import { Banner, Button, Elapsed, EmptyState, StatusChip, Tabs, cx, type TabDef } from "./ui";
import { Attempts } from "./execution/Attempts";
import { Chat } from "./execution/Chat";
import { CommandList } from "./execution/CommandList";
import { FileList } from "./execution/FileList";
import { RawOutput } from "./execution/RawOutput";
import { Results } from "./execution/Results";
import { ScriptOutput } from "./execution/ScriptOutput";
import { ScriptProgram } from "./execution/ScriptProgram";
import { EventDetails, Timeline } from "./execution/Timeline";

export { RawCaptures } from "./execution/RawOutput";
export { SCRIPT_STREAMS } from "./execution/ScriptOutput";

type TabKey = "chat" | "events" | "output" | "program" | "raw" | "files" | "commands" | "results" | "attempts" | "costs";

const PROMINENT = new Set([
  "harness.switched",
  "policy.unenforced",
  "repository.unprotected",
  "work_item.conflicting_activity",
  "harness.no_alternative",
]);
const BANNER_ICON: Record<string, IconName> = { "harness.switched": "switch", "work_item.conflicting_activity": "alert" };

export function ExecutionDetail({ executionId, summary }: { executionId: string; summary?: ExecutionSummary }) {
  const { t, i18n } = useTranslation();
  const detail = useStore((s) => s.details[executionId]);
  const events = useStore((s) => s.events[executionId]);
  const logExpired = useStore((s) => s.logExpired[executionId]);
  // The tab the user chose; none, or one this execution lacks, shows the first of its list.
  const [tab, setTab] = useState<TabKey | null>(null);
  const [selectedEvent, setSelectedEvent] = useState<number | null>(null);
  const { cancel, pending } = useCancel();

  useEffect(() => {
    setTab(null);
    setSelectedEvent(null);
    void actions.loadExecution(executionId);
    void actions.loadEvents(executionId);
  }, [executionId]);

  const exec: (Execution | ExecutionSummary) | undefined = detail ?? summary;
  // The daemon's messages are translated as the timeline is built, so it is rebuilt when the language changes.
  const timeline = useMemo(() => buildTimeline(events ?? []), [events, i18n.language]);
  const files = useMemo(() => {
    const fromRecord = detail?.changedFiles ?? [];
    const merged = [...fromRecord];
    for (const f of changedFilesFromEvents(events ?? [])) if (!merged.includes(f)) merged.push(f);
    return merged;
  }, [detail, events]);
  // Settings can hide the banner of each restriction the harness does not enforce; the Events tab still lists them.
  const showRestrictions = useStore((s) => s.settings?.showRestrictionWarnings ?? true);
  const prominent = useMemo(
    () => (events ?? []).filter((e) => PROMINENT.has(e.type) && (showRestrictions || e.type !== "policy.unenforced")),
    [events, showRestrictions],
  );
  const outputLines = useMemo(() => (events ?? []).filter((e) => e.type === "script.output"), [events]);
  // The chat lists each session message under its attempt, so it counts only those the daemon stamped with one.
  const agentMessages = useMemo(() => (events ?? []).filter((e) => e.type === "harness.message" && e.attempt), [events]);

  if (!exec) {
    return (
      <section className="detail panel">
        <EmptyState icon="terminal" title={t("executions.detail.selectHint")} />
      </section>
    );
  }

  const active = isActive(exec.status);
  const workItem = detail?.workItems?.[0];
  const branch = workItem?.branch;
  const issueNumber = workItem?.issueNumber ?? parseIssue(exec.workItem);
  const commands = detail?.commands ?? [];
  const attempts = detail?.attempts ?? [];
  // A script task runs a program: its output and the program as it ran, and no harness, model, prompt, conversation, or
  // cost.
  const isScript = exec.taskType === "script";
  const glyph = typeGlyph(exec.taskType);

  const tabs: TabDef<TabKey>[] = isScript
    ? [
        { key: "events", label: t("executions.detail.tabs.events"), icon: "list", count: timeline.length },
        { key: "output", label: t("executions.detail.tabs.output"), icon: "terminal", count: outputLines.length },
        { key: "program", label: t("executions.detail.tabs.program"), icon: "prompt" },
        { key: "raw", label: t("executions.detail.tabs.raw"), icon: "file" },
        { key: "results", label: t("executions.detail.tabs.results"), icon: "pr" },
      ]
    : [
        { key: "chat", label: t("executions.detail.tabs.chat"), icon: "message", count: agentMessages.length },
        { key: "events", label: t("executions.detail.tabs.events"), icon: "list", count: timeline.length },
        { key: "raw", label: t("executions.detail.tabs.raw"), icon: "terminal" },
        { key: "files", label: t("executions.detail.tabs.files"), icon: "file", count: files.length },
        { key: "commands", label: t("executions.detail.tabs.commands"), icon: "prompt", count: commands.length },
        { key: "results", label: t("executions.detail.tabs.results"), icon: "pr" },
        { key: "attempts", label: t("executions.detail.tabs.attempts"), icon: "layers", count: attempts.length },
        { key: "costs", label: t("executions.detail.tabs.costs"), icon: "coin" },
      ];
  const shown = tabs.find((d) => d.key === tab)?.key ?? tabs[0].key;

  // With nothing chosen, the details follow the latest step.
  const selected = timeline.find((i) => i.id === selectedEvent) ?? timeline[timeline.length - 1];

  return (
    <section className="detail panel">
      <header className="detail__header">
        <span className={cx("exec-card__glyph exec-card__glyph--lg", toneClass(glyph.tone))} aria-hidden="true">
          <Icon name={glyph.icon} size={24} />
        </span>
        <div className="detail__heading">
          <div className="detail__title-row">
            <h2 className="detail__title">{exec.taskName}</h2>
            <span className="id-chip mono" title={exec.id}>
              {exec.id}
            </span>
            <StatusChip status={exec.status} />
          </div>
          <div className="detail__meta">
            <HarnessLabel harness={exec.harness} model={exec.model} taskType={exec.taskType} />
            {detail?.script?.program ? (
              <span className="meta mono" title={detail.script.program}>
                {detail.script.program.split("/").pop()}
              </span>
            ) : null}
            {exec.repository ? (
              <span className="meta mono">
                <Icon name="repo" size={14} /> {exec.repository}
              </span>
            ) : null}
            {issueNumber ? (
              <span className="meta mono">
                <Icon name="issue" size={14} /> #{issueNumber}
              </span>
            ) : null}
            {branch ? (
              <span className="meta mono">
                <Icon name="branch" size={14} /> {branch}
              </span>
            ) : null}
            <span className="meta mono">
              <Icon name="clock" size={14} /> <Elapsed start={exec.startedAt ?? exec.createdAt} end={exec.endedAt} locale={i18n.language} />
            </span>
          </div>
        </div>
        {active || exec.status === "queued" ? (
          <Button variant="danger-outline" icon="stop" size="lg" disabled={pending[exec.id]} onClick={() => void cancel(exec.id)}>
            {pending[exec.id] ? t("executions.card.cancelling") : t("common.cancel")}
          </Button>
        ) : null}
      </header>

      {prominent.length ? (
        <div className="detail__alerts">
          {prominent.slice(-3).map((e) => (
            <Banner key={e.id} tone={e.type === "harness.switched" ? "magenta" : e.type === "harness.no_alternative" ? "danger" : "warning"} icon={BANNER_ICON[e.type] ?? "shield"}>
              <strong>{t(`eventTypes.${e.type}` as never, { defaultValue: e.type }) as string}</strong>
              {e.message ? <span className="mono"> — {eventText(e)}</span> : null}
            </Banner>
          ))}
        </div>
      ) : null}

      <Tabs tabs={tabs} value={shown} onChange={setTab} />

      <div className="detail__body">
        {logExpired ? <Banner tone="neutral" icon="clock">{t("executions.detail.logExpired")}</Banner> : null}
        {shown === "chat" ? <Chat key={executionId} attempts={attempts} messages={agentMessages} logExpired={logExpired} locale={i18n.language} /> : null}
        {shown === "events" ? (
          <div className="detail__split">
            <Timeline items={timeline} active={active} selectedId={selected?.id} onSelect={setSelectedEvent} status={exec.status} />
            <EventDetails item={selected} events={events ?? []} locale={i18n.language} />
          </div>
        ) : null}
        {shown === "output" ? <ScriptOutput lines={outputLines} /> : null}
        {shown === "program" ? <ScriptProgram detail={detail} active={active} /> : null}
        {shown === "raw" ? <RawOutput key={executionId} executionId={executionId} active={active} /> : null}
        {shown === "files" ? <FileList files={files} /> : null}
        {shown === "commands" ? <CommandList commands={commands} locale={i18n.language} /> : null}
        {shown === "results" ? <Results detail={detail} summary={exec} /> : null}
        {shown === "attempts" ? <Attempts detail={detail} locale={i18n.language} /> : null}
        {shown === "costs" ? <CostsTab key={executionId} attempts={attempts} active={active} /> : null}
      </div>
      <p className="detail__footnote">
        <Icon name="shield" size={13} /> {t("executions.detail.noConversation")}
      </p>
    </section>
  );
}

function parseIssue(workItem?: string): number | undefined {
  const m = workItem?.match(/#(\d+)$/);
  return m ? Number(m[1]) : undefined;
}
