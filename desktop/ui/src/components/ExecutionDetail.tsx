// The selected execution: its conversation alone, its events as they happen and replayed from the log, its raw
// output, files, commands, results, attempts with the prompt and session context each sent, and estimated token costs
// (harmflow-app#39). Observe and cancel only — no input reaches the agent.
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api/client";
import { native } from "../api/native";
import type { Attempt, Execution, ExecutionOutput, ExecutionSummary, HarmflowEvent, MessageKey, MessageParams } from "../api/types";
import { isActive } from "../api/types";
import type { Tone } from "../design/tokens";
import { daemonText, eventText, fieldText } from "../lib/daemonText";
import { formatBytes, formatDateTime, formatDuration, formatTime, elapsedMs } from "../lib/format";
import { buildTimeline, changedFilesFromEvents, toolKey, type TimelineItem } from "../lib/timeline";
import { actions, useStore } from "../state/store";
import { CostsTab } from "./CostsTab";
import { useCancel } from "./ExecutionCard";
import { HarnessLabel } from "./Shell";
import { Icon, type IconName } from "./Icon";
import { Banner, Button, Chip, CopyButton, Elapsed, EmptyState, KeyValue, StatusChip, Tabs, cx, type TabDef } from "./ui";

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
  const prominent = useMemo(() => (events ?? []).filter((e) => PROMINENT.has(e.type)), [events]);
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
        <span className="exec-card__glyph exec-card__glyph--lg" aria-hidden="true">
          <Icon name={exec.taskType === "github" ? "code" : isScript ? "terminal" : "settings"} size={24} />
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
              <Icon name="clock" size={14} /> <Elapsed start={exec.startedAt ?? exec.createdAt} end={exec.endedAt} />
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

/**
 * The conversation alone: for each attempt, the prompt it sent, then the session messages it received, as the daemon kept
 * and redacted them — plain text, no Markdown — and no other event nor the session context. The
 * prompts live in the record and the messages in the log, so an expired log leaves the prompts.
 */
