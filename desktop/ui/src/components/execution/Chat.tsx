import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { Attempt, HarmflowEvent } from "../../api/types";
import { formatTime } from "../../lib/format";
import { HarnessLabel } from "../Shell";
import { Icon } from "../Icon";
import { EmptyState, cx } from "../ui";

/**
 * The conversation alone: for each attempt, the prompt it sent, then the session messages it received, as the daemon kept
 * and redacted them — plain text, no Markdown — and no other event nor the session context. The
 * prompts live in the record and the messages in the log, so an expired log leaves the prompts.
 */
export function Chat({ attempts, messages, logExpired, locale }: { attempts: Attempt[]; messages: HarmflowEvent[]; logExpired?: boolean; locale: string }) {
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