function Chat({ attempts, messages, logExpired, locale }: { attempts: Attempt[]; messages: HarmflowEvent[]; logExpired?: boolean; locale: string }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLOListElement | null>(null);
  // Follows the end of the conversation until the user scrolls up to read.
  const following = useRef(true);
  useEffect(() => {
    if (ref.current && following.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [messages.length, attempts.length]);
  // A message whose attempt the record does not hold yet, the record being read again after the event, still shows.
  const numbers = [...new Set([...attempts.map((a) => a.number), ...messages.map((e) => e.attempt!)])].sort((a, b) => a - b);
  if (numbers.length === 0) return <EmptyState icon="message" title={t("executions.detail.noAttempts")} />;
  return (
    <ol
      className="chat"
      ref={ref}
      onScroll={(e) => {
        const el = e.currentTarget;
        following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }}
    >
      {numbers.map((number) => {
        const a = attempts.find((x) => x.number === number);
        const said = messages.filter((e) => e.attempt === number);
        return (
          <li key={number} className="chat__attempt">
            <header className="chat__header">
              <strong>{t("executions.detail.attempt", { number })}</strong>
              <HarnessLabel harness={a?.harness ?? said[0]?.harness} model={a?.model} />
            </header>
            {a ? <ChatTurn kind="prompt" label={t("executions.detail.prompt")} time={a.startedAt} text={a.prompt} locale={locale} /> : null}
            {said.map((e) => (
              <ChatTurn key={e.id} kind="agent" label={t("executions.detail.chat.agent")} time={e.timestamp} text={e.message} locale={locale} />
            ))}
            {said.length === 0 ? (
              <p className="chat__note muted">{logExpired ? t("executions.detail.chat.messagesExpired") : t("executions.detail.chat.noMessages")}</p>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}

/** One turn of the conversation, its text as React text so that nothing in it is read as markup. */
function ChatTurn({ kind, label, time, text, locale }: { kind: "prompt" | "agent"; label: string; time?: string; text?: string; locale: string }) {
  const { t } = useTranslation();
  return (
    <div className={cx("chat-turn", `chat-turn--${kind}`)}>
      <div className="chat-turn__meta">
        <Icon name={kind === "prompt" ? "prompt" : "message"} size={14} />
        <span className="chat-turn__label">{label}</span>
        <time className="mono" dateTime={time}>
          {formatTime(time, locale)}
        </time>
      </div>
      {/* A record made before prompts were recorded kept no prompt. */}
      {text ? <div className="chat-turn__text">{text}</div> : <span className="muted">{t("executions.detail.notRecorded")}</span>}
    </div>
  );
}

function Timeline({
  items,
  active,
  selectedId,
  onSelect,
  status,
}: {
  items: TimelineItem[];
  active: boolean;
  selectedId?: number;
  onSelect: (id: number) => void;
  status: string;
}) {
  const { t, i18n } = useTranslation();
  const listRef = useRef<HTMLOListElement | null>(null);
  const [follow, setFollow] = useState(true);
  const idPrefix = useId();
  const optionId = (id: number) => `${idPrefix}-event-${id}`;

  useEffect(() => {
    if (follow && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
  }, [items.length, follow]);

  // A step the user chose, by click or key, stops the timeline following the latest one.
  const select = (id: number) => {
    setFollow(false);
    onSelect(id);
  };

  // The list is one tab stop whose arrow keys step through the events, without wrapping (harmflow-app#190).
  const onKeyDown = (e: KeyboardEvent<HTMLOListElement>) => {
    if (e.target !== e.currentTarget || e.altKey || e.ctrlKey || e.metaKey) return;
    const at = items.findIndex((i) => i.id === selectedId);
    const last = items.length - 1;
    const moves: Partial<Record<string, number>> = { ArrowUp: Math.max(at - 1, 0), ArrowDown: Math.min(at + 1, last), Home: 0, End: last };
    const to = moves[e.key];
    if (to === undefined) return;
    e.preventDefault(); // the list does not scroll by itself, even at an edge
    if (to === at) return;
    select(items[to].id);
    listRef.current?.children[to]?.scrollIntoView({ block: "nearest" });
  };

  if (items.length === 0) {
    return (
      <div className="timeline timeline--empty">
        <EmptyState icon="list" title={t("executions.detail.noEvents")} />
      </div>
    );
  }

  return (
    <div className="timeline">
      <div className="timeline__tools">
        <label className="mini-toggle">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          <span>{t("executions.detail.follow")}</span>
        </label>
      </div>
      <ol
        className="timeline__list"
        ref={listRef}
        role="listbox"
        tabIndex={0}
        aria-label={t("executions.detail.timeline")}
        aria-activedescendant={selectedId !== undefined ? optionId(selectedId) : undefined}
        onKeyDown={onKeyDown}
      >
        {items.map((item) => {
          const label = item.title ?? (t(item.labelKey as never, { defaultValue: item.event.type }) as string);
          const running = item.event.type === "tool.started" && !item.completed && active;
          return (
            <li
              key={item.id}
              id={optionId(item.id)}
              role="option"
              aria-selected={item.id === selectedId}
              className={cx("tl-item", `tl-item--${item.tone}`, item.id === selectedId && "tl-item--selected")}
              onClick={() => {
                select(item.id);
                // WebKit focuses nothing on a click; the arrow keys work from here on.
                listRef.current?.focus({ preventScroll: true });
              }}
            >
              <span className="tl-item__time mono">{formatTime(item.event.timestamp, i18n.language)}</span>
              <span className="tl-item__rail" aria-hidden="true">
                <span className="tl-item__dot" />
              </span>
              <span className="tl-item__icon">
                <Icon name={item.icon} size={17} />
              </span>
              <span className="tl-item__text">
                <span className="tl-item__title">
                  {label}
                  {item.attempt && item.attempt > 1 ? <span className="tl-item__attempt">#{item.attempt}</span> : null}
                </span>
                {item.detail ? <span className="tl-item__detail mono">{item.detail}</span> : null}
              </span>
              <span className="tl-item__state">
                {running ? <span className="spinner" aria-label={t("executions.detail.running")} /> : null}
                {item.completed?.declined ? (
                  <Icon name="alert" size={14} className="tone-warning" />
                ) : item.completed && !item.completed.ok ? (
                  <Icon name="x" size={14} className="tone-danger" />
                ) : null}
              </span>
            </li>
          );
        })}
        {/* Not an event to select; the header's status says the same. */}
        {active ? (
          <li className="tl-item tl-item--pending" aria-hidden="true">
            <span className="tl-item__time mono">{formatTime(new Date().toISOString(), i18n.language)}</span>
            <span className="tl-item__rail" aria-hidden="true">
              <span className="tl-item__dot tl-item__dot--hollow" />
            </span>
            <span className="tl-item__icon">
              <Icon name="circle" size={17} />
            </span>
            <span className="tl-item__text">
              <span className="tl-item__title muted">{t(`status.${status}` as never)}…</span>
            </span>
          </li>
        ) : null}
      </ol>
    </div>
  );
}

function outputFor(item: TimelineItem, events: HarmflowEvent[]): string | undefined {
  const p = (item.event.payload ?? {}) as Record<string, unknown>;
  const pick = (o: Record<string, unknown>) =>
    [o.output, o.result, o.stdout, o.text, payloadError(o)].find((v): v is string => typeof v === "string" && v !== "");
  if (item.event.type === "tool.started") {
    const key = toolKey(item.event);
    const done = key ? events.find((e) => e.type === "tool.completed" && toolKey(e) === key) : undefined;
    if (done) return pick((done.payload ?? {}) as Record<string, unknown>) ?? eventText(done);
  }
  return pick(p) ?? (item.event.type === "harness.message" ? item.event.message : undefined);
}

/** A payload's error: the daemon's text in the interface's language, a harness's own error as it is. */
function payloadError(p: Record<string, unknown>): string | undefined {
  if (typeof p.error !== "string") return undefined;
  return daemonText({ message: p.error, messageKey: p.errorKey as MessageKey | undefined, params: p.errorParams as MessageParams | undefined });
}

function EventDetails({ item, events, locale }: { item?: TimelineItem; events: HarmflowEvent[]; locale: string }) {
  const { t } = useTranslation();
  const [moreOpen, setMoreOpen] = useState(false);
  if (!item) {
    return (
      <aside className="event-details">
        <EmptyState icon="terminal" title={t("executions.detail.noEventSelected")} />
      </aside>
    );
  }
  const e = item.event;
  const output = outputFor(item, events);
  const label = item.title ?? (t(item.labelKey as never, { defaultValue: e.type }) as string);
  const info = {
    id: e.id,
    type: e.type,
    severity: e.severity,
    attempt: e.attempt,
    harness: e.harness,
    workItemId: e.workItemId,
    payload: e.payload,
  };
  return (
    <aside className="event-details">
      <header className="event-details__header">
        <span>{t("executions.detail.eventDetails")}</span>
        <span className="mono muted">{formatTime(e.timestamp, locale)}</span>
      </header>
      <div className="event-details__card">
        <span className={cx("event-details__icon", `tone-bg-${item.tone}`)}>
          <Icon name={item.icon} size={20} />
        </span>
        <div className="event-details__text">
          <strong>{label}</strong>
          {item.detail ? <span className="mono muted">{item.detail}</span> : null}
        </div>
        {item.detail ? <CopyButton text={item.detail} /> : null}
      </div>
      {output ? (
        <>
          <h4 className="event-details__subtitle">{t("executions.detail.output")}</h4>
          <div className="codebox">
            <div className="codebox__tools">
              <CopyButton text={output} />
            </div>
            <pre className="mono">{output}</pre>
          </div>
        </>
      ) : null}
      <button type="button" className="disclosure" aria-expanded={moreOpen} onClick={() => setMoreOpen(!moreOpen)}>
        <Icon name={moreOpen ? "chevronDown" : "chevronRight"} size={14} />
        {t("executions.detail.additionalInfo")}
      </button>
      {moreOpen ? (
        <div className="codebox">
          <pre className="mono">{JSON.stringify(info, null, 2)}</pre>
        </div>
      ) : null}
    </aside>
  );
}

const RAW_POLL_MS = 2000;

/** Reads the capture from the daemon, and again every few seconds while the execution runs. */
function RawOutput({ executionId, active }: { executionId: string; active: boolean }) {
  const [output, setOutput] = useState<ExecutionOutput>();
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = async () => {
      // An unreachable daemon keeps the last capture shown; the next read retries.
      const next = await api.executionOutput(executionId).catch(() => undefined);
      if (stopped) return;
      if (next) setOutput(next);
      if (active) timer = setTimeout(() => void load(), RAW_POLL_MS);
    };
    void load();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [executionId, active]);
  return <RawCaptures output={output} />;
}

/** What the harness printed, attempt by attempt, as the daemon captured and redacted it. */
export function RawCaptures({ output }: { output?: ExecutionOutput }) {
  const { t, i18n } = useTranslation();
  const ref = useRef<HTMLPreElement | null>(null);
  // Follows the end of the output until the user scrolls up to read.
  const following = useRef(true);
  const captures = (output?.attempts ?? []).filter((c) => c.text !== "");
  const text = captures.map((c) => c.text).join("\n");
  useEffect(() => {
    if (ref.current && following.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [text]);
  if (!output) return <EmptyState icon="terminal" title={t("common.loading")} />;
  if (output.logExpired) return null; // the banner above says why
  if (captures.length === 0) return <EmptyState icon="terminal" title={t("executions.detail.noRaw")} />;
  const several = output.attempts.length > 1;
  return (
    <div className="codebox codebox--tall">
      <div className="codebox__tools">
        <CopyButton text={text} />
      </div>
      <pre
        className="mono"
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {captures.map((c) => (
          <span key={c.attempt} className="raw-attempt">
            {several ? (
              <span className="raw-note">
                {t("executions.detail.rawAttempt", {
                  number: c.attempt,
                  harness: t(`harness.names.${c.harness}` as never, { defaultValue: c.harness }) as string,
                })}
              </span>
            ) : null}
            {c.offset > 0 ? (
              <span className="raw-note">
                {t("executions.detail.rawTruncated", {
                  shown: formatBytes(c.size - c.offset, i18n.language),
                  size: formatBytes(c.size, i18n.language),
                })}
              </span>
            ) : null}
            {c.text}
          </span>
        ))}
      </pre>
    </div>
  );
}

/** A script's output, line by line, each marked with its stream, as the daemon redacted it. */
function ScriptOutput({ lines }: { lines: HarmflowEvent[] }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLPreElement | null>(null);
  const following = useRef(true);
  useEffect(() => {
    if (ref.current && following.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines.length]);
  if (lines.length === 0) return <EmptyState icon="terminal" title={t("executions.detail.script.noOutput")} />;
  const text = lines.map((e) => e.message ?? "").join("\n");
  return (
    <div className="codebox codebox--tall">
      <div className="codebox__tools">
        <CopyButton text={text} />
      </div>
      <pre
        className="mono script-output"
        ref={ref}
        onScroll={(e) => {
          const el = e.currentTarget;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {lines.map((e) => {
          const stream = (e.payload as { stream?: string } | undefined)?.stream === "stderr" ? "stderr" : "stdout";
          return (
            <span key={e.id} className={cx("script-line", `script-line--${stream}`)} data-stream={stream}>
              <span className="script-line__stream" aria-label={stream}>
                {t(`executions.detail.script.streams.${stream}`)}
              </span>
              {e.message ?? ""}
              {"\n"}
            </span>
          );
        })}
      </pre>
    </div>
  );
}

/** The program as it ran, its working directory and variables, and how it ended. */
function ScriptProgram({ detail, active }: { detail?: Execution; active: boolean }) {
  const { t } = useTranslation();
  const s = detail?.script;
  if (!s) return <EmptyState icon="prompt" title={t("executions.detail.script.notStarted")} />;
  const env = Object.entries(s.environment ?? {});
  const failed = s.signal !== undefined || (s.exitCode !== undefined && s.exitCode !== 0);
  const status =
    s.exitCode === undefined ? (
      <Chip tone={active ? "cyan" : "neutral"}>{active ? t("executions.detail.script.running") : "—"}</Chip>
    ) : (
      <Chip tone={failed ? "danger" : "success"} icon={failed ? "x" : "check"}>
        {s.signal ? t("executions.detail.script.signal", { signal: s.signal }) : t("executions.detail.script.exitCode", { code: s.exitCode })}
      </Chip>
    );
  // A row that does not apply to the program's form is left out rather than shown empty.
  const rows: [string, ReactNode][] = [
    [t("executions.detail.script.exitStatus"), status],
    [t("executions.detail.script.program"), <span className="mono">{s.program}</span>],
  ];
  if (s.command?.length) rows.push([t("executions.detail.script.command"), <ArgList args={s.command} />]);
  if (s.interpreter?.length) rows.push([t("executions.detail.script.interpreter"), <ArgList args={s.interpreter} />]);
  if (s.scriptPath) rows.push([t("executions.detail.script.scriptPath"), <span className="mono">{s.scriptPath}</span>]);
  rows.push([t("executions.detail.script.workingDirectory"), <span className="mono">{s.workingDirectory}</span>]);
  if (env.length) {
    rows.push([t("executions.detail.script.environment"), <span className="mono">{env.map(([k, v]) => `${k}=${v}`).join("\n")}</span>]);
  }
  return (
    <div className="results">
      <KeyValue items={rows} />
      {s.source ? (
        <div className="results__block">
          <h4>{t("executions.detail.script.source")}</h4>
          <div className="codebox">
            <div className="codebox__tools">
              <CopyButton text={s.source} />
            </div>
            <pre className="mono">{s.source}</pre>
          </div>
        </div>
      ) : null}
      <p className="muted small">{t("executions.detail.script.noHarness")}</p>
    </div>
  );
}

/** An argument list, one argument per line, so an argument with spaces reads as one. */
function ArgList({ args }: { args: string[] }) {
  return (
    <ol className="arg-list mono">
      {args.map((a, i) => (
        <li key={i}>{a === "" ? '""' : a}</li>
      ))}
    </ol>
  );
}

function FileList({ files }: { files: string[] }) {
  const { t } = useTranslation();
  if (files.length === 0) return <EmptyState icon="file" title={t("executions.detail.noFiles")} />;
  return (
    <ul className="file-list">
      {files.map((f) => (
        <li key={f} className="mono">
          <Icon name="file" size={14} /> {f}
        </li>
      ))}
    </ul>
  );
}

type CommandStatus = Execution["commands"][number]["status"];
/** A command the harness declined never ran, so it reads apart from a failure. */
const COMMAND_TONES: Record<CommandStatus, Tone> = { running: "cyan", succeeded: "success", failed: "danger", declined: "warning" };
const COMMAND_ICONS: Record<CommandStatus, IconName> = { running: "dot", succeeded: "check", failed: "x", declined: "alert" };

function CommandList({ commands, locale }: { commands: Execution["commands"]; locale: string }) {
  const { t } = useTranslation();
  if (commands.length === 0) return <EmptyState icon="prompt" title={t("executions.detail.noCommands")} />;
  return (
    <div className="table__scroll">
      <table className="table">
        <thead>
          <tr>
            <th>{t("history.columns.status")}</th>
            <th>{t("executions.detail.tabs.commands")}</th>
            <th>{t("executions.detail.exitCode")}</th>
            <th>{t("executions.detail.started")}</th>
            <th>{t("executions.detail.duration")}</th>
          </tr>
        </thead>
        <tbody>
          {commands.map((c) => (
            <tr key={c.id}>
              <td>
                <Chip tone={COMMAND_TONES[c.status]} icon={COMMAND_ICONS[c.status]}>
                  {t(`executions.detail.commandStatus.${c.status}`)}
                </Chip>
              </td>
              <td className="mono cell-wrap">{c.command}</td>
              <td className="mono">{c.exitCode ?? "—"}</td>
              <td className="mono">{formatTime(c.startedAt, locale)}</td>
              <td className="mono">{c.startedAt ? formatDuration(elapsedMs(c.startedAt, c.endedAt)) : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Results({ detail, summary }: { detail?: Execution; summary: ExecutionSummary }) {
  const { t } = useTranslation();
  const items = detail?.workItems ?? [];
  const warnings = detail?.warnings ?? [];
  if (!items.length && !summary.summary && !summary.errorMessage && !summary.pullRequestUrl && !warnings.length && !detail?.lastOperation) {
    return <EmptyState icon="pr" title={t("executions.detail.noResults")} />;
  }
  return (
    <div className="results">
      {summary.summary ? (
        <div className="results__block">
          <h4>{t("executions.detail.summary")}</h4>
          <p className="results__summary">{summary.summary}</p>
        </div>
      ) : null}
      {summary.errorMessage ? (
        <Banner tone="danger" icon="x">
          <span className="mono">{fieldText(summary.errorMessage, summary.errorKey, summary.errorParams)}</span>
        </Banner>
      ) : null}
      {items.map((w) => (
        <div className="results__block results__item" key={w.key}>
          <h4 className="mono">{w.title ? `${w.key} — ${w.title}` : w.key}</h4>
          <KeyValue
            items={[
              [t("executions.detail.branch"), w.branch ? <span className="mono">{w.branch}</span> : null],
              [
                t("executions.detail.tests"),
                w.tests?.status ? (
                  <Chip tone={w.tests.status === "passed" ? "success" : w.tests.status === "failed" ? "danger" : "neutral"} icon="test">
                    {t(`testsStatus.${w.tests.status}`)}
                  </Chip>
                ) : null,
              ],
              [
                t("executions.detail.pullRequest"),
                w.pullRequest?.url ? (
                  <button type="button" className="link mono" onClick={() => void native.openUrl(w.pullRequest!.url!)}>
                    #{w.pullRequest.number} <Icon name="external" size={12} />
                  </button>
                ) : null,
              ],
              [t("executions.detail.commits"), w.commits?.length ? <span className="mono">{w.commits.map((c) => c.slice(0, 7)).join(", ")}</span> : null],
              [t("executions.detail.workspace"), w.worktreePath ? <span className="mono">{w.worktreePath}</span> : null],
            ]}
          />
          {w.tests?.summary ? <pre className="mono results__tests">{w.tests.summary}</pre> : null}
        </div>
      ))}
      {!items.length && summary.pullRequestUrl ? (
        <Button icon="external" onClick={() => void native.openUrl(summary.pullRequestUrl!)}>
          {t("executions.detail.openPullRequest")}
        </Button>
      ) : null}
      {detail?.lastOperation ? (
        <div className="results__block">
          <h4>{t("executions.detail.lastOperation")}</h4>
          <p className="mono">{detail.lastOperation}</p>
        </div>
      ) : null}
      {warnings.length ? (
        <div className="results__block">
          <h4>{t("executions.detail.warnings")}</h4>
          {warnings.map((w, i) => (
            <Banner key={i} tone="warning" icon="shield">
              <strong>{t(`warnings.${w.code}`)}</strong> — {daemonText(w)}
            </Banner>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function Attempts({ detail, locale }: { detail?: Execution; locale: string }) {
  const { t } = useTranslation();
  const attempts = detail?.attempts ?? [];
  if (!attempts.length) return <EmptyState icon="layers" title={t("executions.detail.noAttempts")} />;
  return (
    <ol className="attempts">
      {attempts.map((a) => (
        <li key={a.number} className="attempt">
          <header className="attempt__header">
            <strong>{t("executions.detail.attempt", { number: a.number })}</strong>
            <HarnessLabel harness={a.harness} model={a.model} />
            {a.outcome ? (
              <Chip
                tone={a.outcome === "succeeded" ? "success" : a.outcome === "running" ? "cyan" : a.outcome === "quota_exhausted" || a.outcome === "harness_unavailable" ? "magenta" : a.outcome === "cancelled" ? "neutral" : "danger"}
              >
                {t(`attemptOutcome.${a.outcome}`)}
              </Chip>
            ) : null}
          </header>
          <KeyValue
            items={[
              [t("executions.detail.model"), <UsedValue used={a.model} configured={a.configuredModel} />],
              [t("executions.detail.effort"), <UsedValue used={a.effort} configured={a.configuredEffort} />],
              [t("executions.detail.harnessVersion"), a.harnessVersion ? <span className="mono">{a.harnessVersion}</span> : null],
              [t("executions.detail.reason"), fieldText(a.reason, a.reasonKey, a.reasonParams) ?? null],
              [t("executions.detail.started"), formatDateTime(a.startedAt, locale)],
              [t("executions.detail.duration"), <Elapsed start={a.startedAt} end={a.endedAt} />],
              [t("executions.detail.exitCode"), a.exitCode !== undefined ? <span className="mono">{a.exitCode}</span> : null],
              [
                t("executions.detail.error"),
                a.errorMessage ? <span className="mono">{fieldText(a.errorMessage, a.errorKey, a.errorParams)}</span> : null,
              ],
            ]}
          />
          <div className="attempt__input">
            <AttemptInput label={t("executions.detail.prompt")} text={a.prompt} />
            <AttemptInput label={t("executions.detail.sessionContext")} text={a.sessionContext} />
          </div>
        </li>
      ))}
    </ol>
  );
}

/** What the harness reported using, or that it reported nothing — never the configured value in its place. */
function UsedValue({ used, configured }: { used?: string; configured: string }) {
  const { t } = useTranslation();
  return (
    <>
      {used ? <span className="mono">{used}</span> : <span className="muted">{t("executions.detail.notReported")}</span>}
      {configured && configured !== used ? (
        <span className="muted"> · {t("executions.detail.configured", { value: configured })}</span>
      ) : null}
    </>
  );
}

/** What the attempt sent the harness, as the daemon recorded and redacted it; older records have none. */
function AttemptInput({ label, text }: { label: string; text?: string }) {
  const { t } = useTranslation();
  if (!text) {
    return (
      <div className="attempt-input attempt-input--empty">
        <span>{label}</span>
        <span className="muted">{t("executions.detail.notRecorded")}</span>
      </div>
    );
  }
  return (
    <details className="attempt-input">
      <summary>
        <Icon name="chevronRight" size={14} />
        {label}
      </summary>
      <div className="codebox">
        <div className="codebox__tools">
          <CopyButton text={text} />
        </div>
        <pre className="mono">{text}</pre>
      </div>
    </details>
  );
}
